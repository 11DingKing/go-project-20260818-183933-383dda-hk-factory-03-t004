package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"sitesync/internal/logging"
)

// DeploymentService runs the five-step provisioning saga and its compensation.
type DeploymentService struct {
	deps Deps
}

// CreateOrderRequest is the wire input for opening a pilot deployment.
type CreateOrderRequest struct {
	Code                string   `json:"code"`
	CustomerID          string   `json:"customer_id"`
	FieldEngineerID     string   `json:"field_engineer_id"`
	DeviceIDs           []string `json:"device_ids"`
	BackfillWindowHours int      `json:"backfill_window_hours"`
}

// CreateOrder opens a draft deployment order with its five pending saga steps
// and the planned device bindings, all in one transaction so the order is never
// left without its skeleton.
func (s *DeploymentService) CreateOrder(ctx context.Context, actor string, req CreateOrderRequest) (domain.DeploymentOrder, error) {
	if strings.TrimSpace(req.Code) == "" || req.CustomerID == "" || req.FieldEngineerID == "" {
		return domain.DeploymentOrder{}, fmt.Errorf("%w: code, customer_id and field_engineer_id are required", errorsx.ErrValidation)
	}
	if len(req.DeviceIDs) == 0 {
		return domain.DeploymentOrder{}, fmt.Errorf("%w: at least one device is required", errorsx.ErrValidation)
	}
	if _, err := s.deps.Customers.GetCustomer(ctx, req.CustomerID); err != nil {
		return domain.DeploymentOrder{}, fmt.Errorf("create order: customer: %w", err)
	}
	if _, err := s.deps.Persons.GetPerson(ctx, req.FieldEngineerID); err != nil {
		return domain.DeploymentOrder{}, fmt.Errorf("create order: field engineer: %w", err)
	}
	for _, did := range req.DeviceIDs {
		dev, err := s.deps.Devices.GetDevice(ctx, did)
		if err != nil {
			return domain.DeploymentOrder{}, fmt.Errorf("create order: device %s: %w", did, err)
		}
		if dev.CustomerID != req.CustomerID {
			return domain.DeploymentOrder{}, fmt.Errorf("%w: device %s belongs to another customer", errorsx.ErrValidation, did)
		}
	}
	window := req.BackfillWindowHours
	if window <= 0 {
		window = s.deps.Cfg.Backfill.WindowHours
	}
	order := domain.DeploymentOrder{
		ID: uuid.NewString(), Code: req.Code, CustomerID: req.CustomerID,
		FieldEngineerID: req.FieldEngineerID, Status: domain.DeploymentDraft,
		HandlingMode: domain.HandlingOnSiteDebug, ResponsibleRole: domain.ResponsibleFieldEngineer,
		BackfillWindowHours: window,
	}
	err := s.deps.UOW.InTx(ctx, func(ctx context.Context) error {
		created, err := s.deps.Orders.CreateOrder(ctx, order)
		if err != nil {
			return err
		}
		order = created
		for _, did := range req.DeviceIDs {
			if err := s.deps.OrderDevices.BindDevice(ctx, order.ID, did); err != nil {
				return err
			}
		}
		s.deps.audit(ctx, actor, "ops_specialist", "order.create", "deployment_order", order.ID, "code="+order.Code)
		return nil
	})
	if err != nil {
		return domain.DeploymentOrder{}, err
	}
	return order, nil
}

// Provision runs the five-step saga, resuming from the first incomplete step.
// Each step commits independently so partial progress survives a crash; on any
// step failure the order falls back to pending_retry without re-running done steps.
func (s *DeploymentService) Provision(ctx context.Context, orderID, actor string) (domain.DeploymentOrder, []domain.DeploymentStep, error) {
	order, err := s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		return domain.DeploymentOrder{}, nil, notFound("deployment order", orderID)
	}
	if order.Status == domain.DeploymentActive {
		steps, _ := s.deps.Steps.ListStepsByOrder(ctx, orderID)
		return order, steps, nil
	}
	if order.Status == domain.DeploymentAborted || order.Status == domain.DeploymentCompensating {
		return order, nil, fmt.Errorf("%w: order %s is %s", errorsx.ErrIllegalTransition, orderID, order.Status)
	}
	if err := domain.AssertDeploymentTransition(order.Status, domain.DeploymentProvisioning); err != nil {
		return order, nil, err
	}
	if err := s.deps.Orders.UpdateStatus(ctx, order.ID, order.Status, domain.DeploymentProvisioning, order.Version); err != nil {
		return order, nil, err
	}
	order, err = s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		return order, nil, err
	}
	steps, err := s.deps.Steps.ListStepsByOrder(ctx, orderID)
	if err != nil {
		return order, nil, err
	}
	byName := map[string]domain.DeploymentStep{}
	for _, st := range steps {
		byName[st.StepName] = st
	}
	for _, name := range domain.OrderedSteps {
		st := byName[name]
		if st.Status == domain.StepDone || st.Status == domain.StepCompensated {
			continue
		}
		processing, perr := s.deps.Steps.MarkProcessing(ctx, st.ID, st.Version)
		if perr != nil {
			logging.FromContext(ctx).Warn("step claim conflict, skipping", zap.String("step", name), zap.Error(perr))
			continue
		}
		execErr := s.deps.UOW.InTx(ctx, func(ctx context.Context) error {
			if err := s.execStep(ctx, order, name, actor); err != nil {
				return err
			}
			if _, err := s.deps.Steps.MarkDone(ctx, processing.ID, processing.Version); err != nil {
				return err
			}
			return nil
		})
		if execErr != nil {
			_ = s.deps.Steps.MarkFailed(ctx, processing.ID, processing.Version, execErr.Error())
			_ = s.deps.Orders.BumpRetry(ctx, order.ID, execErr.Error())
			cur, _ := s.deps.Orders.GetOrder(ctx, orderID)
			if cur.ID != "" {
				_ = s.deps.Orders.UpdateStatus(ctx, order.ID, domain.DeploymentProvisioning, domain.DeploymentPendingRetry, cur.Version)
			}
			s.deps.audit(ctx, actor, "ops_specialist", "order.provision_failed", "deployment_order", orderID, "step="+name+" err="+execErr.Error())
			updated, _ := s.deps.Orders.GetOrder(ctx, orderID)
			return updated, nil, errorsx.Retryable(fmt.Errorf("step %s failed: %w", name, execErr), true)
		}
	}
	fresh, err := s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		return order, nil, err
	}
	if err := s.deps.Orders.UpdateStatus(ctx, order.ID, domain.DeploymentProvisioning, domain.DeploymentActive, fresh.Version); err != nil {
		return order, nil, err
	}
	s.deps.audit(ctx, actor, "ops_specialist", "order.provisioned", "deployment_order", orderID, "five steps completed")
	final, _ := s.deps.Orders.GetOrder(ctx, orderID)
	finalSteps, _ := s.deps.Steps.ListStepsByOrder(ctx, orderID)
	return final, finalSteps, nil
}

// execStep performs a single saga step's business effect inside the caller's tx.
func (s *DeploymentService) execStep(ctx context.Context, order domain.DeploymentOrder, stepName, actor string) error {
	switch stepName {
	case domain.StepRegisterOrder:
		if _, err := s.deps.Customers.GetCustomer(ctx, order.CustomerID); err != nil {
			return fmt.Errorf("register: customer missing: %w", err)
		}
		if _, err := s.deps.Persons.GetPerson(ctx, order.FieldEngineerID); err != nil {
			return fmt.Errorf("register: field engineer missing: %w", err)
		}
		return nil
	case domain.StepBindDevices:
		bindings, err := s.deps.OrderDevices.ListDevicesByOrder(ctx, order.ID)
		if err != nil {
			return fmt.Errorf("bind: list: %w", err)
		}
		if len(bindings) == 0 {
			return fmt.Errorf("%w: bind: no devices planned", errorsx.ErrIncomplete)
		}
		for _, b := range bindings {
			dev, err := s.deps.Devices.GetDevice(ctx, b.DeviceID)
			if err != nil {
				return fmt.Errorf("bind: device %s: %w", b.DeviceID, err)
			}
			if dev.CustomerID != order.CustomerID {
				return fmt.Errorf("%w: bind: device %s belongs to another customer", errorsx.ErrValidation, b.DeviceID)
			}
		}
		return nil
	case domain.StepActivateTrial:
		existing, err := s.deps.Trials.GetTrialByOrder(ctx, order.ID)
		if err == nil && existing.ID != "" {
			return nil
		}
		now := s.deps.now()
		trial := domain.Trial{
			ID: uuid.NewString(), OrderID: order.ID,
			EffectiveFrom: now, EffectiveTo: now.Add(time.Duration(s.deps.Cfg.Trial.DefaultWindowHours) * time.Hour),
			AcceptanceDeadline: now.Add(time.Duration(s.deps.Cfg.Trial.AcceptanceWindowHours) * time.Hour),
			Status:             domain.TrialActive,
		}
		created, err := s.deps.Trials.CreateTrial(ctx, trial)
		if err != nil {
			return fmt.Errorf("activate trial: %w", err)
		}
		if err := s.deps.Orders.LinkTrial(ctx, order.ID, created.ID, order.Version); err != nil {
			return fmt.Errorf("link trial: %w", err)
		}
		s.deps.audit(ctx, actor, "ops_specialist", "trial.activated", "trial", created.ID, "order="+order.ID)
		return nil
	case domain.StepDispatchInspection:
		if _, err := s.deps.Inspections.GetInspectionByOrderRound(ctx, order.ID, 1); err == nil {
			return nil
		}
		ins := domain.Inspection{
			ID: uuid.NewString(), OrderID: order.ID, Round: 1, Type: "first_round",
			ScheduledAt: s.deps.now(), Status: domain.InspectionDispatched, AssigneeID: order.FieldEngineerID,
		}
		if _, err := s.deps.Inspections.CreateInspection(ctx, ins); err != nil {
			return fmt.Errorf("dispatch inspection: %w", err)
		}
		return nil
	case domain.StepGenerateBill:
		if _, err := s.deps.Bills.GetBillByOrderPeriod(ctx, order.ID, 1); err == nil {
			return nil
		}
		now := s.deps.now()
		bill := domain.ReconciliationBill{
			ID: uuid.NewString(), OrderID: order.ID, PeriodNo: 1,
			PeriodStart: now, PeriodEnd: now.Add(30 * 24 * time.Hour),
			Status: domain.BillDraft, GeneratedBy: actor,
		}
		if _, err := s.deps.Bills.CreateBill(ctx, bill); err != nil {
			return fmt.Errorf("generate bill: %w", err)
		}
		return nil
	}
	return fmt.Errorf("unknown step %s", stepName)
}

// Compensate reverses every completed step of an order idempotently. It may be
// called repeatedly without side effects; already-compensated steps are skipped.
func (s *DeploymentService) Compensate(ctx context.Context, orderID, actor string) (domain.DeploymentOrder, []domain.DeploymentStep, error) {
	order, err := s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		return domain.DeploymentOrder{}, nil, notFound("deployment order", orderID)
	}
	if order.Status == domain.DeploymentAborted {
		steps, _ := s.deps.Steps.ListStepsByOrder(ctx, orderID)
		return order, steps, nil
	}
	if err := domain.AssertDeploymentTransition(order.Status, domain.DeploymentCompensating); err != nil {
		return order, nil, err
	}
	if err := s.deps.Orders.UpdateStatus(ctx, order.ID, order.Status, domain.DeploymentCompensating, order.Version); err != nil {
		return order, nil, err
	}
	steps, err := s.deps.Steps.ListStepsByOrder(ctx, orderID)
	if err != nil {
		return order, nil, err
	}
	byName := make(map[string]domain.DeploymentStep, len(steps))
	for _, x := range steps {
		byName[x.StepName] = x
	}
	for i := len(domain.OrderedSteps) - 1; i >= 0; i-- {
		name := domain.OrderedSteps[i]
		st := byName[name]
		if st.Status != domain.StepDone {
			continue
		}
		s.compensateStep(ctx, order, name)
		if err := s.deps.Steps.MarkCompensated(ctx, st.ID, st.Version); err != nil {
			if !isOptimisticConflict(err) {
				logging.FromContext(ctx).Warn("compensate step mark failed", zap.String("step", name), zap.Error(err))
			}
		}
	}
	cur, _ := s.deps.Orders.GetOrder(ctx, orderID)
	if cur.ID == "" {
		cur = order
	}
	if err := s.deps.Orders.UpdateStatus(ctx, order.ID, domain.DeploymentCompensating, domain.DeploymentAborted, cur.Version); err != nil && !isOptimisticConflict(err) {
		return order, nil, err
	}
	s.deps.audit(ctx, actor, "ops_specialist", "order.compensated", "deployment_order", orderID, "aborted")
	final, _ := s.deps.Orders.GetOrder(ctx, orderID)
	finalSteps, _ := s.deps.Steps.ListStepsByOrder(ctx, orderID)
	return final, finalSteps, nil
}

// compensateStep runs one compensating action. Each is idempotent and safe to repeat.
func (s *DeploymentService) compensateStep(ctx context.Context, order domain.DeploymentOrder, stepName string) {
	switch stepName {
	case domain.StepGenerateBill:
		bill, err := s.deps.Bills.GetBillByOrderPeriod(ctx, order.ID, 1)
		if err == nil && bill.ID != "" {
			if err := s.deps.Bills.UpdateBillStatus(ctx, bill.ID, domain.BillDraft, domain.BillVoided, bill.Version); err != nil && !isOptimisticConflict(err) {
				logging.FromContext(ctx).Warn("void bill failed", zap.Error(err))
			}
		}
	case domain.StepDispatchInspection:
		if _, err := s.deps.Inspections.CancelInspectionsByOrder(ctx, order.ID); err != nil {
			logging.FromContext(ctx).Warn("cancel inspections failed", zap.Error(err))
		}
	case domain.StepBindDevices:
		if _, err := s.deps.OrderDevices.UnbindAllDevices(ctx, order.ID); err != nil {
			logging.FromContext(ctx).Warn("unbind devices failed", zap.Error(err))
		}
	case domain.StepActivateTrial, domain.StepRegisterOrder:
		// no destructive compensation needed; the trial stays linked to an aborted order.
	}
}

// isOptimisticConflict reports whether err is a version/transition mismatch,
// which compensation treats as "already done" and therefore tolerates.
func isOptimisticConflict(err error) bool {
	return err != nil && (errors.Is(err, errorsx.ErrVersionConflict) || errors.Is(err, errorsx.ErrIllegalTransition))
}

// GetDetail returns an order with its steps, trial and bindings for inspection.
func (s *DeploymentService) GetDetail(ctx context.Context, orderID string) (OrderDetail, error) {
	order, err := s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		return OrderDetail{}, notFound("deployment order", orderID)
	}
	steps, _ := s.deps.Steps.ListStepsByOrder(ctx, orderID)
	bindings, _ := s.deps.OrderDevices.ListDevicesByOrder(ctx, orderID)
	detail := OrderDetail{Order: order, Steps: steps, Devices: bindings}
	if order.TrialID != "" {
		if t, err := s.deps.Trials.GetTrial(ctx, order.TrialID); err == nil {
			detail.Trial = &t
		}
	}
	return detail, nil
}

// OrderDetail is the read model for a deployment order.
type OrderDetail struct {
	Order   domain.DeploymentOrder    `json:"order"`
	Steps   []domain.DeploymentStep   `json:"steps"`
	Devices []domain.DeploymentDevice `json:"devices"`
	Trial   *domain.Trial             `json:"trial,omitempty"`
}
