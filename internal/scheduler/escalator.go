package scheduler

import (
	"context"

	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/service"
)

// Escalator periodically scans for trials past their acceptance deadline and
// escalates responsibility from field engineer to customer manager.
type Escalator struct {
	deps   service.Deps
	trial  *service.TrialService
	cfg    config.SchedulerConfig
	clock  clock.Clock
	logger *zap.Logger
}

// tick runs one escalation pass.
func (e *Escalator) tick(ctx context.Context) {
	count, err := e.trial.CheckDeadlines(ctx)
	if err != nil {
		e.logger.Warn("escalator check deadlines failed", zap.Error(err))
		return
	}
	if count > 0 {
		e.logger.Info("escalator escalated trials", zap.Int("count", count))
	}
}
