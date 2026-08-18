package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Person is a staff member: field engineer, customer manager, adjudicator.
type Person struct {
	ID        string
	Code      string
	Name      string
	Role      string
	Email     string
	Active    bool
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Customer is the workshop receiving a deployment.
type Customer struct {
	ID        string
	Code      string
	Name      string
	Workshop  string
	Contact   string
	Status    string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Device is a unit of equipment installed at a customer site.
type Device struct {
	ID         string
	CustomerID string
	Serial     string
	Name       string
	Model      string
	Status     string
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DeploymentOrder ties a customer, field engineer, devices and a trial together.
type DeploymentOrder struct {
	ID                  string
	Code                string
	CustomerID          string
	FieldEngineerID     string
	CustomerManagerID   string
	TrialID             string
	Status              DeploymentStatus
	HandlingMode        HandlingMode
	ResponsibleRole     ResponsibleRole
	BackfillWindowHours int
	LastError           string
	RetryCount          int
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// DeploymentStep is one element of the five-step provisioning saga.
type DeploymentStep struct {
	ID           string
	OrderID      string
	StepNo       int
	StepName     string
	Status       StepStatus
	AttemptCount int
	LastError    string
	Version      int
	UpdatedAt    time.Time
}

// StepName constants keep step ordering in one place.
const (
	StepRegisterOrder      = "register_order"
	StepBindDevices        = "bind_devices"
	StepActivateTrial      = "activate_trial"
	StepDispatchInspection = "dispatch_inspection"
	StepGenerateBill       = "generate_bill"
)

// OrderedSteps is the canonical, immutable saga order.
var OrderedSteps = []string{StepRegisterOrder, StepBindDevices, StepActivateTrial, StepDispatchInspection, StepGenerateBill}

// DeploymentDevice links a device into a deployment.
type DeploymentDevice struct {
	ID       string
	OrderID  string
	DeviceID string
	BoundAt  time.Time
	Version  int
}

// TrialPeriod is the effective window plus acceptance deadline for an order.
type Trial struct {
	ID                 string
	OrderID            string
	EffectiveFrom      time.Time
	EffectiveTo        time.Time
	AcceptanceDeadline time.Time
	Status             TrialStatus
	AcceptedAt         *time.Time
	AcceptedBy         string
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Inspection is a dispatched maintenance visit.
type Inspection struct {
	ID          string
	OrderID     string
	DeviceID    string
	Round       int
	Type        string
	ScheduledAt time.Time
	CompletedAt *time.Time
	Status      InspectionStatus
	AssigneeID  string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// OperationRecord is a single field action, possibly backfilled offline.
type OperationRecord struct {
	ID             string
	OrderID        string
	DeviceID       string
	ResponsibleID  string
	OccurredAt     time.Time
	RecordedAt     time.Time
	ReceivedAt     *time.Time
	Source         string
	ClientSequence int
	Hours          decimal.Decimal
	Content        string
	Status         RecordStatus
	ChangeVersion  int
	ConflictID     string
	BatchID        string
	ManualID       string
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// RecordSource enumerates where a record came from.
const (
	SourceOnline   = "online"
	SourceBackfill = "offline_backfill"
)

// CustomerWorkHour is the workshop-reported counterpart used for conflict checks.
type CustomerWorkHour struct {
	ID         string
	DeviceID   string
	WorkDate   string
	Hours      decimal.Decimal
	ReportedBy string
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Adjudication is the adjudicator's ruling over a record/work-hour conflict.
type Adjudication struct {
	ID            string
	RecordID      string
	WorkHourID    string
	Winner        string
	DeltaHours    decimal.Decimal
	AttributedTo  string
	AdjudicatorID string
	Reason        string
	DecidedAt     time.Time
	Version       int
}

// AdjudicationWinner values name the prevailing party.
const (
	WinnerField    = "field"
	WinnerCustomer = "customer"
)

// ReconciliationBill settles a period of operation records.
type ReconciliationBill struct {
	ID          string
	OrderID     string
	PeriodNo    int
	PeriodStart time.Time
	PeriodEnd   time.Time
	TotalHours  decimal.Decimal
	Rate        decimal.Decimal
	Amount      decimal.Decimal
	Status      BillStatus
	GeneratedBy string
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SyncBatch is a unit of offline records collected from a site per shift.
type SyncBatch struct {
	ID             string
	OrderID        string
	LeaseOwner     string
	LeaseUntil     *time.Time
	Status         SyncBatchStatus
	RecordCount    int
	ProcessedCount int
	LastError      string
	RetryCount     int
	NextRetryAt    *time.Time
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ManualVerification is the human-review channel for expired-window records.
type ManualVerification struct {
	ID         string
	RecordID   string
	OrderID    string
	Reason     string
	Status     string
	ReviewerID string
	ReviewedAt *time.Time
	Decision   string
	Note       string
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ManualVerificationStatus values.
const (
	ManualPending  = "pending"
	ManualReviewed = "reviewed"
)

// ManualDecision values.
const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
)

// AuditEntry records who did what to which entity and when.
type AuditEntry struct {
	ID         int64
	ActorID    string
	ActorRole  string
	Action     string
	EntityType string
	EntityID   string
	Detail     string
	OccurredAt time.Time
}

// PermanentFailure is a dead-letter record for tasks that exhausted retries.
type PermanentFailure struct {
	ID            string
	EntityType    string
	EntityID      string
	TaskType      string
	LastError     string
	Attempts      int
	LastAttemptAt time.Time
	Status        string
	CreatedAt     time.Time
}

// SyncState tracks the incremental-pull cursor per deployment.
type SyncState struct {
	OrderID           string
	LastChangeVersion int
	LastPulledAt      *time.Time
	LastBackfillAt    *time.Time
	UpdatedAt         time.Time
}
