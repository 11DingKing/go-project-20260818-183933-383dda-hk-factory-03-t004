// Package window encodes the time-window policies that gate sitesync's
// intermittent-sync flow. The backfill policy decides whether an offline record
// is still inside its replay window or must be routed to the manual-verification
// channel; the escalation policy decides whether a manual-verification row has
// waited past its review window and must be escalated to a higher tier.
//
// Both policies are pure: they take the current time as an argument so callers
// inject the clock (real in production, fake in tests) and tests never depend on
// real waiting. They contain no I/O and no knowledge of HTTP or storage.
package window

import "time"

// BackfillVerdict is the outcome of classifying a record against its backfill
// window.
type BackfillVerdict string

const (
	// WithinWindow means the record is still eligible for automatic resolution.
	WithinWindow BackfillVerdict = "within_window"
	// ExpiredManual means the replay window has closed; the record must be
	// routed to the manual-verification channel instead of being auto-resolved.
	ExpiredManual BackfillVerdict = "expired_manual"
)

// BackfillPolicy answers: given a record that occurred at occurredAt and an
// order whose replay window is windowHours long, is the record still eligible
// for automatic processing at now?
type BackfillPolicy struct{}

// Deadline returns the instant past which a record is expired. It is
// occurredAt + windowHours so the boundary is half-open: a record exactly at
// the deadline is still within the window, one nanosecond later is expired.
// This matches the original inline rule now.Sub(occurred) > window.
func (BackfillPolicy) Deadline(occurredAt time.Time, windowHours int) time.Time {
	return occurredAt.Add(time.Duration(windowHours) * time.Hour)
}

// Expired reports whether now is strictly past the record's backfill deadline.
// A zero or negative window collapses the deadline onto occurredAt, so any
// record observed after it occurred is expired, preserving the prior behaviour.
func (p BackfillPolicy) Expired(occurredAt time.Time, windowHours int, now time.Time) bool {
	return now.After(p.Deadline(occurredAt, windowHours))
}

// Classify returns the verdict and, when expired, the human-readable reason used
// to open a manual-verification row. It is the single source of truth for the
// expired-window routing decision so the service layer never re-derives it.
func (p BackfillPolicy) Classify(occurredAt time.Time, windowHours int, now time.Time) (BackfillVerdict, string) {
	if p.Expired(occurredAt, windowHours, now) {
		return ExpiredManual, "backfill window expired"
	}
	return WithinWindow, ""
}

// EscalationVerdict is the outcome of classifying a manual-verification row
// against its review window.
type EscalationVerdict string

const (
	// Fresh means the manual review is still within its review window.
	Fresh EscalationVerdict = "fresh"
	// StaleEscalate means the review window has closed and the item must be
	// escalated to a higher tier for human attention.
	StaleEscalate EscalationVerdict = "stale_escalate"
)

// EscalationPolicy answers: given a manual-verification row created at createdAt
// and a review window of reviewWindowHours, has it waited past the window at
// now and must be escalated?
type EscalationPolicy struct{}

// Deadline returns the instant past which a manual review is stale.
func (EscalationPolicy) Deadline(createdAt time.Time, reviewWindowHours int) time.Time {
	return createdAt.Add(time.Duration(reviewWindowHours) * time.Hour)
}

// Stale reports whether now is strictly past the review deadline.
func (p EscalationPolicy) Stale(createdAt time.Time, reviewWindowHours int, now time.Time) bool {
	return now.After(p.Deadline(createdAt, reviewWindowHours))
}

// Classify returns the escalation verdict and the reason used in audit entries.
func (p EscalationPolicy) Classify(createdAt time.Time, reviewWindowHours int, now time.Time) (EscalationVerdict, string) {
	if p.Stale(createdAt, reviewWindowHours, now) {
		return StaleEscalate, "manual review window expired"
	}
	return Fresh, ""
}
