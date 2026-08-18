package window

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// base is a fixed reference instant reused across the policy tests so the
// outcomes depend only on the offsets, never on wall-clock drift.
var base = time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)

func TestBackfillWindowBoundaryWithinThenExpired(t *testing.T) {
	p := BackfillPolicy{}
	occurred := base.Add(-168 * time.Hour) // 7 days before now
	// Exactly at the deadline: half-open, still within.
	verdict, reason := p.Classify(occurred, 168, base)
	assert.Equal(t, WithinWindow, verdict)
	assert.Empty(t, reason)
	// One nanosecond past the deadline: expired and routed to manual.
	verdict, reason = p.Classify(occurred, 168, base.Add(time.Nanosecond))
	assert.Equal(t, ExpiredManual, verdict)
	assert.Equal(t, "backfill window expired", reason)
}

func TestBackfillWindowClockInjectionDeterministic(t *testing.T) {
	p := BackfillPolicy{}
	occurred := base.Add(-200 * time.Hour)
	// Advancing the injected clock flips the verdict without real waiting.
	assert.False(t, p.Expired(occurred, 168, base.Add(-33*time.Hour)))
	assert.True(t, p.Expired(occurred, 168, base))
}

func TestBackfillWindowZeroWindowIsImmediatelyExpired(t *testing.T) {
	p := BackfillPolicy{}
	// A zero-length window collapses the deadline onto occurredAt, so any
	// observation strictly after the record occurred is expired.
	assert.True(t, p.Expired(base.Add(-time.Second), 0, base))
	assert.False(t, p.Expired(base, 0, base))
}

func TestEscalationPolicyStaleBoundaryAndClockInjection(t *testing.T) {
	p := EscalationPolicy{}
	created := base.Add(-336 * time.Hour) // 14 days before now
	// Exactly at the review deadline: still fresh.
	verdict, reason := p.Classify(created, 336, base)
	assert.Equal(t, Fresh, verdict)
	assert.Empty(t, reason)
	// One nanosecond past: escalate.
	verdict, reason = p.Classify(created, 336, base.Add(time.Nanosecond))
	assert.Equal(t, StaleEscalate, verdict)
	assert.Equal(t, "manual review window expired", reason)
	// Clock injection: a freshly created review is never stale.
	assert.False(t, p.Stale(base, 336, base.Add(time.Hour)))
}
