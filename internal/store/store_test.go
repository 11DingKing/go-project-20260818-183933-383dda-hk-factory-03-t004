package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

func newTestStore(t *testing.T) (*Store, *clock.Fake) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DataDir = dir
	cfg.Storage.DBFile = "test.db"
	cfg.Storage.MaxOpenConns = 1
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	st, err := New(context.Background(), cfg, clk)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrate(context.Background()))
	return st, clk
}

func seedRefs(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	_, err := st.CreatePerson(ctx, domain.Person{ID: "f", Code: "F", Name: "Eng", Role: "field_engineer"})
	require.NoError(t, err)
	_, err = st.CreateCustomer(ctx, domain.Customer{ID: "c", Code: "C", Name: "Cust", Workshop: "W", Status: "active"})
	require.NoError(t, err)
	_, err = st.CreatePerson(ctx, domain.Person{ID: "p", Code: "P", Name: "Resp", Role: "field_engineer"})
	require.NoError(t, err)
	_, err = st.CreateDevice(ctx, domain.Device{ID: "d", CustomerID: "c", Serial: "S", Name: "Dev", Status: "idle"})
	require.NoError(t, err)
}

func TestMigrateAndSchemaVersion(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	v, err := st.SchemaVersion(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, v)
	var name string
	err = st.DB().QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='operation_records'").Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "operation_records", name)
}

func TestEmbeddedSchemaMatchesMigrationFile(t *testing.T) {
	rootMigration := filepath.Join("..", "..", "migrations", "0001_init.sql")
	data, err := os.ReadFile(rootMigration)
	require.NoError(t, err)
	assert.Equal(t, string(data), schemaSQL, "embedded schema.sql drifted from migrations/0001_init.sql")
}

func TestTransactionCommit(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	cust := domain.Customer{ID: "c1", Code: "C1", Name: "Acme", Workshop: "W1", Status: "active"}
	err := st.InTx(ctx, func(ctx context.Context) error {
		_, err := st.CreateCustomer(ctx, cust)
		return err
	})
	require.NoError(t, err)
	got, err := st.GetCustomer(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, "Acme", got.Name)
}

func TestTransactionRollback(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	err := st.InTx(ctx, func(ctx context.Context) error {
		_, err := st.CreateCustomer(ctx, domain.Customer{ID: "c2", Code: "C2", Name: "Roll", Workshop: "W"})
		require.NoError(t, err)
		return errorsx.ErrValidation
	})
	assert.ErrorIs(t, err, errorsx.ErrValidation)
	_, err = st.GetCustomer(ctx, "c2")
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOptimisticLockVersionConflict(t *testing.T) {
	st, _ := newTestStore(t)
	seedRefs(t, st)
	ctx := context.Background()
	_, err := st.CreateOrder(ctx, domain.DeploymentOrder{
		ID: "o1", Code: "O1", CustomerID: "c", FieldEngineerID: "f", Status: domain.DeploymentDraft, BackfillWindowHours: 168,
	})
	require.NoError(t, err)
	err = st.UpdateStatus(ctx, "o1", domain.DeploymentDraft, domain.DeploymentProvisioning, 99)
	assert.ErrorIs(t, err, errorsx.ErrVersionConflict)
}

func TestRestartRecoverPersist(t *testing.T) {
	st, clk := newTestStore(t)
	ctx := context.Background()
	cust := domain.Customer{ID: "c-r", Code: "CR", Name: "Persist", Workshop: "W"}
	_, err := st.CreateCustomer(ctx, cust)
	require.NoError(t, err)
	clk.Advance(time.Hour)
	require.NoError(t, st.Close())

	dir := filepath.Dir(filepath.Join(st.cfg.DataDir, st.cfg.DBFile))
	cfg := config.Default()
	cfg.Storage.DataDir = dir
	cfg.Storage.DBFile = "test.db"
	cfg.Storage.MaxOpenConns = 1
	reopened, err := New(context.Background(), cfg, clock.NewFake(time.Now()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, reopened.Migrate(context.Background()))
	got, err := reopened.GetCustomer(ctx, "c-r")
	require.NoError(t, err)
	assert.Equal(t, "Persist", got.Name)
}

func TestChangeVersionMonotonic(t *testing.T) {
	st, _ := newTestStore(t)
	seedRefs(t, st)
	ctx := context.Background()
	_, err := st.CreateOrder(ctx, domain.DeploymentOrder{ID: "o-v", Code: "OV", CustomerID: "c", FieldEngineerID: "f", Status: domain.DeploymentDraft})
	require.NoError(t, err)
	recs := []domain.OperationRecord{
		{ID: "r1", OrderID: "o-v", DeviceID: "d", ResponsibleID: "p", ClientSequence: 1, Status: domain.RecordPending, BatchID: "b1"},
		{ID: "r2", OrderID: "o-v", DeviceID: "d", ResponsibleID: "p", ClientSequence: 2, Status: domain.RecordPending, BatchID: "b1"},
		{ID: "r3", OrderID: "o-v", DeviceID: "d", ResponsibleID: "p", ClientSequence: 3, Status: domain.RecordPending, BatchID: "b1"},
	}
	inserted, err := st.InsertBatch(ctx, recs)
	require.NoError(t, err)
	assert.Equal(t, 1, inserted[0].ChangeVersion)
	assert.Equal(t, 2, inserted[1].ChangeVersion)
	assert.Equal(t, 3, inserted[2].ChangeVersion)
}

func TestInsertBatchIdempotentDuplicate(t *testing.T) {
	st, _ := newTestStore(t)
	seedRefs(t, st)
	ctx := context.Background()
	_, err := st.CreateOrder(ctx, domain.DeploymentOrder{ID: "o-d", Code: "OD", CustomerID: "c", FieldEngineerID: "f", Status: domain.DeploymentDraft})
	require.NoError(t, err)
	rec := domain.OperationRecord{ID: "r1", OrderID: "o-d", DeviceID: "d", ResponsibleID: "p", ClientSequence: 5, Status: domain.RecordPending, BatchID: "b1"}
	first, err := st.InsertBatch(ctx, []domain.OperationRecord{rec})
	require.NoError(t, err)
	again, err := st.InsertBatch(ctx, []domain.OperationRecord{rec})
	require.NoError(t, err)
	assert.Equal(t, first[0].ID, again[0].ID)
	assert.Equal(t, first[0].ChangeVersion, again[0].ChangeVersion)
}
