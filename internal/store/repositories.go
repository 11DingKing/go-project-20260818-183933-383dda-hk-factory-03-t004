package store

import (
	"context"
	"time"

	"sitesync/internal/domain"
)

// PersonRepository manages staff records.
type PersonRepository interface {
	CreatePerson(ctx context.Context, p domain.Person) (domain.Person, error)
	GetPerson(ctx context.Context, id string) (domain.Person, error)
}

// CustomerRepository manages workshop customers.
type CustomerRepository interface {
	CreateCustomer(ctx context.Context, c domain.Customer) (domain.Customer, error)
	GetCustomer(ctx context.Context, id string) (domain.Customer, error)
}

// DeviceRepository manages equipment units.
type DeviceRepository interface {
	CreateDevice(ctx context.Context, d domain.Device) (domain.Device, error)
	GetDevice(ctx context.Context, id string) (domain.Device, error)
}

// DeploymentOrderRepository manages order lifecycle with optimistic locking.
type DeploymentOrderRepository interface {
	CreateOrder(ctx context.Context, o domain.DeploymentOrder) (domain.DeploymentOrder, error)
	GetOrder(ctx context.Context, id string) (domain.DeploymentOrder, error)
	UpdateStatus(ctx context.Context, id string, from domain.DeploymentStatus, to domain.DeploymentStatus, fromVersion int) error
	UpdateResponsibility(ctx context.Context, id string, role domain.ResponsibleRole, mode domain.HandlingMode, customerManagerID string, fromVersion int) error
	LinkTrial(ctx context.Context, orderID, trialID string, fromVersion int) error
	BumpRetry(ctx context.Context, id string, lastError string) error
	ListByStatus(ctx context.Context, statuses ...domain.DeploymentStatus) ([]domain.DeploymentOrder, error)
}

// DeploymentStepRepository manages the per-saga step rows.
type DeploymentStepRepository interface {
	ListStepsByOrder(ctx context.Context, orderID string) ([]domain.DeploymentStep, error)
	MarkProcessing(ctx context.Context, id string, fromVersion int) (domain.DeploymentStep, error)
	MarkDone(ctx context.Context, id string, fromVersion int) (domain.DeploymentStep, error)
	MarkFailed(ctx context.Context, id string, fromVersion int, lastErr string) error
	MarkCompensated(ctx context.Context, id string, fromVersion int) error
}

// DeploymentDeviceRepository binds devices to an order.
type DeploymentDeviceRepository interface {
	BindDevice(ctx context.Context, orderID, deviceID string) error
	ListDevicesByOrder(ctx context.Context, orderID string) ([]domain.DeploymentDevice, error)
	UnbindAllDevices(ctx context.Context, orderID string) (int64, error)
}

// TrialRepository manages trial periods and deadline queries.
type TrialRepository interface {
	CreateTrial(ctx context.Context, t domain.Trial) (domain.Trial, error)
	GetTrial(ctx context.Context, id string) (domain.Trial, error)
	GetTrialByOrder(ctx context.Context, orderID string) (domain.Trial, error)
	UpdateTrialStatus(ctx context.Context, id string, from domain.TrialStatus, to domain.TrialStatus, fromVersion int) error
	SetAccepted(ctx context.Context, id, by string, at time.Time, fromVersion int) error
	ListTrialsPastDeadline(ctx context.Context, now time.Time) ([]domain.Trial, error)
	ListTrialsByStatus(ctx context.Context, statuses ...domain.TrialStatus) ([]domain.Trial, error)
}

// InspectionRepository manages dispatched inspections.
type InspectionRepository interface {
	CreateInspection(ctx context.Context, ins domain.Inspection) (domain.Inspection, error)
	GetInspectionByOrderRound(ctx context.Context, orderID string, round int) (domain.Inspection, error)
	CancelInspectionsByOrder(ctx context.Context, orderID string) (int64, error)
}

// OperationRecordRepository manages field records, change versions and pulls.
type OperationRecordRepository interface {
	InsertBatch(ctx context.Context, rs []domain.OperationRecord) ([]domain.OperationRecord, error)
	GetRecord(ctx context.Context, id string) (domain.OperationRecord, error)
	GetByOrderSeq(ctx context.Context, orderID string, seq int) (domain.OperationRecord, error)
	UpdateRecordStatus(ctx context.Context, id string, from domain.RecordStatus, to domain.RecordStatus, fromVersion int) error
	SetConflict(ctx context.Context, id, conflictID string, fromVersion int) error
	SetManual(ctx context.Context, id, manualID string, fromVersion int) error
	CorrectHours(ctx context.Context, id string, hours string, fromVersion int) error
	ListRecordsByFilter(ctx context.Context, f domain.RecordFilter, page domain.Page) ([]domain.OperationRecord, int64, error)
	ListRecordsByBatch(ctx context.Context, batchID string) ([]domain.OperationRecord, error)
	ListRecordChanges(ctx context.Context, sinceVersion, limit int) ([]domain.OperationRecord, error)
	SumAcceptedHours(ctx context.Context, orderID string, from, to time.Time) (string, error)
}

// CustomerWorkHourRepository manages workshop-reported hours.
type CustomerWorkHourRepository interface {
	UpsertWorkHour(ctx context.Context, w domain.CustomerWorkHour) (domain.CustomerWorkHour, error)
	GetWorkHourByDeviceDate(ctx context.Context, deviceID, date string) (domain.CustomerWorkHour, error)
	ListWorkHoursByDevice(ctx context.Context, deviceID string) ([]domain.CustomerWorkHour, error)
}

// AdjudicationRepository manages conflict rulings.
type AdjudicationRepository interface {
	CreateAdjudication(ctx context.Context, a domain.Adjudication) (domain.Adjudication, error)
	GetAdjudicationByRecord(ctx context.Context, recordID string) (domain.Adjudication, error)
}

// ReconciliationBillRepository manages bills.
type ReconciliationBillRepository interface {
	CreateBill(ctx context.Context, b domain.ReconciliationBill) (domain.ReconciliationBill, error)
	GetBillByOrderPeriod(ctx context.Context, orderID string, periodNo int) (domain.ReconciliationBill, error)
	GetBillByID(ctx context.Context, id string) (domain.ReconciliationBill, error)
	UpdateBillStatus(ctx context.Context, id string, from, to domain.BillStatus, fromVersion int) error
	UpdateBillTotals(ctx context.Context, id string, totalHours, rate, amount string, fromVersion int) error
}

// SyncBatchRepository manages shift-polling batches and leases.
type SyncBatchRepository interface {
	CreateSyncBatch(ctx context.Context, b domain.SyncBatch) (domain.SyncBatch, error)
	GetSyncBatch(ctx context.Context, id string) (domain.SyncBatch, error)
	AcquireLease(ctx context.Context, id, owner string, leaseUntil time.Time) (domain.SyncBatch, error)
	ReleaseLease(ctx context.Context, id string) (domain.SyncBatch, error)
	ReclaimExpired(ctx context.Context, now time.Time) (int64, error)
	UpdateBatchProgress(ctx context.Context, id string, processed int, lastErr string, status domain.SyncBatchStatus, fromVersion int) error
	ListAccumulatedBatches(ctx context.Context, f domain.SyncAccumulatedFilter, page domain.Page) ([]domain.SyncBatch, int64, error)
	ListBatchesRetryDue(ctx context.Context, now time.Time) ([]domain.SyncBatch, error)
	BumpBatchRetry(ctx context.Context, id string, lastErr string, nextRetry time.Time) error
	MarkBatchPermanent(ctx context.Context, id string, lastErr string) error
}

// ManualVerificationRepository manages the human-review channel.
type ManualVerificationRepository interface {
	CreateManualVerification(ctx context.Context, m domain.ManualVerification) (domain.ManualVerification, error)
	GetManualByRecord(ctx context.Context, recordID string) (domain.ManualVerification, error)
	ReviewManual(ctx context.Context, id, reviewerID string, at time.Time, decision, note string, fromVersion int) (domain.ManualVerification, error)
	ListManualPending(ctx context.Context, page domain.Page) ([]domain.ManualVerification, int64, error)
}

// SyncStateRepository tracks incremental-pull cursors.
type SyncStateRepository interface {
	UpsertBackfill(ctx context.Context, orderID string, at time.Time, changeVersion int) error
}

// AuditRepository writes and queries audit entries.
type AuditRepository interface {
	AppendAudit(ctx context.Context, e domain.AuditEntry) error
	ListAuditByFilter(ctx context.Context, f domain.AuditFilter, page domain.Page) ([]domain.AuditEntry, int64, error)
}

// FailureRepository manages dead-letter records.
type FailureRepository interface {
	RecordFailure(ctx context.Context, entityType, entityID, taskType, lastErr string) error
	ListFailures(ctx context.Context, page domain.Page) ([]domain.PermanentFailure, int64, error)
	RequeueFailure(ctx context.Context, id string) error
}
