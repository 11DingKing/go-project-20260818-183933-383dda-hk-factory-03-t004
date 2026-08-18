package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"sitesync/internal/domain"
)

const trialCols = `id, order_id, effective_from, effective_to, acceptance_deadline, status, accepted_at, accepted_by, version, created_at, updated_at`

const inspectionCols = `id, order_id, device_id, round, type, scheduled_at, completed_at, status, assignee_id, version, created_at, updated_at`

// CreateTrial inserts a trial period. One trial per order is enforced by the
// unique constraint on order_id.
func (s *Store) CreateTrial(ctx context.Context, t domain.Trial) (domain.Trial, error) {
	now := s.nowRFC3339()
	t.Version = 1
	t.CreatedAt = s.clock.Now()
	t.UpdatedAt = t.CreatedAt
	if t.Status == "" {
		t.Status = domain.TrialPending
	}
	res, err := s.txFrom(ctx).ExecContext(ctx, trialInsert, t.ID, t.OrderID,
		formatTime(t.EffectiveFrom), formatTime(t.EffectiveTo), formatTime(t.AcceptanceDeadline),
		string(t.Status), t.Version, now, now)
	return t, dupInsert(res, err, "trial", t.OrderID)
}

const trialInsert = `INSERT INTO trial_periods (id, order_id, effective_from, effective_to, acceptance_deadline, status, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

func scanTrial(sc rowScanner) (domain.Trial, error) {
	var t domain.Trial
	var status, accAt, accBy, effFrom, effTo, deadline, created, updated sql.NullString
	if err := sc.Scan(&t.ID, &t.OrderID, &effFrom, &effTo, &deadline, &status, &accAt, &accBy, &t.Version, &created, &updated); err != nil {
		return t, err
	}
	t.Status = domain.TrialStatus(status.String)
	t.EffectiveFrom = parseTime(effFrom)
	t.EffectiveTo = parseTime(effTo)
	t.AcceptanceDeadline = parseTime(deadline)
	t.AcceptedAt = parseTimePtr(accAt)
	t.AcceptedBy = accBy.String
	t.CreatedAt = parseTime(created)
	t.UpdatedAt = parseTime(updated)
	return t, nil
}

// GetTrial loads a trial by id.
func (s *Store) GetTrial(ctx context.Context, id string) (domain.Trial, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+trialCols+` FROM trial_periods WHERE id = ?`, scanTrial, "store: get trial "+id, id)
}

// GetTrialByOrder loads the trial for an order.
func (s *Store) GetTrialByOrder(ctx context.Context, orderID string) (domain.Trial, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+trialCols+` FROM trial_periods WHERE order_id = ?`, scanTrial, "store: get trial by order "+orderID, orderID)
}

// UpdateTrialStatus transitions a trial with optimistic locking.
func (s *Store) UpdateTrialStatus(ctx context.Context, id string, from, to domain.TrialStatus, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE trial_periods SET status = ?, updated_at = ?, version = version + 1 WHERE id = ? AND status = ? AND version = ?`,
		"store: update trial status", string(to), s.nowRFC3339(), id, string(from), fromVersion)
}

// SetAccepted records acceptance with optimistic locking.
func (s *Store) SetAccepted(ctx context.Context, id, by string, at time.Time, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE trial_periods SET status = ?, accepted_at = ?, accepted_by = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ? AND status IN (?, ?)`,
		"store: set accepted", string(domain.TrialAccepted), formatTime(at), by, s.nowRFC3339(), id, fromVersion,
		string(domain.TrialActive), string(domain.TrialOverdue))
}

// ListTrialsPastDeadline returns active/overdue trials whose deadline has passed.
func (s *Store) ListTrialsPastDeadline(ctx context.Context, now time.Time) ([]domain.Trial, error) {
	return queryRows(ctx, s.txFrom(ctx), `SELECT `+trialCols+` FROM trial_periods WHERE acceptance_deadline < ? AND status IN (?, ?) ORDER BY acceptance_deadline`,
		"store: list past deadline", scanTrial, formatTime(now), string(domain.TrialActive), string(domain.TrialOverdue))
}

// ListTrialsByStatus returns trials in any of the given statuses.
func (s *Store) ListTrialsByStatus(ctx context.Context, statuses ...domain.TrialStatus) ([]domain.Trial, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, st := range statuses {
		placeholders[i] = "?"
		args[i] = string(st)
	}
	q := `SELECT ` + trialCols + ` FROM trial_periods WHERE status IN (` + strings.Join(placeholders, ",") + `) ORDER BY acceptance_deadline`
	return queryRows(ctx, s.txFrom(ctx), q, "store: list trials by status", scanTrial, args...)
}

// CreateInspection inserts a dispatched inspection. One first-round inspection
// per order is enforced by the service checking GetByOrderRound.
func (s *Store) CreateInspection(ctx context.Context, ins domain.Inspection) (domain.Inspection, error) {
	now := s.nowRFC3339()
	ins.Version = 1
	ins.CreatedAt = s.clock.Now()
	ins.UpdatedAt = ins.CreatedAt
	if ins.Status == "" {
		ins.Status = domain.InspectionDispatched
	}
	res, err := s.txFrom(ctx).ExecContext(ctx, inspectionInsert, ins.ID, ins.OrderID,
		sql.NullString{String: ins.DeviceID, Valid: ins.DeviceID != ""},
		ins.Round, ins.Type, formatTime(ins.ScheduledAt), sql.NullString{}, string(ins.Status), ins.AssigneeID, ins.Version, now, now)
	return ins, dupInsert(res, err, "inspection", ins.ID)
}

const inspectionInsert = `INSERT INTO inspections (id, order_id, device_id, round, type, scheduled_at, completed_at, status, assignee_id, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func scanInspection(sc rowScanner) (domain.Inspection, error) {
	var ins domain.Inspection
	var status, devID, sched, comp, created, updated sql.NullString
	if err := sc.Scan(&ins.ID, &ins.OrderID, &devID, &ins.Round, &ins.Type, &sched, &comp, &status, &ins.AssigneeID, &ins.Version, &created, &updated); err != nil {
		return ins, err
	}
	ins.DeviceID = devID.String
	ins.Status = domain.InspectionStatus(status.String)
	ins.ScheduledAt = parseTime(sched)
	ins.CompletedAt = parseTimePtr(comp)
	ins.CreatedAt = parseTime(created)
	ins.UpdatedAt = parseTime(updated)
	return ins, nil
}

// GetInspectionByOrderRound loads an inspection by order and round.
func (s *Store) GetInspectionByOrderRound(ctx context.Context, orderID string, round int) (domain.Inspection, error) {
	ins, err := scanInspection(s.txFrom(ctx).QueryRowContext(ctx, `SELECT `+inspectionCols+` FROM inspections WHERE order_id = ? AND round = ?`, orderID, round))
	if err != nil {
		return ins, errNoRows(err, fmt.Sprintf("store: get inspection %s/%d", orderID, round))
	}
	return ins, nil
}

// ListInspectionsByOrder returns inspections for an order.
func (s *Store) ListInspectionsByOrder(ctx context.Context, orderID string) ([]domain.Inspection, error) {
	rs, err := s.txFrom(ctx).QueryContext(ctx, `SELECT `+inspectionCols+` FROM inspections WHERE order_id = ? ORDER BY round`, orderID)
	if err != nil {
		return nil, fmt.Errorf("store: list inspections: %w", err)
	}
	defer rs.Close()
	var rows []domain.Inspection
	for rs.Next() {
		ins, err := scanInspection(rs)
		if err != nil {
			return nil, fmt.Errorf("store: scan inspection: %w", err)
		}
		rows = append(rows, ins)
	}
	return rows, rs.Err()
}

// CancelInspectionsByOrder cancels every dispatched inspection for an order.
func (s *Store) CancelInspectionsByOrder(ctx context.Context, orderID string) (int64, error) {
	res, err := s.txFrom(ctx).ExecContext(ctx, `UPDATE inspections SET status = ?, completed_at = ?, updated_at = ?, version = version + 1 WHERE order_id = ? AND status = ?`,
		string(domain.InspectionCancelled), s.nowRFC3339(), s.nowRFC3339(), orderID, string(domain.InspectionDispatched))
	if err != nil {
		return 0, fmt.Errorf("store: cancel inspections: %w", err)
	}
	return rowsAffected(res)
}
