// Package scheduler runs the background loops that pull offline records per
// shift, escalate overdue trials and resume interrupted work after a restart.
// Each loop is a ticker that can be stopped gracefully and reports readiness.
package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/service"
)

// Scheduler owns the poller and escalator loops plus the recovery pass.
type Scheduler struct {
	poller    *Poller
	escalator *Escalator
	recoverer *Recoverer
	cfg       config.SchedulerConfig
	clock     clock.Clock
	logger    *zap.Logger
	stop      chan struct{}
	wg        sync.WaitGroup
	startedMu sync.RWMutex
	started   bool
	cancel    context.CancelFunc
}

// New assembles a scheduler from the service container and shared dependencies.
func New(svc *service.Services, deps service.Deps) *Scheduler {
	return &Scheduler{
		poller:    &Poller{deps: deps, sync: svc.Sync, cfg: deps.Cfg.Scheduler, clock: deps.Clock, logger: deps.Logger},
		escalator: &Escalator{deps: deps, trial: svc.Trial, cfg: deps.Cfg.Scheduler, clock: deps.Clock, logger: deps.Logger},
		recoverer: &Recoverer{deps: deps, deployment: svc.Deployment, sync: svc.Sync, cfg: deps.Cfg.Scheduler, clock: deps.Clock, logger: deps.Logger},
		cfg:       deps.Cfg.Scheduler,
		clock:     deps.Clock,
		logger:    deps.Logger,
		stop:      make(chan struct{}),
	}
}

// Start launches the poller and escalator loops. It is safe to call once; a
// second call is a no-op. The loops stop when ctx is cancelled or Stop is called.
func (s *Scheduler) Start(ctx context.Context) error {
	s.startedMu.Lock()
	defer s.startedMu.Unlock()
	if s.started {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	s.wg.Add(2)
	go s.runLoop(runCtx, "poller", s.cfg.PollInterval, s.poller.tick)
	go s.runLoop(runCtx, "escalator", s.cfg.EscalatorInterval, s.escalator.tick)
	s.logger.Info("scheduler started",
		zap.Duration("poll_interval", s.cfg.PollInterval),
		zap.Duration("escalator_interval", s.cfg.EscalatorInterval))
	return nil
}

// runLoop drives a single ticker-based task until ctx is cancelled.
func (s *Scheduler) runLoop(ctx context.Context, name string, interval time.Duration, fn func(context.Context)) {
	defer s.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("loop stopped", zap.String("name", name))
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// Stop signals both loops to exit and waits for them to drain.
func (s *Scheduler) Stop() {
	s.startedMu.Lock()
	if !s.started {
		s.startedMu.Unlock()
		return
	}
	s.started = false
	if s.cancel != nil {
		s.cancel()
	}
	s.startedMu.Unlock()
	s.wg.Wait()
}

// Ready reports whether the scheduler has started and is still running.
func (s *Scheduler) Ready() bool {
	s.startedMu.RLock()
	defer s.startedMu.RUnlock()
	return s.started
}

// Recover resumes work that was interrupted by a process restart: it reclaims
// expired sync leases, re-provisions deployment orders stuck mid-saga and
// retries failed sync batches once. Completed items are never re-processed.
func (s *Scheduler) Recover(ctx context.Context) error {
	if err := s.recoverer.Recover(ctx); err != nil {
		var le *loopError
		if errors.As(err, &le) {
			s.logger.Warn("recovery completed with errors", zap.Int("errors", len(le.errs)), zap.Strings("messages", le.messages()))
		}
		return err
	}
	s.logger.Info("recovery completed")
	return nil
}

// loopError aggregates non-fatal errors from the recovery pass.
type loopError struct {
	errs []error
}

func (e *loopError) Error() string { return "scheduler: recovery completed with errors" }
func (e *loopError) Unwrap() error {
	if len(e.errs) > 0 {
		return e.errs[0]
	}
	return nil
}
func (e *loopError) messages() []string {
	out := make([]string, len(e.errs))
	for i, err := range e.errs {
		out[i] = err.Error()
	}
	return out
}
