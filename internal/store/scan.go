package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// formatTime renders a time as RFC3339Nano UTC text for TEXT columns.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// formatTimePtr renders a nullable time as a NullString.
func formatTimePtr(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*t), Valid: true}
}

// parseTime decodes a NullString back into a time. Zero value when absent.
func parseTime(ns sql.NullString) time.Time {
	if !ns.Valid || ns.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseTimePtr decodes a NullString into a pointer, nil when absent.
func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ns.String)
	if err != nil {
		return nil
	}
	return &t
}

// parseDecimal decodes a NullString canonical decimal into a decimal.Decimal.
func parseDecimal(ns sql.NullString) decimal.Decimal {
	if !ns.Valid || ns.String == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(ns.String)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// decimalText renders a decimal for storage, falling back to "0".
func decimalText(d decimal.Decimal) string {
	if d.IsZero() {
		return "0"
	}
	return d.String()
}

// errNoRows wraps a missing-row result as a domain NotFound error while keeping
// sql.ErrNoRows in the chain. Wrapping both lets callers branch with either
// errors.Is(err, errorsx.ErrNotFound) (service/HTTP 404) or sql.ErrNoRows
// (store tests), without leaking the database sentinel to higher layers.
func errNoRows(target error, wrap string) error {
	if target == sql.ErrNoRows {
		return fmt.Errorf("%s: %w: %w", wrap, errorsx.ErrNotFound, sql.ErrNoRows)
	}
	return target
}

// intValue renders a bool as the 0/1 integer stored in flag columns.
func intValue(b bool) int {
	if b {
		return 1
	}
	return 0
}

// rowScanner is satisfied by both *sql.Row (single) and *sql.Rows (multi),
// letting one scan helper serve Get-by-id and List queries for an entity.
type rowScanner interface {
	Scan(dest ...any) error
}

// whereClause joins optional predicates into a " WHERE ..." prefix or "".
func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// collectRows drains rs into a slice using scan (which accepts the rowScanner
// that both *sql.Row and *sql.Rows satisfy), returning the first scan error or
// rs.Err(). Every multi-row list query funnels through here so the scan loop,
// error handling and nil-when-empty shape stay uniform.
func collectRows[T any](rs *sql.Rows, scan func(rowScanner) (T, error)) ([]T, error) {
	var rows []T
	for rs.Next() {
		x, err := scan(rs)
		if err != nil {
			return nil, err
		}
		rows = append(rows, x)
	}
	return rows, rs.Err()
}

// queryRows runs a multi-row query, defers Close and collects rows via scan. It
// is the single path for non-paginated list queries so they share one error
// wrap and one close lifecycle.
func queryRows[T any](ctx context.Context, ex executor, query, wrap string, scan func(rowScanner) (T, error), args ...any) ([]T, error) {
	rs, err := ex.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", wrap, err)
	}
	defer rs.Close()
	return collectRows(rs, scan)
}

// queryOne runs a single-row query and decodes it via scan, wrapping a missing
// row as a domain NotFound. It is the shared path for every Get-by-id method.
func queryOne[T any](ctx context.Context, ex executor, query string, scan func(rowScanner) (T, error), wrap string, args ...any) (T, error) {
	x, err := scan(ex.QueryRowContext(ctx, query, args...))
	if err != nil {
		return x, errNoRows(err, wrap)
	}
	return x, nil
}

// paginated runs a count query then a bounded list query sharing the same WHERE
// args. listSQL must omit the trailing "LIMIT ? OFFSET ?"; the helper appends it
// so every paginated query honours the same page-size and offset boundary.
func paginated[T any](ctx context.Context, ex executor, countSQL, listSQL string, whereArgs []any, page domain.Page, scan func(rowScanner) (T, error)) ([]T, int64, error) {
	page = page.Normalize()
	var total int64
	if err := ex.QueryRowContext(ctx, countSQL, whereArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: count: %w", err)
	}
	rs, err := ex.QueryContext(ctx, listSQL+" LIMIT ? OFFSET ?", append(whereArgs, page.Size, page.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list: %w", err)
	}
	defer rs.Close()
	rows, err := collectRows(rs, scan)
	if err != nil {
		return nil, total, err
	}
	return rows, total, nil
}
