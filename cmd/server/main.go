// Command sitesync-server runs the HTTP service on port 48557 (overridable).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/httpapi"
	"sitesync/internal/logging"
	"sitesync/internal/scheduler"
	"sitesync/internal/service"
	"sitesync/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to YAML config file (optional)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	logger, err := logging.New(cfg.Log.Level, cfg.Log.Development)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	clk := clock.Real{}
	st, err := store.New(ctx, cfg, clk)
	if err != nil {
		logger.Fatal("store init failed", zap.Error(err))
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		logger.Fatal("migrate failed", zap.Error(err))
	}
	logger.Info("store migrated", zap.String("data_dir", cfg.Storage.DataDir))

	deps := service.Deps{
		UOW: st, Clock: clk, Logger: logger, Cfg: cfg,
		Persons: st, Customers: st, Devices: st,
		Orders: st, Steps: st, OrderDevices: st,
		Trials: st, Inspections: st,
		Records: st, WorkHours: st, Adjudications: st,
		Bills: st, Batches: st, Manuals: st, SyncState: st,
		Audit: st, Failures: st,
	}
	services := service.New(deps)

	sched := scheduler.New(services, deps)
	if cfg.Scheduler.RecoveryOnStart {
		if err := sched.Recover(ctx); err != nil {
			logger.Warn("recovery reported errors", zap.Error(err))
		}
	}
	if err := sched.Start(ctx); err != nil {
		logger.Fatal("scheduler start failed", zap.Error(err))
	}
	defer sched.Stop()

	probes := httpapi.Probes{
		DBPing:         st.Ping,
		DataDir:        cfg.Storage.DataDir,
		SchedulerReady: sched.Ready,
		SchemaVersion:  st.SchemaVersion,
	}
	srv := httpapi.New(services, cfg, probes, logger, clk)

	logger.Info("starting sitesync", zap.Int("port", cfg.Server.Port))
	runErr := srv.Run(logging.IntoContext(ctx, logger))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("graceful shutdown failed", zap.Error(err))
	}
	if runErr != nil {
		logger.Fatal("server stopped", zap.Error(runErr))
	}
	logger.Info("sitesync stopped")
}
