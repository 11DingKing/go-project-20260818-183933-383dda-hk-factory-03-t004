package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"sitesync/internal/config"
	"sitesync/internal/errorsx"
)

// Clock is the minimal time abstraction the store needs to stamp rows.
type Clock interface {
	Now() time.Time
}

// UnitOfWork lets the service layer run several repository calls inside a
// single transaction without depending on *sql.Tx or the driver directly.
type UnitOfWork interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// executor is the common surface of *sql.DB and *sql.Tx so repository methods
// can run either against the pool or inside a context-carried transaction.
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store owns the SQLite connection, runs migrations and exposes transaction
// helpers shared by every repository implementation.
type Store struct {
	db    *sql.DB
	clock Clock
	cfg   config.StorageConfig
}

// New opens the SQLite database at the configured data directory, applies
// pragmas via the DSN and returns a Store ready for migration.
func New(ctx context.Context, cfg config.Config, clock Clock) (*Store, error) {
	if clock == nil {
		return nil, fmt.Errorf("store: clock is required")
	}
	if err := ensureDataDir(cfg.Storage.DataDir); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	if cfg.Storage.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.Storage.MaxOpenConns)
	}
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping sqlite: %w", err)
	}
	return &Store{db: db, clock: clock, cfg: cfg.Storage}, nil
}

// DB exposes the underlying connection pool for health checks.
func (s *Store) DB() *sql.DB { return s.db }

// Clock returns the injected time source.
func (s *Store) Clock() Clock { return s.clock }

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping verifies the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Migrate applies the embedded schema and records the schema version. It is
// idempotent: CREATE TABLE IF NOT EXISTS keeps re-runs safe.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.execSchema(ctx, schemaSQL); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return s.recordSchemaVersion(ctx, 1)
}

// SchemaVersion returns the highest applied migration version, or 0 if none.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return version, nil
}

func (s *Store) recordSchemaVersion(ctx context.Context, version int) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO schema_version (version, applied_at) VALUES (?, ?) ON CONFLICT DO NOTHING",
		version, s.clock.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) execSchema(ctx context.Context, blob string) error {
	for _, stmt := range splitStatements(blob) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply statement %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

// splitStatements breaks a SQL blob into individual statements on ";", skipping
// -- line comments. The sitesync schema contains no string-literal semicolons.
func splitStatements(blob string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(blob, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func firstLine(stmt string) string {
	if i := strings.IndexByte(stmt, '\n'); i >= 0 {
		return strings.TrimSpace(stmt[:i])
	}
	return strings.TrimSpace(stmt)
}

func (s *Store) nowRFC3339() string {
	return s.clock.Now().UTC().Format(time.RFC3339Nano)
}

type txCtxKey struct{}

// WithTx returns a context carrying tx so repository methods executed with it
// participate in the same transaction.
func WithTx(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// txFrom returns the executor for ctx: the carried transaction if present,
// otherwise the store's connection pool.
func (s *Store) txFrom(ctx context.Context) executor {
	if tx, ok := ctx.Value(txCtxKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return s.db
}

// InTx runs fn inside a read-write transaction. If fn returns an error the
// transaction is rolled back; otherwise it is committed. The request context
// flows to the driver so cancellation aborts blocking calls.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	txCtx := WithTx(ctx, tx)
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
			}
			return
		}
		if cerr := tx.Commit(); cerr != nil {
			err = fmt.Errorf("store: commit tx: %w", cerr)
		}
	}()
	return fn(txCtx)
}

// nextChangeVersion allocates a monotonic change version from the counter
// table. It must be called inside a transaction so the allocation and the row
// insert that uses it commit atomically.
func (s *Store) nextChangeVersion(ctx context.Context) (int, error) {
	ex := s.txFrom(ctx)
	var next int
	if err := ex.QueryRowContext(ctx, "SELECT next_version FROM change_counter WHERE id = 1").Scan(&next); err != nil {
		return 0, fmt.Errorf("store: read change counter: %w", err)
	}
	if _, err := ex.ExecContext(ctx, "UPDATE change_counter SET next_version = next_version + 1 WHERE id = 1"); err != nil {
		return 0, fmt.Errorf("store: bump change counter: %w", err)
	}
	return next, nil
}

// rowsAffected returns the count from a Result, surfacing driver errors.
func rowsAffected(res sql.Result) (int64, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: rows affected: %w", err)
	}
	return n, nil
}

// execVersioned runs an optimistic-lock UPDATE and returns a version-conflict
// error when zero rows matched. Every versioned write funnels through here so
// the WHERE-version guard and retryable error are applied uniformly.
func (s *Store) execVersioned(ctx context.Context, query, wrap string, args ...any) error {
	res, err := s.txFrom(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", wrap, err)
	}
	n, _ := rowsAffected(res)
	return checkOptimistic(n, wrap)
}

// dupInsert classifies a create result: a UNIQUE violation or zero rows becomes
// ErrAlreadyExists (retryable semantics for callers), any other driver error is
// wrapped with the entity kind. Centralising this keeps every insert idempotent
// path consistent.
func dupInsert(res sql.Result, err error, kind, ident string) error {
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("%w: %s %s: %v", errorsx.ErrAlreadyExists, kind, ident, err)
		}
		return fmt.Errorf("store: create %s: %w", kind, err)
	}
	n, _ := rowsAffected(res)
	if n == 0 {
		return fmt.Errorf("store: create %s: %w", kind, errorsx.ErrAlreadyExists)
	}
	return nil
}
