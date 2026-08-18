// Package errorsx defines the domain error vocabulary used across sitesync.
// Errors are sentinel values wrapped with %w so callers can use errors.Is to
// branch on the failure cause, and a typed RetryableError so the HTTP layer and
// the background scheduler can decide whether an operation is safe to retry.
package errorsx

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound is returned when an entity does not exist.
	ErrNotFound = errors.New("sitesync: entity not found")
	// ErrAlreadyExists is returned for a duplicate create that is not idempotent.
	ErrAlreadyExists = errors.New("sitesync: entity already exists")
	// ErrVersionConflict is returned when an optimistic-lock update affects zero rows.
	ErrVersionConflict = errors.New("sitesync: optimistic lock version conflict")
	// ErrIllegalTransition is returned when a state machine transition is not allowed.
	ErrIllegalTransition = errors.New("sitesync: illegal state transition")
	// ErrConflictExists is returned when a record collides with customer-reported hours.
	ErrConflictExists = errors.New("sitesync: work-hour conflict requires adjudication")
	// ErrWindowExpired is returned when a backfill record falls outside the replay window.
	ErrWindowExpired = errors.New("sitesync: backfill window expired")
	// ErrLeaseHeld is returned when a sync batch is leased by another worker.
	ErrLeaseHeld = errors.New("sitesync: sync batch lease held by another worker")
	// ErrValidation is returned for malformed request payloads.
	ErrValidation = errors.New("sitesync: validation error")
	// ErrIncomplete is returned when a saga cannot complete because a precondition failed.
	ErrIncomplete = errors.New("sitesync: operation incomplete")
	// ErrPermanent is returned when a task has exhausted its retries.
	ErrPermanent = errors.New("sitesync: permanent failure")
)

// RetryableError carries a retry hint alongside a wrapped cause. The Retry
// flag tells callers (HTTP 409, scheduler backoff) that re-running the same
// operation with the same arguments may succeed once the contention clears.
type RetryableError struct {
	Cause error
	Retry bool
}

func (e *RetryableError) Error() string {
	if e == nil || e.Cause == nil {
		return "sitesync: retryable error"
	}
	if e.Retry {
		return fmt.Sprintf("sitesync: retryable: %v", e.Cause)
	}
	return fmt.Sprintf("sitesync: not retryable: %v", e.Cause)
}

func (e *RetryableError) Unwrap() error { return e.Cause }

// Retryable wraps cause and marks whether the caller may retry the operation.
func Retryable(cause error, retry bool) error {
	if cause == nil {
		return nil
	}
	return &RetryableError{Cause: cause, Retry: retry}
}

// IsRetryable reports whether err carries a RetryableError whose Retry flag is set.
// It walks the wrapped chain so errors created with %w keep the hint.
func IsRetryable(err error) bool {
	var re *RetryableError
	if errors.As(err, &re) {
		return re.Retry
	}
	return errors.Is(err, ErrVersionConflict) || errors.Is(err, ErrLeaseHeld)
}
