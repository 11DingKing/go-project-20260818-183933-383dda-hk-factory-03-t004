package store

import (
	"context"
	"database/sql"
	"fmt"

	"sitesync/internal/domain"
)

const billCols = `id, order_id, period_no, period_start, period_end, total_hours, rate, amount, status, generated_by, version, created_at, updated_at`

// CreateBill inserts a reconciliation bill.
func (s *Store) CreateBill(ctx context.Context, b domain.ReconciliationBill) (domain.ReconciliationBill, error) {
	b.Version = 1
	b.CreatedAt = s.clock.Now()
	b.UpdatedAt = b.CreatedAt
	if b.Status == "" {
		b.Status = domain.BillDraft
	}
	now := s.nowRFC3339()
	res, err := s.txFrom(ctx).ExecContext(ctx, billInsert, b.ID, b.OrderID, b.PeriodNo,
		formatTime(b.PeriodStart), formatTime(b.PeriodEnd), decimalText(b.TotalHours), decimalText(b.Rate),
		decimalText(b.Amount), string(b.Status), b.GeneratedBy, b.Version, now, now)
	return b, dupInsert(res, err, "bill", fmt.Sprintf("%s/%d", b.OrderID, b.PeriodNo))
}

const billInsert = `INSERT INTO reconciliation_bills (id, order_id, period_no, period_start, period_end, total_hours, rate, amount, status, generated_by, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func scanBill(sc rowScanner) (domain.ReconciliationBill, error) {
	var b domain.ReconciliationBill
	var status, start, end, total, rate, amount, created, updated sql.NullString
	if err := sc.Scan(&b.ID, &b.OrderID, &b.PeriodNo, &start, &end, &total, &rate, &amount, &status, &b.GeneratedBy, &b.Version, &created, &updated); err != nil {
		return b, err
	}
	b.Status = domain.BillStatus(status.String)
	b.PeriodStart = parseTime(start)
	b.PeriodEnd = parseTime(end)
	b.TotalHours = parseDecimal(total)
	b.Rate = parseDecimal(rate)
	b.Amount = parseDecimal(amount)
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return b, nil
}

// GetBillByOrderPeriod loads a bill by order and period number.
func (s *Store) GetBillByOrderPeriod(ctx context.Context, orderID string, periodNo int) (domain.ReconciliationBill, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+billCols+` FROM reconciliation_bills WHERE order_id = ? AND period_no = ?`, scanBill, fmt.Sprintf("store: get bill %s/%d", orderID, periodNo), orderID, periodNo)
}

// GetBillByID loads a bill by its primary key.
func (s *Store) GetBillByID(ctx context.Context, id string) (domain.ReconciliationBill, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+billCols+` FROM reconciliation_bills WHERE id = ?`, scanBill, "store: get bill by id "+id, id)
}

// UpdateBillStatus transitions a bill with optimistic locking.
func (s *Store) UpdateBillStatus(ctx context.Context, id string, from, to domain.BillStatus, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE reconciliation_bills SET status = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ? AND status = ?`,
		"store: update bill status", string(to), s.nowRFC3339(), id, fromVersion, string(from))
}

// UpdateBillTotals recalculates a bill's totals with optimistic locking.
func (s *Store) UpdateBillTotals(ctx context.Context, id string, totalHours, rate, amount string, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE reconciliation_bills SET total_hours = ?, rate = ?, amount = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: update bill totals", totalHours, rate, amount, s.nowRFC3339(), id, fromVersion)
}
