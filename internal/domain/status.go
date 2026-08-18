// Package domain holds the entity definitions, value objects and state machines
// for the field-deployment offline-sync platform. The package depends only on
// the standard library, decimal and uuid; it never imports HTTP or storage
// implementation packages, keeping the dependency direction one-way.
package domain

import (
	"fmt"

	"sitesync/internal/errorsx"
)

// DeploymentStatus enumerates the deployment order lifecycle.
type DeploymentStatus string

const (
	DeploymentDraft        DeploymentStatus = "draft"
	DeploymentProvisioning DeploymentStatus = "provisioning"
	DeploymentActive       DeploymentStatus = "active"
	DeploymentPendingRetry DeploymentStatus = "pending_retry"
	DeploymentCompensating DeploymentStatus = "compensating"
	DeploymentAborted      DeploymentStatus = "aborted"
)

// StepStatus enumerates the lifecycle of a single saga step.
type StepStatus string

const (
	StepPending     StepStatus = "pending"
	StepProcessing  StepStatus = "processing"
	StepDone        StepStatus = "done"
	StepFailed      StepStatus = "failed"
	StepCompensated StepStatus = "compensated"
)

// TrialStatus enumerates the trial period lifecycle.
type TrialStatus string

const (
	TrialPending   TrialStatus = "pending"
	TrialActive    TrialStatus = "active"
	TrialAccepted  TrialStatus = "accepted"
	TrialOverdue   TrialStatus = "overdue"
	TrialEscalated TrialStatus = "escalated"
	TrialConverted TrialStatus = "converted"
)

// RecordStatus enumerates the operation-record lifecycle.
type RecordStatus string

const (
	RecordPending            RecordStatus = "pending"
	RecordAccepted           RecordStatus = "accepted"
	RecordConflict           RecordStatus = "conflict"
	RecordAdjudicated        RecordStatus = "adjudicated"
	RecordManualVerifyNeeded RecordStatus = "manual_verify_pending"
	RecordVerified           RecordStatus = "verified"
	RecordRevoked            RecordStatus = "revoked"
)

// SyncBatchStatus enumerates the shift-polling batch lifecycle.
type SyncBatchStatus string

const (
	SyncBatchPending    SyncBatchStatus = "pending"
	SyncBatchLeasing    SyncBatchStatus = "leasing"
	SyncBatchProcessing SyncBatchStatus = "processing"
	SyncBatchCompleted  SyncBatchStatus = "completed"
	SyncBatchFailed     SyncBatchStatus = "failed"
	SyncBatchPermanent  SyncBatchStatus = "permanent_failure"
)

// InspectionStatus enumerates the inspection lifecycle.
type InspectionStatus string

const (
	InspectionDispatched InspectionStatus = "dispatched"
	InspectionCompleted  InspectionStatus = "completed"
	InspectionCancelled  InspectionStatus = "cancelled"
)

// BillStatus enumerates the reconciliation bill lifecycle.
type BillStatus string

const (
	BillDraft   BillStatus = "draft"
	BillIssued  BillStatus = "issued"
	BillSettled BillStatus = "settled"
	BillVoided  BillStatus = "voided"
)

// ResponsibleRole names who currently owns a deployment.
type ResponsibleRole string

const (
	ResponsibleFieldEngineer ResponsibleRole = "field_engineer"
	ResponsibleCustomerMgr   ResponsibleRole = "customer_manager"
)

// HandlingMode names how the deployment is being handled.
type HandlingMode string

const (
	HandlingOnSiteDebug HandlingMode = "on_site_debug"
	HandlingUrgeAccept  HandlingMode = "urge_acceptance"
	HandlingRentSale    HandlingMode = "formal_rent_sale_return"
)

// transitionTable maps a current state to the set of states it may move to.
// Any transition not present here is illegal and must be rejected.
var deploymentTransitions = map[DeploymentStatus]map[DeploymentStatus]struct{}{
	DeploymentDraft:        {DeploymentProvisioning: {}},
	DeploymentProvisioning: {DeploymentActive: {}, DeploymentPendingRetry: {}, DeploymentCompensating: {}},
	DeploymentPendingRetry: {DeploymentProvisioning: {}, DeploymentCompensating: {}},
	DeploymentCompensating: {DeploymentAborted: {}, DeploymentPendingRetry: {}},
	DeploymentActive:       {DeploymentPendingRetry: {}, DeploymentCompensating: {}},
}

var trialTransitions = map[TrialStatus]map[TrialStatus]struct{}{
	TrialPending:   {TrialActive: {}},
	TrialActive:    {TrialAccepted: {}, TrialOverdue: {}},
	TrialOverdue:   {TrialEscalated: {}, TrialAccepted: {}},
	TrialEscalated: {TrialConverted: {}, TrialAccepted: {}},
	TrialConverted: {},
	TrialAccepted:  {},
}

var recordTransitions = map[RecordStatus]map[RecordStatus]struct{}{
	RecordPending:            {RecordAccepted: {}, RecordConflict: {}, RecordManualVerifyNeeded: {}, RecordRevoked: {}},
	RecordAccepted:           {RecordVerified: {}, RecordRevoked: {}},
	RecordConflict:           {RecordAdjudicated: {}},
	RecordManualVerifyNeeded: {RecordVerified: {}, RecordRevoked: {}},
	RecordAdjudicated:        {},
	RecordVerified:           {RecordRevoked: {}},
	RecordRevoked:            {},
}

var billTransitions = map[BillStatus]map[BillStatus]struct{}{
	BillDraft:   {BillIssued: {}, BillVoided: {}},
	BillIssued:  {BillSettled: {}, BillVoided: {}},
	BillSettled: {BillVoided: {}},
	BillVoided:  {},
}

// canTransition reports whether moving from src to dst is allowed for the
// generic state machine. It is the single source of truth used by every entity.
func canTransition[S ~string](table map[S]map[S]struct{}, src, dst S) bool {
	allowed, ok := table[src]
	if !ok {
		return false
	}
	_, ok = allowed[dst]
	return ok
}

// AssertTransition returns an illegal-transition error when the move is forbidden.
func AssertTransition[S ~string](table map[S]map[S]struct{}, src, dst S) error {
	if src == dst {
		return nil
	}
	if canTransition(table, src, dst) {
		return nil
	}
	return fmt.Errorf("sitesync: illegal transition %s -> %s: %w", src, dst, errorsx.ErrIllegalTransition)
}

// AssertDeploymentTransition validates a deployment-order move.
func AssertDeploymentTransition(src, dst DeploymentStatus) error {
	return AssertTransition(deploymentTransitions, src, dst)
}

// AssertTrialTransition validates a trial-period move.
func AssertTrialTransition(src, dst TrialStatus) error {
	return AssertTransition(trialTransitions, src, dst)
}

// AssertRecordTransition validates an operation-record move.
func AssertRecordTransition(src, dst RecordStatus) error {
	return AssertTransition(recordTransitions, src, dst)
}

// AssertBillTransition validates a reconciliation-bill move.
func AssertBillTransition(src, dst BillStatus) error {
	return AssertTransition(billTransitions, src, dst)
}
