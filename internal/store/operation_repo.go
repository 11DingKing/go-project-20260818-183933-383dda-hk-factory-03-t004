package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"time"
)

func (s *Store) insertRecordRow(ctx context.Context, r *domain.OperationRecord, changeVersion int) error {
	now := s.nowRFC3339()
	r.Version = 1
	r.ChangeVersion = changeVersion
	r.CreatedAt = s.clock.Now()
	r.UpdatedAt = r.CreatedAt
	if r.Source == "" {
		r.Source = domain.SourceOnline
	}
	if r.Status == "" {
		r.Status = domain.RecordPending
	}
	res, err := s.txFrom(ctx).ExecContext(ctx, recordInsert, r.ID, r.OrderID, r.DeviceID, r.ResponsibleID,
		formatTime(r.OccurredAt), formatTime(r.RecordedAt), formatTimePtr(r.ReceivedAt), r.Source, r.ClientSequence,
		decimalText(r.Hours), r.Content, string(r.Status), r.ChangeVersion,
		sql.NullString{String: r.ConflictID, Valid: r.ConflictID != ""},
		sql.NullString{String: r.ManualID, Valid: r.ManualID != ""},
		sql.NullString{String: r.BatchID, Valid: r.BatchID != ""},
		r.Version, now, now)
	// Idempotency: a UNIQUE violation or zero-affected rows means the
	// (order_id, client_sequence) already exists; replay returns the stored row
	// instead of allocating a new change version.
	if err != nil {
		if !strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("store: insert record: %w", err)
		}
	} else if n, _ := rowsAffected(res); n != 0 {
		return nil
	}
	if existing, gerr := s.GetByOrderSeq(ctx, r.OrderID, r.ClientSequence); gerr == nil {
		*r = existing
		return nil
	}
	return fmt.Errorf("store: insert record: %w", errorsx.ErrAlreadyExists)
}

const recordInsert = `INSERT INTO operation_records
(id, order_id, device_id, responsible_id, occurred_at, recorded_at, received_at, source,
client_sequence, hours, content, status, change_version, conflict_id, manual_id, batch_id, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// InsertBatch inserts many records inside the caller's transaction, allocating a
// monotonic change version per record. Idempotent per (order_id, client_sequence).
func (s *Store) InsertBatch(ctx context.Context, rs []domain.OperationRecord) ([]domain.OperationRecord, error) {
	for i := range rs {
		// Skip allocation if the record already exists.
		existing, err := s.GetByOrderSeq(ctx, rs[i].OrderID, rs[i].ClientSequence)
		if err == nil {
			rs[i] = existing
			continue
		}
		v, err := s.nextChangeVersion(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.insertRecordRow(ctx, &rs[i], v); err != nil {
			return nil, err
		}
	}
	return rs, nil
}

// GetRecord loads an operation record by id.
func (s *Store) GetRecord(ctx context.Context, id string) (domain.OperationRecord, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+recordCols+` FROM operation_records WHERE id = ?`, scanRecord, "store: get record "+id, id)
}

const recordCols = `id, order_id, device_id, responsible_id, occurred_at, recorded_at, received_at, source,
client_sequence, hours, content, status, change_version, conflict_id, manual_id, batch_id, version, created_at, updated_at`

func scanRecord(sc rowScanner) (domain.OperationRecord, error) {
	var r domain.OperationRecord
	var status, source, occurred, recorded, received, hours, conflictID, manualID, batchID, created, updated sql.NullString
	if err := sc.Scan(&r.ID, &r.OrderID, &r.DeviceID, &r.ResponsibleID, &occurred, &recorded, &received, &source,
		&r.ClientSequence, &hours, &r.Content, &status, &r.ChangeVersion, &conflictID, &manualID, &batchID, &r.Version, &created, &updated); err != nil {
		return r, err
	}
	assignRecord(&r, status, source, occurred, recorded, received, hours, conflictID, manualID, batchID, created, updated)
	return r, nil
}

func assignRecord(r *domain.OperationRecord, status, source, occurred, recorded, received, hours, conflictID, manualID, batchID, created, updated sql.NullString) {
	r.Status = domain.RecordStatus(status.String)
	r.Source = source.String
	r.OccurredAt = parseTime(occurred)
	r.RecordedAt = parseTime(recorded)
	r.ReceivedAt = parseTimePtr(received)
	r.Hours = parseDecimal(hours)
	r.ConflictID = conflictID.String
	r.ManualID = manualID.String
	r.BatchID = batchID.String
	r.CreatedAt = parseTime(created)
	r.UpdatedAt = parseTime(updated)
}

// GetByOrderSeq loads a record by its order and client sequence.
func (s *Store) GetByOrderSeq(ctx context.Context, orderID string, seq int) (domain.OperationRecord, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+recordCols+` FROM operation_records WHERE order_id = ? AND client_sequence = ?`, scanRecord, fmt.Sprintf("store: get record %s/%d", orderID, seq), orderID, seq)
}

// UpdateRecordStatus transitions a record with optimistic locking.
func (s *Store) UpdateRecordStatus(ctx context.Context, id string, from, to domain.RecordStatus, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE operation_records SET status = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ? AND status = ?`,
		"store: update record status", string(to), s.nowRFC3339(), id, fromVersion, string(from))
}

// SetConflict marks a record as conflicting and links the work-hour id.
func (s *Store) SetConflict(ctx context.Context, id, conflictID string, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE operation_records SET status = ?, conflict_id = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: set conflict", string(domain.RecordConflict), conflictID, s.nowRFC3339(), id, fromVersion)
}

// SetManual routes a record to the manual-verification channel.
func (s *Store) SetManual(ctx context.Context, id, manualID string, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE operation_records SET status = ?, manual_id = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: set manual", string(domain.RecordManualVerifyNeeded), manualID, s.nowRFC3339(), id, fromVersion)
}

// CorrectHours updates a record's hours (staff correction) with optimistic locking.
func (s *Store) CorrectHours(ctx context.Context, id string, hours string, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE operation_records SET hours = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: correct hours", hours, s.nowRFC3339(), id, fromVersion)
}

// ListRecordsByFilter returns a page of records matching the filter plus a total.
func (s *Store) ListRecordsByFilter(ctx context.Context, f domain.RecordFilter, page domain.Page) ([]domain.OperationRecord, int64, error) {
	page = page.Normalize()
	var (
		conds []string
		args  []any
		join  string
	)
	if f.CustomerID != "" {
		join = " LEFT JOIN deployment_orders o ON o.id = r.order_id"
		conds = append(conds, "o.customer_id = ?")
		args = append(args, f.CustomerID)
	}
	if f.DeviceID != "" {
		conds = append(conds, "r.device_id = ?")
		args = append(args, f.DeviceID)
	}
	if f.OrderID != "" {
		conds = append(conds, "r.order_id = ?")
		args = append(args, f.OrderID)
	}
	if f.Status != "" {
		conds = append(conds, "r.status = ?")
		args = append(args, string(f.Status))
	}
	if f.Source != "" {
		conds = append(conds, "r.source = ?")
		args = append(args, f.Source)
	}
	if !f.From.IsZero() {
		conds = append(conds, "r.occurred_at >= ?")
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "r.occurred_at < ?")
		args = append(args, formatTime(f.To))
	}
	where := whereClause(conds)
	countSQL := "SELECT COUNT(*) FROM operation_records r" + join + where
	alias := "r." + strings.ReplaceAll(strings.ReplaceAll(recordCols, ", ", ", r."), ",\n", ",\nr.")
	listSQL := "SELECT " + alias + " FROM operation_records r" + join + where +
		" ORDER BY r.occurred_at DESC, r.client_sequence DESC"
	return paginated(ctx, s.txFrom(ctx), countSQL, listSQL, args, page, scanRecord)
}

// ListRecordChanges returns records with change_version greater than since, in
// ascending order, capped at limit. This is the incremental-pull cursor query.
func (s *Store) ListRecordChanges(ctx context.Context, sinceVersion, limit int) ([]domain.OperationRecord, error) {
	if limit <= 0 {
		limit = domain.DefaultPageSize
	}
	return queryRows(ctx, s.txFrom(ctx),
		`SELECT `+recordCols+` FROM operation_records WHERE change_version > ? ORDER BY change_version ASC LIMIT ?`,
		"store: list changes", scanRecord, sinceVersion, limit)
}

// SumAcceptedHours sums hours for accepted/verified/adjudicated records of an
// order occurring within [from, to). Returns canonical decimal text.
func (s *Store) SumAcceptedHours(ctx context.Context, orderID string, from, to time.Time) (string, error) {
	var sum sql.NullString
	q := `SELECT COALESCE(SUM(CAST(hours AS REAL)), 0) FROM operation_records
WHERE order_id = ? AND status IN (?, ?, ?) AND occurred_at >= ? AND occurred_at < ?`
	args := []any{orderID, string(domain.RecordAccepted), string(domain.RecordVerified), string(domain.RecordAdjudicated),
		formatTime(from), formatTime(to)}
	if err := s.txFrom(ctx).QueryRowContext(ctx, q, args...).Scan(&sum); err != nil {
		return "0", fmt.Errorf("store: sum accepted hours: %w", err)
	}
	if !sum.Valid || sum.String == "" {
		return "0", nil
	}
	return sum.String, nil
}

// ListRecordsByBatch returns all records belonging to a sync batch, ordered by
// the chronological replay order used at backfill time.
func (s *Store) ListRecordsByBatch(ctx context.Context, batchID string) ([]domain.OperationRecord, error) {
	return queryRows(ctx, s.txFrom(ctx),
		`SELECT `+recordCols+` FROM operation_records WHERE batch_id = ? ORDER BY occurred_at, client_sequence`,
		"store: list by batch", scanRecord, batchID)
}
