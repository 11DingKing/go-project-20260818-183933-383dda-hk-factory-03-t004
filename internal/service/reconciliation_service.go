package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"sitesync/internal/reconcile"
)

// ReconciliationService settles periods, corrects misregistrations and exports
// reconciliation data for finance.
type ReconciliationService struct {
	deps Deps
}

// GenerateBill recomputes a bill's totals from accepted records in its period.
func (s *ReconciliationService) GenerateBill(ctx context.Context, orderID string, periodNo int, rate decimal.Decimal, actor string) (domain.ReconciliationBill, error) {
	if periodNo <= 0 {
		periodNo = 1
	}
	bill, err := s.deps.Bills.GetBillByOrderPeriod(ctx, orderID, periodNo)
	if err != nil {
		if !errors.Is(err, errorsx.ErrNotFound) {
			return domain.ReconciliationBill{}, fmt.Errorf("generate bill: %w", err)
		}
		now := s.deps.now()
		bill = domain.ReconciliationBill{
			ID: orderID + "-bill-" + fmt.Sprint(periodNo), OrderID: orderID, PeriodNo: periodNo,
			PeriodStart: now, PeriodEnd: now.Add(30 * 24 * time.Hour), Status: domain.BillDraft, GeneratedBy: actor,
		}
		created, cerr := s.deps.Bills.CreateBill(ctx, bill)
		if cerr != nil {
			return domain.ReconciliationBill{}, cerr
		}
		bill = created
	}
	if rate.IsZero() {
		rate = decimal.NewFromInt(100)
	}
	totalStr, err := s.deps.Records.SumAcceptedHours(ctx, orderID, bill.PeriodStart, bill.PeriodEnd)
	if err != nil {
		return bill, err
	}
	total, _ := decimal.NewFromString(totalStr)
	amount := total.Mul(rate)
	if err := s.deps.Bills.UpdateBillTotals(ctx, bill.ID, total.String(), rate.String(), amount.String(), bill.Version); err != nil {
		return bill, err
	}
	s.deps.audit(ctx, actor, "finance", "bill.generated", "reconciliation_bill", bill.ID,
		fmt.Sprintf("hours=%s amount=%s", total.String(), amount.String()))
	final, _ := s.deps.Bills.GetBillByOrderPeriod(ctx, orderID, periodNo)
	return final, nil
}

// IssueBill moves a draft bill to issued. Idempotent on already-issued bills and
// rejects any transition the bill state machine forbids.
func (s *ReconciliationService) IssueBill(ctx context.Context, billID string, actor string) (domain.ReconciliationBill, error) {
	bill, err := s.loadBillByID(ctx, billID)
	if err != nil {
		return domain.ReconciliationBill{}, err
	}
	if bill.Status == domain.BillIssued {
		return bill, nil
	}
	if err := domain.AssertBillTransition(bill.Status, domain.BillIssued); err != nil {
		return bill, err
	}
	if err := s.deps.Bills.UpdateBillStatus(ctx, bill.ID, bill.Status, domain.BillIssued, bill.Version); err != nil {
		return bill, err
	}
	s.deps.audit(ctx, actor, "finance", "bill.issued", "reconciliation_bill", bill.ID, "")
	final, _ := s.deps.Bills.GetBillByOrderPeriod(ctx, bill.OrderID, bill.PeriodNo)
	return final, nil
}

func (s *ReconciliationService) loadBillByID(ctx context.Context, billID string) (domain.ReconciliationBill, error) {
	bill, err := s.deps.Bills.GetBillByID(ctx, billID)
	if err != nil {
		return domain.ReconciliationBill{}, fmt.Errorf("bill %s: %w", billID, err)
	}
	return bill, nil
}

// CorrectRecord adjusts a record's hours (staff correction). An accepted record
// moves to verified to mark it has been reviewed.
func (s *ReconciliationService) CorrectRecord(ctx context.Context, recordID string, newHours decimal.Decimal, actor string) (domain.OperationRecord, error) {
	r, err := s.deps.Records.GetRecord(ctx, recordID)
	if err != nil {
		return domain.OperationRecord{}, notFound("record", recordID)
	}
	if r.Status == domain.RecordRevoked {
		return r, fmt.Errorf("%w: cannot correct a revoked record", errorsx.ErrIllegalTransition)
	}
	priorVersion := r.Version
	err = s.deps.UOW.InTx(ctx, func(ctx context.Context) error {
		if err := s.deps.Records.CorrectHours(ctx, r.ID, newHours.String(), priorVersion); err != nil {
			return err
		}
		if r.Status == domain.RecordAccepted {
			cur, err := s.deps.Records.GetRecord(ctx, r.ID)
			if err != nil {
				return err
			}
			if err := s.deps.Records.UpdateRecordStatus(ctx, r.ID, domain.RecordAccepted, domain.RecordVerified, cur.Version); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	s.deps.audit(ctx, actor, "ops_specialist", "record.corrected", "operation_record", recordID,
		fmt.Sprintf("hours=%s", newHours.String()))
	final, _ := s.deps.Records.GetRecord(ctx, recordID)
	return final, nil
}

// RevokeRecord revokes a misregistered record. Idempotent on revoked records.
func (s *ReconciliationService) RevokeRecord(ctx context.Context, recordID, reason, actor string) (domain.OperationRecord, error) {
	r, err := s.deps.Records.GetRecord(ctx, recordID)
	if err != nil {
		return domain.OperationRecord{}, notFound("record", recordID)
	}
	if r.Status == domain.RecordRevoked {
		return r, nil
	}
	if err := domain.AssertRecordTransition(r.Status, domain.RecordRevoked); err != nil {
		return r, err
	}
	if err := s.deps.Records.UpdateRecordStatus(ctx, r.ID, r.Status, domain.RecordRevoked, r.Version); err != nil {
		return r, err
	}
	s.deps.audit(ctx, actor, "ops_specialist", "record.revoked", "operation_record", recordID, reason)
	final, _ := s.deps.Records.GetRecord(ctx, recordID)
	return final, nil
}

// ExportRow is one line of a reconciliation export.
type ExportRow struct {
	OrderID     string `json:"order_id"`
	RecordID    string `json:"record_id"`
	DeviceID    string `json:"device_id"`
	OccurredAt  string `json:"occurred_at"`
	Source      string `json:"source"`
	Status      string `json:"status"`
	Hours       string `json:"hours"`
	Responsible string `json:"responsible_id"`
}

// Export returns reconciliation rows for finance to review.
func (s *ReconciliationService) Export(ctx context.Context, filter domain.RecordFilter, page domain.Page) ([]ExportRow, int64, error) {
	records, total, err := s.deps.Records.ListRecordsByFilter(ctx, filter, page)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]ExportRow, 0, len(records))
	for _, r := range records {
		rows = append(rows, ExportRow{
			OrderID: r.OrderID, RecordID: r.ID, DeviceID: r.DeviceID,
			OccurredAt: r.OccurredAt.UTC().Format(time.RFC3339), Source: r.Source,
			Status: string(r.Status), Hours: r.Hours.String(), Responsible: r.ResponsibleID,
		})
	}
	return rows, total, nil
}

// ComputeDiff compares the system's settled operation records against the
// customer-reported work hours for an order over a period and returns a
// structured reconciliation diff. Only reconcilable records (accepted, verified
// or adjudicated) count toward the system side; pending, conflicted,
// manual-verify and revoked records are excluded because they are not yet
// settled. Customer work-hour rows outside the period are ignored. The diff
// surfaces over- and under-reports as conflicts requiring adjudication.
func (s *ReconciliationService) ComputeDiff(ctx context.Context, orderID string, from, to time.Time) (reconcile.Diff, error) {
	f := domain.RecordFilter{OrderID: orderID, From: from, To: to}
	var system []reconcile.Input
	scan := domain.Page{Size: domain.MaxPageSize}
	for {
		items, total, err := s.deps.Records.ListRecordsByFilter(ctx, f, scan)
		if err != nil {
			return reconcile.Diff{}, fmt.Errorf("reconcile: list records: %w", err)
		}
		for _, r := range items {
			if !isReconcilable(r.Status) {
				continue
			}
			system = append(system, reconcile.Input{
				DeviceID: r.DeviceID, Date: r.OccurredAt.UTC().Format("2006-01-02"), Hours: r.Hours,
			})
		}
		if len(items) < scan.Size || int64(scan.Offset+len(items)) >= total {
			break
		}
		scan.Offset += len(items)
	}
	devices, err := s.deps.OrderDevices.ListDevicesByOrder(ctx, orderID)
	if err != nil {
		return reconcile.Diff{}, fmt.Errorf("reconcile: list devices: %w", err)
	}
	fromStr, toStr := "", ""
	if !from.IsZero() {
		fromStr = from.UTC().Format("2006-01-02")
	}
	if !to.IsZero() {
		toStr = to.UTC().Format("2006-01-02")
	}
	var customer []reconcile.Input
	for _, dd := range devices {
		whs, err := s.deps.WorkHours.ListWorkHoursByDevice(ctx, dd.DeviceID)
		if err != nil {
			return reconcile.Diff{}, fmt.Errorf("reconcile: list work hours %s: %w", dd.DeviceID, err)
		}
		for _, w := range whs {
			if fromStr != "" && w.WorkDate < fromStr {
				continue
			}
			if toStr != "" && w.WorkDate > toStr {
				continue
			}
			customer = append(customer, reconcile.Input{DeviceID: w.DeviceID, Date: w.WorkDate, Hours: w.Hours})
		}
	}
	return reconcile.Engine{}.Diff(system, customer), nil
}

// isReconcilable reports whether a record's status counts toward the system
// side of a reconciliation diff: only settled records participate.
func isReconcilable(s domain.RecordStatus) bool {
	switch s {
	case domain.RecordAccepted, domain.RecordVerified, domain.RecordAdjudicated:
		return true
	}
	return false
}
