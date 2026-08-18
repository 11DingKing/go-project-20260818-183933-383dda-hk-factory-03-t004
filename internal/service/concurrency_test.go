package service

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// runConcurrent launches n goroutines at the same instant and waits for all to
// finish, collecting their results so the assertions can reason about the fan-out.
func runConcurrent(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			<-start
			errs[idx] = fn(idx)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func seedAndProvision(t *testing.T) (*env, masterData, string) {
	t.Helper()
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	return e, md, orderID
}

// TestConcurrentLeaseAcquisitionRace proves the sync-batch lease is exclusive:
// among many workers only one may process a batch, the rest receive ErrLeaseHeld,
// and the records are resolved exactly once.
func TestConcurrentLeaseAcquisitionRace(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	now := e.clk.Now()
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "fe", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-time.Hour), ClientSequence: 1, Hours: decimal.NewFromInt(3)},
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-30 * time.Minute), ClientSequence: 2, Hours: decimal.NewFromInt(1)},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.BatchID)

	const workers = 8
	errs := runConcurrent(workers, func(i int) error {
		_, _, perr := e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "worker")
		return perr
	})

	var success, leaseHeld int
	for _, perr := range errs {
		switch {
		case perr == nil:
			success++
		case errors.Is(perr, errorsx.ErrLeaseHeld):
			leaseHeld++
		default:
			t.Fatalf("unexpected error: %v", perr)
		}
	}
	assert.Equal(t, 1, success, "exactly one worker must win the lease")
	assert.Equal(t, workers-1, leaseHeld, "the rest must be told the lease is held")

	batch, err := e.deps.Batches.GetSyncBatch(e.ctx(), res.BatchID)
	require.NoError(t, err)
	assert.Equal(t, domain.SyncBatchCompleted, batch.Status)
	assert.Equal(t, 2, batch.ProcessedCount)

	records, err := e.deps.Records.ListRecordsByBatch(e.ctx(), res.BatchID)
	require.NoError(t, err)
	for _, r := range records {
		assert.Equal(t, domain.RecordAccepted, r.Status, "record %s must be accepted exactly once", r.ID)
	}
}

// TestParallelProvisioningNoInterference runs several independent provisioning
// sagas concurrently and verifies none steps on another's data.
func TestParallelProvisioningNoInterference(t *testing.T) {
	e, md, _ := seedAndProvision(t)
	const parallel = 5
	errs := runConcurrent(parallel, func(i int) error {
		order, perr := e.services.Deployment.CreateOrder(e.ctx(), "ops", CreateOrderRequest{
			Code: fmt.Sprintf("DEP-P%d", i), CustomerID: md.customerID, FieldEngineerID: md.engineerID, DeviceIDs: md.deviceIDs,
		})
		if perr != nil {
			return perr
		}
		_, _, perr = e.services.Deployment.Provision(e.ctx(), order.ID, "ops")
		return perr
	})
	for _, perr := range errs {
		require.NoError(t, perr)
	}
	orders, err := e.deps.Orders.ListByStatus(e.ctx(), domain.DeploymentActive)
	require.NoError(t, err)
	assert.Equal(t, parallel+1, len(orders), "seeded order plus parallel orders are active")
	for _, o := range orders {
		assert.Equal(t, domain.HandlingOnSiteDebug, o.HandlingMode)
		steps, _ := e.deps.Steps.ListStepsByOrder(e.ctx(), o.ID)
		assert.Equal(t, len(domain.OrderedSteps), len(steps), "every order has all five steps")
		for _, st := range steps {
			assert.Equal(t, domain.StepDone, st.Status, "step %s done", st.StepName)
		}
	}
}

// TestConcurrentBackfillIdempotentDuplicate replays the same offline sequence from
// many goroutines; only one batch is ever created and every caller sees the same record.
func TestConcurrentBackfillIdempotentDuplicate(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	now := e.clk.Now()
	in := []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-time.Hour), ClientSequence: 7, Hours: decimal.NewFromInt(2)},
	}
	const callers = 6
	errs := runConcurrent(callers, func(i int) error {
		_, berr := e.services.Sync.Backfill(e.ctx(), orderID, "fe", in)
		return berr
	})
	for _, berr := range errs {
		require.NoError(t, berr, "idempotent replay never errors")
	}
	recs, _, err := e.deps.Records.ListRecordsByFilter(e.ctx(), domain.RecordFilter{OrderID: orderID, Source: domain.SourceBackfill}, domain.Page{Size: 100})
	require.NoError(t, err)
	require.Len(t, recs, 1, "the sequence is stored exactly once")
	assert.Equal(t, 7, recs[0].ClientSequence)
}

// TestConcurrentOptimisticLockConflictRace has many goroutines transition the same
// accepted record to verified using the same captured version; the optimistic lock
// admits exactly one and rejects the rest with ErrVersionConflict.
func TestConcurrentOptimisticLockConflictRace(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	now := e.clk.Now()
	_, err := e.services.Sync.Backfill(e.ctx(), orderID, "fe", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-time.Hour), ClientSequence: 1, Hours: decimal.NewFromInt(2)},
	})
	require.NoError(t, err)
	batches, _, err := e.deps.Batches.ListAccumulatedBatches(e.ctx(), domain.SyncAccumulatedFilter{OrderID: orderID}, domain.Page{Size: 10})
	require.NoError(t, err)
	require.Len(t, batches, 1)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), batches[0].ID, "w")
	require.NoError(t, err)
	recs, _, err := e.deps.Records.ListRecordsByFilter(e.ctx(), domain.RecordFilter{OrderID: orderID}, domain.Page{Size: 10})
	require.NoError(t, err)
	require.NotEmpty(t, recs)
	recordID := recs[0].ID
	staleVersion := recs[0].Version

	const contenders = 8
	errs := runConcurrent(contenders, func(i int) error {
		return e.deps.Records.UpdateRecordStatus(e.ctx(), recordID, domain.RecordAccepted, domain.RecordVerified, staleVersion)
	})
	var success, conflict int
	for _, cerr := range errs {
		switch {
		case cerr == nil:
			success++
		case errors.Is(cerr, errorsx.ErrVersionConflict):
			conflict++
		default:
			t.Fatalf("unexpected error: %v", cerr)
		}
	}
	assert.Equal(t, 1, success, "exactly one transition wins the optimistic lock")
	assert.Equal(t, contenders-1, conflict)
	final, err := e.deps.Records.GetRecord(e.ctx(), recordID)
	require.NoError(t, err)
	assert.Equal(t, domain.RecordVerified, final.Status)
	assert.Equal(t, staleVersion+1, final.Version)
}

// TestConcurrentCheckDeadlinesIdempotentRetry escalates an overdue trial from many
// goroutines at once and guarantees the responsibility transfers only once.
func TestConcurrentCheckDeadlinesIdempotentRetry(t *testing.T) {
	e, _, orderID := seedAndProvision(t)
	trial := trialOf(t, e, orderID)
	e.clk.Advance(time.Duration(e.deps.Cfg.Trial.AcceptanceWindowHours+1) * time.Hour)

	const callers = 6
	var total int64
	errs := runConcurrent(callers, func(i int) error {
		n, derr := e.services.Trial.CheckDeadlines(e.ctx())
		atomic.AddInt64(&total, int64(n))
		return derr
	})
	for _, derr := range errs {
		require.NoError(t, derr)
	}
	assert.Equal(t, int64(1), total, "the trial is escalated exactly once across concurrent checks")
	final, err := e.deps.Trials.GetTrial(e.ctx(), trial.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TrialEscalated, final.Status)
	order, err := e.deps.Orders.GetOrder(e.ctx(), orderID)
	require.NoError(t, err)
	assert.Equal(t, domain.ResponsibleCustomerMgr, order.ResponsibleRole, "responsibility transferred to customer manager")
	assert.Equal(t, domain.HandlingUrgeAccept, order.HandlingMode)
}
