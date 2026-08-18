package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

const batchCols = `id, order_id, lease_owner, lease_until, status, record_count, processed_count, last_error, retry_count, next_retry_at, version, created_at, updated_at`

// CreateSyncBatch inserts a pending batch of accumulated offline records.
func (s *Store) CreateSyncBatch(ctx context.Context, b domain.SyncBatch) (domain.SyncBatch, error) {
	b.Version = 1
	b.CreatedAt = s.clock.Now()
	b.UpdatedAt = b.CreatedAt
	if b.Status == "" {
		b.Status = domain.SyncBatchPending
	}
	now := s.nowRFC3339()
	res, err := s.txFrom(ctx).ExecContext(ctx, batchInsert, b.ID, b.OrderID, b.LeaseOwner,
		formatTimePtr(b.LeaseUntil), string(b.Status), b.RecordCount, b.ProcessedCount,
		sql.NullString{String: b.LastError, Valid: b.LastError != ""},
		b.RetryCount, formatTimePtr(b.NextRetryAt), b.Version, now, now)
	return b, dupInsert(res, err, "sync batch", b.ID)
}

const batchInsert = `INSERT INTO sync_batches (id, order_id, lease_owner, lease_until, status, record_count, processed_count, last_error, retry_count, next_retry_at, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func scanBatch(sc rowScanner) (domain.SyncBatch, error) {
	var b domain.SyncBatch
	var status, leaseUntil, lastErr, nextRetry, created, updated sql.NullString
	if err := sc.Scan(&b.ID, &b.OrderID, &b.LeaseOwner, &leaseUntil, &status, &b.RecordCount, &b.ProcessedCount,
		&lastErr, &b.RetryCount, &nextRetry, &b.Version, &created, &updated); err != nil {
		return b, err
	}
	b.Status = domain.SyncBatchStatus(status.String)
	b.LeaseUntil = parseTimePtr(leaseUntil)
	b.LastError = lastErr.String
	b.NextRetryAt = parseTimePtr(nextRetry)
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return b, nil
}

// GetSyncBatch loads a batch by id.
func (s *Store) GetSyncBatch(ctx context.Context, id string) (domain.SyncBatch, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+batchCols+` FROM sync_batches WHERE id = ?`, scanBatch, "store: get sync batch "+id, id)
}

// AcquireLease atomically claims a pending batch for a worker. Returns the
// updated batch or ErrLeaseHeld when another worker already owns it.
func (s *Store) AcquireLease(ctx context.Context, id, owner string, leaseUntil time.Time) (domain.SyncBatch, error) {
	res, err := s.txFrom(ctx).ExecContext(ctx, `UPDATE sync_batches SET lease_owner = ?, lease_until = ?, status = ?, updated_at = ?, version = version + 1
WHERE id = ? AND status = ?`,
		owner, formatTime(leaseUntil), string(domain.SyncBatchLeasing), s.nowRFC3339(), id, string(domain.SyncBatchPending))
	if err != nil {
		return domain.SyncBatch{}, fmt.Errorf("store: acquire lease: %w", err)
	}
	n, _ := rowsAffected(res)
	if n == 0 {
		return domain.SyncBatch{}, fmt.Errorf("store: acquire lease: %w", errorsx.ErrLeaseHeld)
	}
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+batchCols+` FROM sync_batches WHERE id = ?`, scanBatch, "store: get sync batch "+id, id)
}

// ReleaseLease returns a batch to pending without processing (worker abort).
func (s *Store) ReleaseLease(ctx context.Context, id string) (domain.SyncBatch, error) {
	res, err := s.txFrom(ctx).ExecContext(ctx, `UPDATE sync_batches SET lease_owner = '', lease_until = NULL, status = ?, updated_at = ?, version = version + 1 WHERE id = ? AND status = ?`,
		string(domain.SyncBatchPending), s.nowRFC3339(), id, string(domain.SyncBatchLeasing))
	if err != nil {
		return domain.SyncBatch{}, fmt.Errorf("store: release lease: %w", err)
	}
	if n, _ := rowsAffected(res); n == 0 {
		return domain.SyncBatch{}, fmt.Errorf("store: release lease: %w", errorsx.ErrLeaseHeld)
	}
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+batchCols+` FROM sync_batches WHERE id = ?`, scanBatch, "store: get sync batch "+id, id)
}

// ReclaimExpired resets leasing batches whose lease has expired back to pending.
func (s *Store) ReclaimExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.txFrom(ctx).ExecContext(ctx, `UPDATE sync_batches SET lease_owner = '', lease_until = NULL, status = ?, updated_at = ?, version = version + 1
WHERE status = ? AND lease_until IS NOT NULL AND lease_until < ?`,
		string(domain.SyncBatchPending), s.nowRFC3339(), string(domain.SyncBatchLeasing), formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("store: reclaim expired: %w", err)
	}
	return rowsAffected(res)
}

// UpdateBatchProgress records processed count and status with optimistic locking.
func (s *Store) UpdateBatchProgress(ctx context.Context, id string, processed int, lastErr string, status domain.SyncBatchStatus, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE sync_batches SET processed_count = ?, last_error = ?, status = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: update batch progress", processed, sql.NullString{String: lastErr, Valid: lastErr != ""}, string(status), s.nowRFC3339(), id, fromVersion)
}

// BumpBatchRetry schedules the next retry attempt with the given backoff.
func (s *Store) BumpBatchRetry(ctx context.Context, id string, lastErr string, nextRetry time.Time) error {
	_, err := s.txFrom(ctx).ExecContext(ctx, `UPDATE sync_batches SET status = ?, last_error = ?, retry_count = retry_count + 1, next_retry_at = ?, updated_at = ?, version = version + 1
WHERE id = ? AND status IN (?, ?, ?)`,
		string(domain.SyncBatchFailed), sql.NullString{String: lastErr, Valid: lastErr != ""},
		formatTime(nextRetry), s.nowRFC3339(), id,
		string(domain.SyncBatchProcessing), string(domain.SyncBatchFailed), string(domain.SyncBatchLeasing))
	if err != nil {
		return fmt.Errorf("store: bump batch retry: %w", err)
	}
	return nil
}

// MarkBatchPermanent marks a batch as permanently failed (dead letter).
func (s *Store) MarkBatchPermanent(ctx context.Context, id string, lastErr string) error {
	return s.execVersioned(ctx, `UPDATE sync_batches SET status = ?, last_error = ?, next_retry_at = NULL, updated_at = ?, version = version + 1 WHERE id = ?`,
		"store: mark batch permanent", string(domain.SyncBatchPermanent), lastErr, s.nowRFC3339(), id)
}

// ListAccumulatedBatches returns batches not yet completed, paginated.
func (s *Store) ListAccumulatedBatches(ctx context.Context, f domain.SyncAccumulatedFilter, page domain.Page) ([]domain.SyncBatch, int64, error) {
	conds, args := batchConds(f)
	where := whereClause(conds)
	return paginated(ctx, s.txFrom(ctx),
		"SELECT COUNT(*) FROM sync_batches"+where,
		"SELECT "+batchCols+" FROM sync_batches"+where+" ORDER BY created_at DESC",
		args, page, scanBatch)
}

func batchConds(f domain.SyncAccumulatedFilter) ([]string, []any) {
	conds := []string{"status != ?"}
	args := []any{string(domain.SyncBatchCompleted)}
	if f.OrderID != "" {
		conds = append(conds, "order_id = ?")
		args = append(args, f.OrderID)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, string(f.Status))
	}
	return conds, args
}

// ListBatchesRetryDue returns failed batches whose next retry time has arrived.
func (s *Store) ListBatchesRetryDue(ctx context.Context, now time.Time) ([]domain.SyncBatch, error) {
	return queryRows(ctx, s.txFrom(ctx), `SELECT `+batchCols+` FROM sync_batches
WHERE status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= ? ORDER BY next_retry_at`,
		"store: list retry due", scanBatch, string(domain.SyncBatchFailed), formatTime(now))
}
