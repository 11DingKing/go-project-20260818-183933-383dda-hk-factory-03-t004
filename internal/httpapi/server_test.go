package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"sitesync/internal/clock"
	"sitesync/internal/config"
	"sitesync/internal/service"
	"sitesync/internal/store"
)

func newTestServer(t *testing.T) (*Server, *clock.Fake) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.DataDir = dir
	cfg.Storage.DBFile = "httpapi_test.db"
	cfg.Storage.MaxOpenConns = 1
	cfg.Backfill.WindowHours = 168
	cfg.Backfill.ManualReviewAfterHours = 336
	clk := clock.NewFake(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC))
	st, err := store.New(context.Background(), cfg, clk)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrate(context.Background()))
	deps := service.Deps{
		UOW: st, Clock: st.Clock(), Logger: zap.NewNop(), Cfg: cfg,
		Persons: st, Customers: st, Devices: st,
		Orders: st, Steps: st, OrderDevices: st,
		Trials: st, Inspections: st,
		Records: st, WorkHours: st, Adjudications: st,
		Bills: st, Batches: st, Manuals: st, SyncState: st,
		Audit: st, Failures: st,
	}
	svc := service.New(deps)
	probes := Probes{
		DBPing:         func(ctx context.Context) error { return st.Ping(ctx) },
		DataDir:        cfg.Storage.DataDir,
		SchedulerReady: func() bool { return true },
		SchemaVersion:  func(ctx context.Context) (int, error) { return st.SchemaVersion(ctx) },
	}
	srv := New(svc, cfg, probes, zap.NewNop(), clk)
	return srv, clk
}

// TestRequestContextPropagatedToStore proves the request context flows from the
// HTTP layer through the service down to the storage driver: the same create
// request with a live context succeeds, but with an already-cancelled context
// it surfaces as an error because the driver aborts the write.
func TestRequestContextPropagatedToStore(t *testing.T) {
	srv, _ := newTestServer(t)
	body := `{"code":"CUST-1","name":"Acme","workshop":"Hall-A"}`

	// Cancelled context: the driver must observe the cancellation and fail.
	req := httptest.NewRequest(http.MethodPost, "/api/customers", strings.NewReader(body))
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	assert.GreaterOrEqual(t, rr.Code, http.StatusBadRequest)
	assert.NotEqual(t, http.StatusCreated, rr.Code, "cancelled ctx must not create the entity")

	// Live context: the same request succeeds, confirming the only difference
	// was the propagated context.
	rr2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/customers", strings.NewReader(body)))
	assert.Equal(t, http.StatusCreated, rr2.Code)
}

// TestHTTPErrorMappingValidationAndNotFound exercises the error-chain mapping
// at the wire: a malformed payload maps to 400 and a missing entity maps to 404.
func TestHTTPErrorMappingValidationAndNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/customers", strings.NewReader(`{}`)))
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	rr2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/deployments/does-not-exist", nil))
	assert.Equal(t, http.StatusNotFound, rr2.Code)
}

// TestIllegalTransitionRejectedByHTTP drives the deployment state machine over
// HTTP: compensating a draft order is an illegal transition and must map to 422.
func TestIllegalTransitionRejectedByHTTP(t *testing.T) {
	srv, _ := newTestServer(t)
	orderID := seedOrder(t, srv)

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/deployments/"+orderID+"/compensate", nil))
	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
}

// TestNewSyncRoutesWired reaches the window and reconciliation surfaces added
// this round over HTTP: the stale-manual escalation list and the reconciliation
// diff both answer 200 for a freshly seeded order.
func TestNewSyncRoutesWired(t *testing.T) {
	srv, _ := newTestServer(t)
	orderID := seedOrder(t, srv)

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/manual/stale", nil))
	assert.Equal(t, http.StatusOK, rr.Code)

	diffBody := `{"order_id":"` + orderID + `"}`
	rr2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/reconciliation/diff", strings.NewReader(diffBody)))
	assert.Equal(t, http.StatusOK, rr2.Code)
}

// TestHealthAndReadyProbes checks the liveness and readiness endpoints.
func TestHealthAndReadyProbes(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equalf(t, http.StatusOK, rr.Code, "%s not ok", path)
	}
}

// seedOrder creates the master data and a draft deployment order through the
// services so HTTP tests have a real entity to target.
func seedOrder(t *testing.T, srv *Server) string {
	t.Helper()
	ctx := context.Background()
	cust, err := srv.services.Registry.CreateCustomer(ctx, "ops", service.CreateCustomerRequest{Code: "C1", Name: "Acme", Workshop: "Hall-A"})
	require.NoError(t, err)
	eng, err := srv.services.Registry.CreatePerson(ctx, "ops", service.CreatePersonRequest{Code: "E1", Name: "Liu", Role: "field_engineer"})
	require.NoError(t, err)
	dev, err := srv.services.Registry.CreateDevice(ctx, "ops", service.CreateDeviceRequest{CustomerID: cust.ID, Serial: "S1", Name: "Robot", Model: "X1"})
	require.NoError(t, err)
	order, err := srv.services.Deployment.CreateOrder(ctx, "ops", service.CreateOrderRequest{
		Code: "O1", CustomerID: cust.ID, FieldEngineerID: eng.ID, DeviceIDs: []string{dev.ID},
	})
	require.NoError(t, err)
	return order.ID
}
