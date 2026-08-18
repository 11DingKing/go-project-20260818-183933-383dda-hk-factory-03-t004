package domain

import "time"

// RecordFilter narrows an operation-record query. All fields are optional.
type RecordFilter struct {
	CustomerID string
	DeviceID   string
	OrderID    string
	Status     RecordStatus
	Source     string
	From       time.Time
	To         time.Time
}

// Page is an offset-based pagination request. Size is clamped server-side.
type Page struct {
	Offset int
	Size   int
}

// DefaultPageSize is the largest page returned when none is requested.
const DefaultPageSize = 50

// MaxPageSize bounds how many rows a single page may return.
const MaxPageSize = 200

// Normalize clamps the page size into the allowed range and the offset to >= 0.
func (p Page) Normalize() Page {
	size := p.Size
	if size <= 0 {
		size = DefaultPageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	offset := p.Offset
	if offset < 0 {
		offset = 0
	}
	return Page{Offset: offset, Size: size}
}

// Result wraps a page of items with paging metadata for clients.
type Result[T any] struct {
	Items  []T   `json:"items"`
	Offset int   `json:"offset"`
	Size   int   `json:"size"`
	Total  int64 `json:"total"`
	Next   int   `json:"next,omitempty"`
}

// AuditFilter narrows an audit-log query.
type AuditFilter struct {
	ActorID    string
	EntityType string
	EntityID   string
	Action     string
	From       time.Time
	To         time.Time
}

// SyncAccumulatedFilter narrows the accumulated-batch query.
type SyncAccumulatedFilter struct {
	OrderID string
	Status  SyncBatchStatus
}
