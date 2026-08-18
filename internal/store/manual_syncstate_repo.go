package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sitesync/internal/domain"
)

const manualCols = `id, record_id, order_id, reason, status, reviewer_id, reviewed_at, decision, note, version, created_at, updated_at`

const manualInsert = `INSERT INTO manual_verifications (id, record_id, order_id, reason, status, reviewer_id, reviewed_at, decision, note, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// CreateManualVerification inserts a human-review record.
func (s *Store) CreateManualVerification(ctx context.Context, m domain.ManualVerification) (domain.ManualVerification, error) {
	m.Version = 1
	m.CreatedAt = s.clock.Now()
	m.UpdatedAt = m.CreatedAt
	if m.Status == "" {
		m.Status = domain.ManualPending
	}
	now := s.nowRFC3339()
	res, err := s.txFrom(ctx).ExecContext(ctx, manualInsert, m.ID, m.RecordID, m.OrderID, m.Reason, m.Status,
		sql.NullString{String: m.ReviewerID, Valid: m.ReviewerID != ""},
		formatTimePtr(m.ReviewedAt),
		sql.NullString{String: m.Decision, Valid: m.Decision != ""},
		sql.NullString{String: m.Note, Valid: m.Note != ""},
		m.Version, now, now)
	return m, dupInsert(res, err, "manual", m.RecordID)
}

func scanManual(sc rowScanner) (domain.ManualVerification, error) {
	var m domain.ManualVerification
	var status, reviewer, reviewed, decision, note, created, updated sql.NullString
	if err := sc.Scan(&m.ID, &m.RecordID, &m.OrderID, &m.Reason, &status, &reviewer, &reviewed, &decision, &note, &m.Version, &created, &updated); err != nil {
		return m, err
	}
	m.Status = status.String
	m.ReviewerID = reviewer.String
	m.ReviewedAt = parseTimePtr(reviewed)
	m.Decision = decision.String
	m.Note = note.String
	m.CreatedAt = parseTime(created)
	m.UpdatedAt = parseTime(updated)
	return m, nil
}

// GetManualByRecord loads a manual verification by record id.
func (s *Store) GetManualByRecord(ctx context.Context, recordID string) (domain.ManualVerification, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+manualCols+` FROM manual_verifications WHERE record_id = ?`, scanManual, "store: get manual by record "+recordID, recordID)
}

// ReviewManual records a reviewer's decision with optimistic locking and
// returns the updated row.
func (s *Store) ReviewManual(ctx context.Context, id, reviewerID string, at time.Time, decision, note string, fromVersion int) (domain.ManualVerification, error) {
	if err := s.execVersioned(ctx, `UPDATE manual_verifications SET status = ?, reviewer_id = ?, reviewed_at = ?, decision = ?, note = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: review manual", domain.ManualReviewed, reviewerID, formatTime(at), decision, note, s.nowRFC3339(), id, fromVersion); err != nil {
		return domain.ManualVerification{}, err
	}
	m, err := scanManual(s.txFrom(ctx).QueryRowContext(ctx, `SELECT `+manualCols+` FROM manual_verifications WHERE id = ?`, id))
	if err != nil {
		return m, errNoRows(err, "store: get manual "+id)
	}
	return m, nil
}

// ListManualPending returns pending verifications, paginated.
func (s *Store) ListManualPending(ctx context.Context, page domain.Page) ([]domain.ManualVerification, int64, error) {
	return paginated(ctx, s.txFrom(ctx),
		"SELECT COUNT(*) FROM manual_verifications WHERE status = ?",
		"SELECT "+manualCols+" FROM manual_verifications WHERE status = ? ORDER BY created_at",
		[]any{domain.ManualPending}, page, scanManual)
}

// UpsertBackfill records a successful backfill and the latest change version.
func (s *Store) UpsertBackfill(ctx context.Context, orderID string, at time.Time, changeVersion int) error {
	_, err := s.txFrom(ctx).ExecContext(ctx, `INSERT INTO sync_state (order_id, last_change_version, last_pulled_at, last_backfill_at, updated_at)
VALUES (?, ?, NULL, ?, ?)
ON CONFLICT(order_id) DO UPDATE SET last_change_version = MAX(excluded.last_change_version, sync_state.last_change_version), last_backfill_at = excluded.last_backfill_at, updated_at = excluded.updated_at`,
		orderID, changeVersion, formatTime(at), s.nowRFC3339())
	if err != nil {
		return fmt.Errorf("store: upsert backfill state: %w", err)
	}
	return nil
}
