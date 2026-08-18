package service

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"sitesync/internal/logging"
)

// TrialService manages trial acceptance, deadline detection and overdue
// escalation that transfers responsibility from field engineer to customer manager.
type TrialService struct {
	deps Deps
}

// Accept records trial acceptance. Only active or overdue trials may be accepted.
func (s *TrialService) Accept(ctx context.Context, trialID, by string) (domain.Trial, error) {
	t, err := s.deps.Trials.GetTrial(ctx, trialID)
	if err != nil {
		return domain.Trial{}, notFound("trial", trialID)
	}
	if t.Status == domain.TrialAccepted {
		return t, nil
	}
	if err := domain.AssertTrialTransition(t.Status, domain.TrialAccepted); err != nil {
		return t, err
	}
	if err := s.deps.Trials.SetAccepted(ctx, t.ID, by, s.deps.now(), t.Version); err != nil {
		return t, err
	}
	s.deps.audit(ctx, by, "customer_manager", "trial.accepted", "trial", t.ID, "")
	final, _ := s.deps.Trials.GetTrial(ctx, trialID)
	return final, nil
}

// Escalate transfers responsibility to the customer manager once a trial is past
// its deadline. It is idempotent: an already-escalated trial is a no-op that
// reports didEscalate=false so callers can avoid double-counting under concurrency.
func (s *TrialService) Escalate(ctx context.Context, trialID string) (domain.Trial, domain.DeploymentOrder, bool, error) {
	t, err := s.deps.Trials.GetTrial(ctx, trialID)
	if err != nil {
		return domain.Trial{}, domain.DeploymentOrder{}, false, notFound("trial", trialID)
	}
	order, err := s.deps.Orders.GetOrder(ctx, t.OrderID)
	if err != nil {
		return t, domain.DeploymentOrder{}, false, notFound("deployment order", t.OrderID)
	}
	if t.Status == domain.TrialEscalated || t.Status == domain.TrialConverted {
		return t, order, false, nil
	}
	if t.Status == domain.TrialActive {
		if err := s.deps.Trials.UpdateTrialStatus(ctx, t.ID, domain.TrialActive, domain.TrialOverdue, t.Version); err != nil {
			return t, order, false, err
		}
		t, _ = s.deps.Trials.GetTrial(ctx, trialID)
	}
	if err := domain.AssertTrialTransition(t.Status, domain.TrialEscalated); err != nil {
		return t, order, false, err
	}
	if err := s.deps.Trials.UpdateTrialStatus(ctx, t.ID, t.Status, domain.TrialEscalated, t.Version); err != nil {
		return t, order, false, err
	}
	if err := s.deps.Orders.UpdateResponsibility(ctx, order.ID, domain.ResponsibleCustomerMgr, domain.HandlingUrgeAccept, order.CustomerManagerID, order.Version); err != nil {
		logging.FromContext(ctx).Warn("escalate: transfer responsibility failed", zap.Error(err))
	}
	s.deps.audit(ctx, "scheduler", "scheduler", "trial.escalated", "trial", t.ID, "responsible->customer_manager")
	final, _ := s.deps.Trials.GetTrial(ctx, trialID)
	finalOrder, _ := s.deps.Orders.GetOrder(ctx, t.OrderID)
	return final, finalOrder, true, nil
}

// Convert ends an escalated trial by moving the handling mode to formal rent,
// sale or return. Used when acceptance cannot be obtained.
func (s *TrialService) Convert(ctx context.Context, trialID, actor string) (domain.Trial, domain.DeploymentOrder, error) {
	t, err := s.deps.Trials.GetTrial(ctx, trialID)
	if err != nil {
		return domain.Trial{}, domain.DeploymentOrder{}, notFound("trial", trialID)
	}
	if t.Status == domain.TrialConverted {
		order, _ := s.deps.Orders.GetOrder(ctx, t.OrderID)
		return t, order, nil
	}
	if err := domain.AssertTrialTransition(t.Status, domain.TrialConverted); err != nil {
		return t, domain.DeploymentOrder{}, err
	}
	if err := s.deps.Trials.UpdateTrialStatus(ctx, t.ID, t.Status, domain.TrialConverted, t.Version); err != nil {
		return t, domain.DeploymentOrder{}, err
	}
	order, _ := s.deps.Orders.GetOrder(ctx, t.OrderID)
	if order.ID != "" {
		_ = s.deps.Orders.UpdateResponsibility(ctx, order.ID, domain.ResponsibleCustomerMgr, domain.HandlingRentSale, order.CustomerManagerID, order.Version)
	}
	s.deps.audit(ctx, actor, "customer_manager", "trial.converted", "trial", t.ID, "formal_rent_sale_return")
	final, _ := s.deps.Trials.GetTrial(ctx, trialID)
	finalOrder, _ := s.deps.Orders.GetOrder(ctx, t.OrderID)
	return final, finalOrder, nil
}

// CheckDeadlines scans for trials past their acceptance deadline and escalates
// them. Returns the number of newly escalated trials.
func (s *TrialService) CheckDeadlines(ctx context.Context) (int, error) {
	now := s.deps.now()
	trials, err := s.deps.Trials.ListTrialsPastDeadline(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("check deadlines: %w", err)
	}
	escalated := 0
	for _, t := range trials {
		if t.Status == domain.TrialActive {
			if err := s.deps.Trials.UpdateTrialStatus(ctx, t.ID, domain.TrialActive, domain.TrialOverdue, t.Version); err != nil {
				if !errors.Is(err, errorsx.ErrVersionConflict) {
					logging.FromContext(ctx).Warn("mark overdue failed", zap.String("trial", t.ID), zap.Error(err))
				}
				continue
			}
			t, _ = s.deps.Trials.GetTrial(ctx, t.ID)
		}
		if t.Status != domain.TrialOverdue {
			continue
		}
		_, _, did, err := s.Escalate(ctx, t.ID)
		if err != nil {
			if !errors.Is(err, errorsx.ErrVersionConflict) {
				logging.FromContext(ctx).Warn("escalate failed", zap.String("trial", t.ID), zap.Error(err))
			}
			continue
		}
		if did {
			escalated++
		}
	}
	return escalated, nil
}

// ListOverdue returns overdue and escalated trials for the query endpoint.
func (s *TrialService) ListOverdue(ctx context.Context) ([]domain.Trial, error) {
	return s.deps.Trials.ListTrialsByStatus(ctx, domain.TrialOverdue, domain.TrialEscalated)
}
