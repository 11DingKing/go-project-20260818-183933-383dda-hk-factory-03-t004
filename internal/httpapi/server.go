// Package httpapi exposes the sitesync HTTP surface. It only translates wire
// requests into service calls and service results into JSON; it never builds SQL
// or touches storage files, keeping the dependency direction one-way.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"
	mid "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/logging"
	"sitesync/internal/service"
)

// Probes are the readiness checks the /readyz endpoint runs.
type Probes struct {
	DBPing         func(ctx context.Context) error
	DataDir        string
	SchedulerReady func() bool
	SchemaVersion  func(ctx context.Context) (int, error)
}

// Server holds the HTTP handler wiring and the underlying http.Server.
type Server struct {
	services *service.Services
	cfg      config.Config
	clock    clock.Clock
	probes   Probes
	logger   *zap.Logger
	srv      *http.Server
}

// New assembles the HTTP server.
func New(services *service.Services, cfg config.Config, probes Probes, logger *zap.Logger, clk clock.Clock) *Server {
	return &Server{services: services, cfg: cfg, clock: clk, probes: probes, logger: logger}
}

// Router builds the chi router with all routes registered.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(RequestLogger(s.logger))
	r.Use(Recoverer(s.logger))
	r.Use(mid.NoCache)

	r.Get("/healthz", s.healthz)
	r.Get("/readyz", s.readyz)

	r.Route("/api", func(r chi.Router) {
		r.Post("/customers", s.createCustomer)
		r.Post("/devices", s.createDevice)
		r.Post("/persons", s.createPerson)

		r.Post("/deployments", s.createDeployment)
		r.Post("/deployments/{id}/provision", s.provisionDeployment)
		r.Post("/deployments/{id}/compensate", s.compensateDeployment)
		r.Get("/deployments/{id}", s.getDeployment)
		r.Post("/deployments/{id}/records/backfill", s.backfillRecords)

		r.Get("/records", s.queryRecords)
		r.Get("/records/changes", s.pullChanges)
		r.Post("/records/{id}/manual-verify", s.manualVerify)
		r.Post("/records/{id}/correct", s.correctRecord)
		r.Post("/records/{id}/revoke", s.revokeRecord)

		r.Post("/adjudications", s.adjudicate)
		r.Post("/trials/{id}/accept", s.acceptTrial)
		r.Post("/trials/{id}/convert", s.convertTrial)
		r.Get("/trials/overdue", s.listOverdueTrials)

		r.Post("/reconciliation/generate", s.generateBill)
		r.Post("/reconciliation/bills/{id}/issue", s.issueBill)
		r.Get("/reconciliation/export", s.exportReconciliation)
		r.Post("/reconciliation/diff", s.reconcileDiff)

		r.Get("/sync/accumulated", s.listAccumulated)
		r.Get("/manual/pending", s.listManualPending)
		r.Get("/manual/stale", s.listStaleManuals)
		r.Get("/audit", s.queryAudit)
		r.Get("/permanent-failures", s.listPermanentFailures)
		r.Post("/permanent-failures/{id}/requeue", s.requeueFailure)
	})
	return r
}

// Run starts listening on the configured port and blocks until ctx is done,
// then drains in-flight requests within the shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	handler := s.Router()
	s.srv = &http.Server{
		Addr:         addrFor(s.cfg),
		Handler:      handler,
		ReadTimeout:  s.cfg.Server.ReadTimeout,
		WriteTimeout: s.cfg.Server.WriteTimeout,
		IdleTimeout:  s.cfg.Server.IdleTimeout,
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
	}
	logger := logging.IntoContext(ctx, s.logger)
	errCh := make(chan error, 1)
	go func() {
		logging.FromContext(logger).Info("http server listening", zap.String("addr", s.srv.Addr))
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

// Shutdown drains in-flight requests within the caller-supplied context. The
// bounded shutdown context is created in cmd/, never in this package, so no
// detached root context is constructed here; request contexts inherit the
// BaseContext set in Run, so cancellation propagates end to end.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func addrFor(cfg config.Config) string {
	return fmt.Sprintf(":%d", cfg.Server.Port)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	status, msg := service.MapError(err)
	writeJSON(w, status, errEnvelope{Error: msg, Status: http.StatusText(status)})
}

type errEnvelope struct {
	Error  string `json:"error"`
	Status string `json:"status"`
}

func actorFrom(r *http.Request) string {
	if a := r.Header.Get("X-Actor"); a != "" {
		return a
	}
	return "api"
}
