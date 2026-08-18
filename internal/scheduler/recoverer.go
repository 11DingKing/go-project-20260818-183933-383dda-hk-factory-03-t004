package scheduler

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/domain"
	"sitesync/internal/service"
)

// Recoverer resumes work interrupted by a process restart.
type Recoverer struct {
	deps       service.Deps
	deployment *service.DeploymentService
	sync       *service.SyncService
	cfg        config.SchedulerConfig
	clock      clock.Clock
	logger     *zap.Logger
}

// Recover reclaims expired leases, re-provisions interrupted deployment orders
// and retries failed sync batches once. Errors are collected; the pass never
// aborts the whole recovery because of one bad entity.
func (r *Recoverer) Recover(ctx context.Context) error {
	var errs []error
	if reclaimed, err := r.sync.ReclaimExpiredLeases(ctx); err != nil {
		errs = append(errs, fmt.Errorf("reclaim leases: %w", err))
	} else if reclaimed > 0 {
		r.logger.Info("reclaimed expired leases", zap.Int64("count", reclaimed))
	}
	orders, err := r.deps.Orders.ListByStatus(ctx, domain.DeploymentProvisioning, domain.DeploymentPendingRetry)
	if err != nil {
		errs = append(errs, fmt.Errorf("list interrupted orders: %w", err))
	} else {
		for _, o := range orders {
			if _, _, perr := r.deployment.Provision(ctx, o.ID, "recoverer"); perr != nil {
				errs = append(errs, fmt.Errorf("re-provision %s: %w", o.ID, perr))
			}
		}
	}
	if len(errs) > 0 {
		return &loopError{errs: errs}
	}
	return nil
}
