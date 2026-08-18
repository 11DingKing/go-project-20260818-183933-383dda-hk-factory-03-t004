package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/store"
)

type env struct {
	store    *store.Store
	deps     Deps
	services *Services
	clk      *clock.Fake
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DataDir = dir
	cfg.Storage.DBFile = "svc.db"
	cfg.Storage.MaxOpenConns = 1
	cfg.Scheduler.PollInterval = 50 * time.Millisecond
	cfg.Scheduler.EscalatorInterval = 50 * time.Millisecond
	cfg.Scheduler.MaxRetries = 3
	cfg.Scheduler.BaseBackoff = 10 * time.Millisecond
	cfg.Scheduler.MaxBackoff = 100 * time.Millisecond
	cfg.Scheduler.LeaseTTL = 200 * time.Millisecond
	cfg.Trial.DefaultWindowHours = 24 * 30
	cfg.Trial.AcceptanceWindowHours = 24 * 7
	cfg.Backfill.WindowHours = 168
	clk := clock.NewFake(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	st, err := store.New(context.Background(), cfg, clk)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrate(context.Background()))
	deps := Deps{
		UOW: st, Clock: st.Clock(), Logger: nil, Cfg: cfg,
		Persons: st, Customers: st, Devices: st,
		Orders: st, Steps: st, OrderDevices: st,
		Trials: st, Inspections: st,
		Records: st, WorkHours: st, Adjudications: st,
		Bills: st, Batches: st, Manuals: st, SyncState: st,
		Audit: st, Failures: st,
	}
	return &env{store: st, deps: deps, services: New(deps), clk: clk}
}

// masterData holds the seeded references tests build on.
type masterData struct {
	customerID    string
	engineerID    string
	managerID     string
	adjudicatorID string
	deviceIDs     []string
}

func seedMaster(t *testing.T, e *env) masterData {
	t.Helper()
	ctx := context.Background()
	c, err := e.services.Registry.CreateCustomer(ctx, "ops", CreateCustomerRequest{Code: "CUST-1", Name: "Acme Robotics", Workshop: "Hall-A"})
	require.NoError(t, err)
	eng, err := e.services.Registry.CreatePerson(ctx, "ops", CreatePersonRequest{Code: "ENG-1", Name: "Liu", Role: "field_engineer"})
	require.NoError(t, err)
	mgr, err := e.services.Registry.CreatePerson(ctx, "ops", CreatePersonRequest{Code: "MGR-1", Name: "Chen", Role: "customer_manager"})
	require.NoError(t, err)
	adj, err := e.services.Registry.CreatePerson(ctx, "ops", CreatePersonRequest{Code: "ADJ-1", Name: "Zhao", Role: "adjudicator"})
	require.NoError(t, err)
	var devIDs []string
	for i := 0; i < 2; i++ {
		d, err := e.services.Registry.CreateDevice(ctx, "ops", CreateDeviceRequest{
			CustomerID: c.ID, Serial: "ROBOT-" + string(rune('A'+i)), Name: "Robot", Model: "X1",
		})
		require.NoError(t, err)
		devIDs = append(devIDs, d.ID)
	}
	return masterData{customerID: c.ID, engineerID: eng.ID, managerID: mgr.ID, adjudicatorID: adj.ID, deviceIDs: devIDs}
}

func (e *env) ctx() context.Context { return context.Background() }

func (e *env) createOrder(t *testing.T, md masterData) string {
	t.Helper()
	order, err := e.services.Deployment.CreateOrder(e.ctx(), "ops", CreateOrderRequest{
		Code: "DEP-1", CustomerID: md.customerID, FieldEngineerID: md.engineerID, DeviceIDs: md.deviceIDs,
	})
	require.NoError(t, err)
	return order.ID
}
