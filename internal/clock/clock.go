// Package clock provides an injectable time source so that deadline windows,
// escalation checks and audit timestamps can be tested without real waiting.
package clock

import (
	"sync"
	"time"
)

// Clock abstracts the current time. Production code uses Real; tests use Fake.
type Clock interface {
	Now() time.Time
}

// Real returns the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake holds a mutable instant protected by a mutex. It is safe for concurrent
// use by the race detector, which lets scheduler tests advance the clock while
// background goroutines read it.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake creates a fake clock anchored at the given instant.
func NewFake(at time.Time) *Fake { return &Fake{now: at} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake clock forward by d. A negative d moves it backward,
// which is useful for simulating a record that occurred in the past.
func (f *Fake) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

// Set replaces the fake clock's instant.
func (f *Fake) Set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = at
}
