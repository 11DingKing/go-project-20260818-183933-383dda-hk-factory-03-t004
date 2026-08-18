package scheduler

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"sitesync/internal/service"
)

// Poller pulls pending and retry-due sync batches each tick, processes them with
// a lease, and schedules retries with exponential backoff or dead-letters them.
type Poller struct {
	deps   service.Deps
	sync   *service.SyncService
	cfg    config.SchedulerConfig
	clock  clock.Clock
	logger *zap.Logger
}

// tick runs one polling pass.
func (p *Poller) tick(ctx context.Context) {
	if _, err := p.sync.ReclaimExpiredLeases(ctx); err != nil {
		p.logger.Warn("reclaim expired leases failed", zap.Error(err))
	}
	owner := "poller"
	pending, _, err := p.deps.Batches.ListAccumulatedBatches(ctx, domain.SyncAccumulatedFilter{Status: domain.SyncBatchPending}, domain.Page{Size: domain.MaxPageSize})
	if err != nil {
		p.logger.Warn("list pending batches failed", zap.Error(err))
	} else {
		for _, b := range pending {
			p.processOne(ctx, b, owner)
		}
	}
	due, err := p.deps.Batches.ListBatchesRetryDue(ctx, p.clock.Now())
	if err != nil {
		p.logger.Warn("list retry-due batches failed", zap.Error(err))
	} else {
		for _, b := range due {
			p.processOne(ctx, b, owner)
		}
	}
}

// processOne processes a single batch and handles its retry or dead-letter fate.
func (p *Poller) processOne(ctx context.Context, batch domain.SyncBatch, owner string) {
	_, _, err := p.sync.ProcessBatch(ctx, batch.ID, owner)
	if err == nil {
		return
	}
	if errors.Is(err, errorsx.ErrLeaseHeld) {
		return
	}
	p.logger.Warn("batch processing failed", zap.String("batch", batch.ID), zap.Int("attempt", batch.RetryCount), zap.Error(err))
	nextAttempt := batch.RetryCount + 1
	if nextAttempt >= p.cfg.MaxRetries {
		if err := p.deps.Batches.MarkBatchPermanent(ctx, batch.ID, err.Error()); err != nil {
			p.logger.Warn("mark batch permanent failed", zap.Error(err))
		}
		if err := p.deps.Failures.RecordFailure(ctx, "sync_batch", batch.ID, "shift_pull", err.Error()); err != nil {
			p.logger.Warn("record failure failed", zap.Error(err))
		}
		_ = p.deps.Audit.AppendAudit(ctx, domain.AuditEntry{
			ActorID: "scheduler", ActorRole: "scheduler", Action: "batch.dead_lettered",
			EntityType: "sync_batch", EntityID: batch.ID, Detail: err.Error(), OccurredAt: p.clock.Now(),
		})
		return
	}
	nextRetry := p.nextRetry(batch.RetryCount)
	if err := p.deps.Batches.BumpBatchRetry(ctx, batch.ID, err.Error(), nextRetry); err != nil {
		p.logger.Warn("bump batch retry failed", zap.Error(err))
	}
}

// nextRetry computes an exponential backoff with jitter using the injected clock.
func (p *Poller) nextRetry(attempt int) time.Time {
	backoff := p.cfg.BaseBackoff << uint(attempt)
	if backoff > p.cfg.MaxBackoff {
		backoff = p.cfg.MaxBackoff
	}
	if backoff <= 0 {
		backoff = p.cfg.BaseBackoff
	}
	half := int64(backoff/2) + 1
	jitter := time.Duration(rand.Int63n(half))
	return p.clock.Now().Add(backoff + jitter)
}
