package service

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/domain"
	"sitesync/internal/reconcile"
)

// TestComputeDiffSurfacesOverReportedConflict wires the new reconciliation diff
// through the service: an accepted system record is compared against a
// customer-reported work hour for the same device-day, and the larger customer
// figure must surface as an over-reported conflict requiring adjudication.
func TestComputeDiffSurfacesOverReportedConflict(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	day := e.clk.Now().Add(-12 * time.Hour)
	date := day.UTC().Format("2006-01-02")

	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: day, ClientSequence: 1, Hours: decimal.NewFromInt(5)},
	})
	require.NoError(t, err)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)
	rec, _ := e.deps.Records.GetRecord(e.ctx(), res.Records[0].ID)
	require.Equal(t, domain.RecordAccepted, rec.Status, "record must be accepted before it counts on the system side")

	// Customer reports 8h for the same device-day: 3h more than the system.
	_, err = e.deps.WorkHours.UpsertWorkHour(e.ctx(), domain.CustomerWorkHour{
		ID: "wh-diff", DeviceID: md.deviceIDs[0], WorkDate: date, Hours: decimal.NewFromInt(8), ReportedBy: md.managerID,
	})
	require.NoError(t, err)

	diff, err := e.services.Reconciliation.ComputeDiff(e.ctx(), orderID, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Len(t, diff.Pairs, 1)
	assert.Equal(t, reconcile.KindOverReported, diff.Pairs[0].Kind)
	assert.True(t, diff.Pairs[0].Delta.Equal(decimal.NewFromInt(3)))
	assert.True(t, diff.Summary.HasConflict)
	assert.Equal(t, 1, diff.Summary.OverReported)
}

// TestManualStaleEscalationAfterWindowExpired exercises the manual-review
// escalation path: a record past its backfill window is routed to the manual
// channel, then once the review window closes the escalation listing surfaces
// it. The clock is injected so no real waiting is needed.
func TestManualStaleEscalationAfterWindowExpired(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)

	old := e.clk.Now().Add(-400 * time.Hour) // older than the 168h window
	res, err := e.services.Sync.Backfill(e.ctx(), orderID, "eng", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: old, ClientSequence: 1, Hours: decimal.NewFromInt(4)},
	})
	require.NoError(t, err)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), res.BatchID, "poller")
	require.NoError(t, err)
	rec, _ := e.deps.Records.GetRecord(e.ctx(), res.Records[0].ID)
	require.Equal(t, domain.RecordManualVerifyNeeded, rec.Status, "expired-window record must route to manual review")

	// Freshly created manual verification is within its review window: not stale.
	fresh, err := e.services.Query.ListStaleManuals(e.ctx(), 0, domain.Page{Size: domain.DefaultPageSize})
	require.NoError(t, err)
	assert.Equal(t, int64(0), fresh.Total, "manual review is fresh immediately after creation")

	// Advance the injected clock past the configured review window.
	e.clk.Advance(time.Duration(e.deps.Cfg.Backfill.ManualReviewAfterHours)*time.Hour + time.Hour)
	stale, err := e.services.Query.ListStaleManuals(e.ctx(), 0, domain.Page{Size: domain.DefaultPageSize})
	require.NoError(t, err)
	assert.Equal(t, int64(1), stale.Total, "expired manual review must surface for escalation")
	require.Len(t, stale.Items, 1)
	assert.Equal(t, rec.ID, stale.Items[0].RecordID)
}
