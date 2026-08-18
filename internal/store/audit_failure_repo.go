package store

import (
	"context"
	"database/sql"
	"fmt"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// AppendAudit writes a single audit entry. Audit writes never fail the caller's
// transaction: they run on their own statement and swallow duplicate-key issues.
func (s *Store) AppendAudit(ctx context.Context, e domain.AuditEntry) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = s.clock.Now()
	}
	_, err := s.txFrom(ctx).ExecContext(ctx, `INSERT INTO audit_logs (actor_id, actor_role, action, entity_type, entity_id, detail, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, e.ActorID, e.ActorRole, e.Action, e.EntityType, e.EntityID, e.Detail, formatTime(e.OccurredAt))
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// ListAuditByFilter returns a page of audit entries matching the filter.
func (s *Store) ListAuditByFilter(ctx context.Context, f domain.AuditFilter, page domain.Page) ([]domain.AuditEntry, int64, error) {
	conds, args := auditConds(f)
	where := whereClause(conds)
	return paginated(ctx, s.txFrom(ctx),
		"SELECT COUNT(*) FROM audit_logs"+where,
		"SELECT id, actor_id, actor_role, action, entity_type, entity_id, detail, occurred_at FROM audit_logs"+where+" ORDER BY occurred_at DESC, id DESC",
		args, page, scanAuditEntry)
}

func scanAuditEntry(sc rowScanner) (domain.AuditEntry, error) {
	var e domain.AuditEntry
	var occurred sql.NullString
	if err := sc.Scan(&e.ID, &e.ActorID, &e.ActorRole, &e.Action, &e.EntityType, &e.EntityID, &e.Detail, &occurred); err != nil {
		return e, err
	}
	e.OccurredAt = parseTime(occurred)
	return e, nil
}

func auditConds(f domain.AuditFilter) ([]string, []any) {
	var conds []string
	var args []any
	if f.ActorID != "" {
		conds = append(conds, "actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.EntityType != "" {
		conds = append(conds, "entity_type = ?")
		args = append(args, f.EntityType)
	}
	if f.EntityID != "" {
		conds = append(conds, "entity_id = ?")
		args = append(args, f.EntityID)
	}
	if f.Action != "" {
		conds = append(conds, "action = ?")
		args = append(args, f.Action)
	}
	if !f.From.IsZero() {
		conds = append(conds, "occurred_at >= ?")
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		conds = append(conds, "occurred_at < ?")
		args = append(args, formatTime(f.To))
	}
	return conds, args
}

// RecordFailure upserts a dead-letter entry, incrementing the attempt counter.
func (s *Store) RecordFailure(ctx context.Context, entityType, entityID, taskType, lastErr string) error {
	id := fmt.Sprintf("%s:%s:%s", entityType, entityID, taskType)
	_, err := s.txFrom(ctx).ExecContext(ctx, `INSERT INTO permanent_failures (id, entity_type, entity_id, task_type, last_error, attempts, last_attempt_at, status, created_at)
VALUES (?, ?, ?, ?, ?, 1, ?, 'permanent', ?)
ON CONFLICT(entity_type, entity_id, task_type) DO UPDATE SET last_error = excluded.last_error, attempts = permanent_failures.attempts + 1, last_attempt_at = excluded.last_attempt_at, status = 'permanent'`,
		id, entityType, entityID, taskType, lastErr, s.nowRFC3339(), s.nowRFC3339())
	if err != nil {
		return fmt.Errorf("store: record failure: %w", err)
	}
	return nil
}

// ListFailures returns dead-letter entries, paginated.
func (s *Store) ListFailures(ctx context.Context, page domain.Page) ([]domain.PermanentFailure, int64, error) {
	return paginated(ctx, s.txFrom(ctx),
		"SELECT COUNT(*) FROM permanent_failures WHERE status = 'permanent'",
		`SELECT id, entity_type, entity_id, task_type, last_error, attempts, last_attempt_at, status, created_at
FROM permanent_failures WHERE status = 'permanent' ORDER BY last_attempt_at DESC`,
		nil, page, scanFailure)
}

func scanFailure(sc rowScanner) (domain.PermanentFailure, error) {
	var f domain.PermanentFailure
	var lastAttempt, created sql.NullString
	if err := sc.Scan(&f.ID, &f.EntityType, &f.EntityID, &f.TaskType, &f.LastError, &f.Attempts, &lastAttempt, &f.Status, &created); err != nil {
		return f, err
	}
	f.LastAttemptAt = parseTime(lastAttempt)
	f.CreatedAt = parseTime(created)
	return f, nil
}

// RequeueFailure clears the dead-letter status so the scheduler retries it.
func (s *Store) RequeueFailure(ctx context.Context, id string) error {
	res, err := s.txFrom(ctx).ExecContext(ctx, `UPDATE permanent_failures SET status = 'requeued', last_attempt_at = ? WHERE id = ? AND status = 'permanent'`,
		s.nowRFC3339(), id)
	if err != nil {
		return fmt.Errorf("store: requeue failure: %w", err)
	}
	if n, _ := rowsAffected(res); n == 0 {
		return fmt.Errorf("store: requeue failure %s: %w", id, errorsx.ErrNotFound)
	}
	return nil
}
