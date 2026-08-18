package service

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/domain"
)

func provisionedOrder(t *testing.T, e *env, md masterData) string {
	t.Helper()
	orderID := e.createOrder(t, md)
	_, _, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	return orderID
}

func TestBackfillReplaysInOrderAndStaysPending(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	now := e.clk.Now()

	inputs := []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-3 * time.Hour), ClientSequence: 3, Hours: decimal.NewFromInt(2)},
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-48 * time.Hour), ClientSequence: 1, Hours: decimal.NewFromInt(4)},
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-24 * time.Hour), ClientSequence: 2, Hours: decimal.NewFromInt(3)},
	}
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "field_eng", inputs)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Pending)
	require.Len(t, res.Records, 3)
	// Records must be stored in chronological order with ascending change versions.
	assert.Equal(t, 1, res.Records[0].ClientSequence)
	assert.Equal(t, 2, res.Records[1].ClientSequence)
	assert.Equal(t, 3, res.Records[2].ClientSequence)
	assert.Less(t, res.Records[0].ChangeVersion, res.Records[1].ChangeVersion)
	for _, r := range res.Records {
		assert.Equal(t, domain.RecordPending, r.Status)
		assert.Equal(t, domain.SourceBackfill, r.Source)
	}
}

func TestBackfillIdempotentDuplicateSequence(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	now := e.clk.Now()
	in := []BackfillInput{{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-1 * time.Hour), ClientSequence: 1, Hours: decimal.NewFromInt(2)}}
	first, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", in)
	require.NoError(t, err)
	again, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", in)
	require.NoError(t, err)
	assert.Equal(t, first.BatchID, again.BatchID, "duplicate backfill of same sequence must not create a new batch")
	assert.Equal(t, first.Records[0].ID, again.Records[0].ID)
}

func TestProcessBatchResolvesConflict(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	now := e.clk.Now()
	day := now.Add(-12 * time.Hour)
	// Customer reported 8 hours for that device/day.
	_, err := e.deps.WorkHours.UpsertWorkHour(e.ctx(), domain.CustomerWorkHour{
		ID: "wh1", DeviceID: md.deviceIDs[0], WorkDate: day.UTC().Format("2006-01-02"),
		Hours: decimal.NewFromInt(8), ReportedBy: md.managerID,
	})
	require.NoError(t, err)

	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: day, ClientSequence: 1, Hours: decimal.NewFromInt(5)},
	})
	require.NoError(t, err)
	batch, processed, err := e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)
	assert.Equal(t, 1, processed)
	assert.Equal(t, domain.SyncBatchCompleted, batch.Status)
	rec, err := e.deps.Records.GetRecord(e.ctx(), res.Records[0].ID)
	require.NoError(t, err)
	assert.Equal(t, domain.RecordConflict, rec.Status)
	assert.Equal(t, "wh1", rec.ConflictID)
}

func TestProcessBatchAcceptsNonConflicting(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	now := e.clk.Now()
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-1 * time.Hour), ClientSequence: 1, Hours: decimal.NewFromInt(3)},
	})
	require.NoError(t, err)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)
	rec, _ := e.deps.Records.GetRecord(e.ctx(), res.Records[0].ID)
	assert.Equal(t, domain.RecordAccepted, rec.Status)
}

func TestProcessBatchWindowExpiredRoutesToManual(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	// Record older than the 168h backfill window.
	old := e.clk.Now().Add(-400 * time.Hour)
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: old, ClientSequence: 1, Hours: decimal.NewFromInt(3)},
	})
	require.NoError(t, err)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)
	rec, _ := e.deps.Records.GetRecord(e.ctx(), res.Records[0].ID)
	assert.Equal(t, domain.RecordManualVerifyNeeded, rec.Status)
	manual, err := e.deps.Manuals.GetManualByRecord(e.ctx(), rec.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.ManualPending, manual.Status)
}

func TestPullChangesIncrementalCursor(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	now := e.clk.Now()
	_, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-1 * time.Hour), ClientSequence: 1, Hours: decimal.NewFromInt(1)},
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now.Add(-2 * time.Hour), ClientSequence: 2, Hours: decimal.NewFromInt(1)},
	})
	require.NoError(t, err)
	first, err := e.services.Sync.PullChanges(e.ctx(), 0, 10)
	require.NoError(t, err)
	assert.Len(t, first, 2)
	since := first[len(first)-1].ChangeVersion
	second, err := e.services.Sync.PullChanges(e.ctx(), since, 10)
	require.NoError(t, err)
	assert.Empty(t, second, "no changes beyond the cursor")
}

func TestManualVerifyIdempotentAndRepeatable(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	old := e.clk.Now().Add(-400 * time.Hour)
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: old, ClientSequence: 1, Hours: decimal.NewFromInt(3)},
	})
	require.NoError(t, err)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)
	recID := res.Records[0].ID

	manual1, rec1, err := e.services.Sync.ManualVerify(e.ctx(), recID, md.managerID, domain.DecisionAccept, "ok")
	require.NoError(t, err)
	assert.Equal(t, domain.ManualReviewed, manual1.Status)
	assert.Equal(t, domain.RecordVerified, rec1.Status)

	manual2, rec2, err := e.services.Sync.ManualVerify(e.ctx(), recID, md.managerID, domain.DecisionAccept, "ok")
	require.NoError(t, err)
	assert.Equal(t, manual1.ID, manual2.ID)
	assert.Equal(t, domain.RecordVerified, rec2.Status)
}

func TestAdjudicateConflictAndDelta(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	now := e.clk.Now()
	day := now.Add(-12 * time.Hour)
	_, err := e.deps.WorkHours.UpsertWorkHour(e.ctx(), domain.CustomerWorkHour{
		ID: "wh2", DeviceID: md.deviceIDs[0], WorkDate: day.UTC().Format("2006-01-02"),
		Hours: decimal.NewFromInt(8), ReportedBy: md.managerID,
	})
	require.NoError(t, err)
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: day, ClientSequence: 1, Hours: decimal.NewFromInt(5)},
	})
	require.NoError(t, err)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)

	adj, err := e.services.Adjudication.Adjudicate(e.ctx(), AdjudicateRequest{
		RecordID: res.Records[0].ID, Winner: domain.WinnerCustomer, AdjudicatorID: md.adjudicatorID, Reason: "customer log authoritative",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.WinnerCustomer, adj.Winner)
	assert.True(t, adj.DeltaHours.Equal(decimal.NewFromInt(3)))
	assert.Equal(t, domain.WinnerField, adj.AttributedTo)
	rec, _ := e.deps.Records.GetRecord(e.ctx(), res.Records[0].ID)
	assert.Equal(t, domain.RecordAdjudicated, rec.Status)

	// Idempotent: re-adjudicating returns the existing ruling.
	again, err := e.services.Adjudication.Adjudicate(e.ctx(), AdjudicateRequest{RecordID: res.Records[0].ID, Winner: domain.WinnerField, AdjudicatorID: md.adjudicatorID})
	require.NoError(t, err)
	assert.Equal(t, adj.ID, again.ID)
}

var _ = context.Background
