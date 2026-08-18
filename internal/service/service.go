// Package service orchestrates the business use-cases of sitesync: deployment
// provisioning saga, offline backfill, conflict adjudication, trial escalation,
// reconciliation and read-side queries. It depends on store repository
// interfaces and the domain model, never on HTTP or the SQLite driver.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"sitesync/internal/logging"
	"sitesync/internal/store"
)

// Deps bundles every repository and infra primitive a service needs. The main
// entrypoint assembles it from a single *store.Store, which satisfies every
// interface, but each service only references the subset it uses.
type Deps struct {
	UOW           store.UnitOfWork
	Clock         clock.Clock
	Logger        *zap.Logger
	Cfg           config.Config
	Persons       store.PersonRepository
	Customers     store.CustomerRepository
	Devices       store.DeviceRepository
	Orders        store.DeploymentOrderRepository
	Steps         store.DeploymentStepRepository
	OrderDevices  store.DeploymentDeviceRepository
	Trials        store.TrialRepository
	Inspections   store.InspectionRepository
	Records       store.OperationRecordRepository
	WorkHours     store.CustomerWorkHourRepository
	Adjudications store.AdjudicationRepository
	Bills         store.ReconciliationBillRepository
	Batches       store.SyncBatchRepository
	Manuals       store.ManualVerificationRepository
	SyncState     store.SyncStateRepository
	Audit         store.AuditRepository
	Failures      store.FailureRepository
}

// Services is the aggregate the HTTP layer depends on.
type Services struct {
	Registry       *RegistryService
	Deployment     *DeploymentService
	Sync           *SyncService
	Adjudication   *AdjudicationService
	Trial          *TrialService
	Reconciliation *ReconciliationService
	Query          *QueryService
}

// New assembles every service from the shared dependencies.
func New(deps Deps) *Services {
	return &Services{
		Registry:       &RegistryService{deps: deps},
		Deployment:     &DeploymentService{deps: deps},
		Sync:           &SyncService{deps: deps},
		Adjudication:   &AdjudicationService{deps: deps},
		Trial:          &TrialService{deps: deps},
		Reconciliation: &ReconciliationService{deps: deps},
		Query:          &QueryService{deps: deps},
	}
}

// audit writes an audit entry joined to the caller's transaction when present.
func (d Deps) audit(ctx context.Context, actor, role, action, entityType, entityID, detail string) {
	if d.Audit == nil {
		return
	}
	entry := domain.AuditEntry{
		ActorID: actor, ActorRole: role, Action: action,
		EntityType: entityType, EntityID: entityID, Detail: detail,
		OccurredAt: d.Clock.Now(),
	}
	if err := d.Audit.AppendAudit(ctx, entry); err != nil {
		logging.FromContext(ctx).Warn("audit write failed", zap.Error(err))
	}
}

// mapError translates a domain error into an HTTP status code. It is the single
// place the service vocabulary meets the wire, so handlers stay thin.
func MapError(err error) (int, string) {
	if err == nil {
		return http.StatusOK, ""
	}
	switch {
	case errors.Is(err, errorsx.ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, errorsx.ErrAlreadyExists):
		return http.StatusConflict, err.Error()
	case errors.Is(err, errorsx.ErrVersionConflict), errors.Is(err, errorsx.ErrLeaseHeld):
		return http.StatusConflict, err.Error()
	case errors.Is(err, errorsx.ErrIllegalTransition):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, errorsx.ErrConflictExists):
		return http.StatusConflict, err.Error()
	case errors.Is(err, errorsx.ErrWindowExpired):
		return http.StatusGone, err.Error()
	case errors.Is(err, errorsx.ErrValidation):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, errorsx.ErrIncomplete):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, errorsx.ErrPermanent):
		return http.StatusTooManyRequests, err.Error()
	}
	if errorsx.IsRetryable(err) {
		return http.StatusConflict, err.Error()
	}
	return http.StatusInternalServerError, err.Error()
}

// notFound wraps a missing-entity error so handlers share one helper.
func notFound(entity, id string) error {
	return fmt.Errorf("%s %s: %w", entity, id, errorsx.ErrNotFound)
}

// now returns the injected current time.
func (d Deps) now() time.Time { return d.Clock.Now() }
