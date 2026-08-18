package service

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// acceptedRecord seeds an order, replays one offline record and resolves it so the
// returned record is in the accepted state and its hours count towards a bill.
func acceptedRecord(t *testing.T, e *env, md masterData, orderID string, hours int64) domain.OperationRecord {
	t.Helper()
	now := e.clk.Now()
	_, err := e.services.Sync.Backfill(e.ctx(), orderID, "fe", []BackfillInput{
		{DeviceID: md.deviceIDs[0], ResponsibleID: md.engineerID, OccurredAt: now, ClientSequence: 1, Hours: decimal.NewFromInt(hours)},
	})
	require.NoError(t, err)
	batches, _, err := e.deps.Batches.ListAccumulatedBatches(e.ctx(), domain.SyncAccumulatedFilter{OrderID: orderID}, domain.Page{Size: 10})
	require.NoError(t, err)
	require.Len(t, batches, 1)
	_, _, err = e.services.Sync.ProcessBatch(e.ctx(), batches[0].ID, "w")
	require.NoError(t, err)
	recs, _, err := e.deps.Records.ListRecordsByFilter(e.ctx(), domain.RecordFilter{OrderID: orderID}, domain.Page{Size: 10})
	require.NoError(t, err)
	require.Len(t, recs, 1)
	return recs[0]
}

// TestGenerateBillRecomputesAcceptedHours verifies the period-1 bill created during
// provisioning is recomputed from accepted records with the given rate.
func TestGenerateBillRecomputesAcceptedHours(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	acceptedRecord(t, e, md, orderID, 3)

	rate := decimal.NewFromInt(150)
	bill, err := e.services.Reconciliation.GenerateBill(e.ctx(), orderID, 1, rate, "finance")
	require.NoError(t, err)
	assert.Equal(t, domain.BillDraft, bill.Status)

	total, err := decimal.NewFromString(bill.TotalHours.String())
	require.NoError(t, err)
	assert.True(t, total.Equal(decimal.NewFromInt(3)), "total hours = %s", bill.TotalHours)
	amount, err := decimal.NewFromString(bill.Amount.String())
	require.NoError(t, err)
	assert.True(t, amount.Equal(decimal.NewFromInt(450)), "amount = %s", bill.Amount)
}

// TestGenerateBillCreatesNewPeriodWhenMissing locks in the not-found fix: asking
// for a period that has no bill yet must create it instead of erroring.
func TestGenerateBillCreatesNewPeriodWhenMissing(t *testing.T) {
	e, _, orderID := seedAndProvision(t)

	bill, err := e.services.Reconciliation.GenerateBill(e.ctx(), orderID, 2, decimal.NewFromInt(100), "finance")
	require.NoError(t, err)
	assert.Equal(t, 2, bill.PeriodNo)
	assert.Equal(t, domain.BillDraft, bill.Status)

	loaded, err := e.deps.Bills.GetBillByOrderPeriod(e.ctx(), orderID, 2)
	require.NoError(t, err)
	assert.Equal(t, bill.ID, loaded.ID)
}

// TestIssueBillTransitionDraftToIssued exercises the optimistic bill-status transition.
func TestIssueBillTransitionDraftToIssued(t *testing.T) {
	e, _, orderID := seedAndProvision(t)
	bill, err := e.services.Reconciliation.GenerateBill(e.ctx(), orderID, 1, decimal.NewFromInt(100), "finance")
	require.NoError(t, err)
	assert.Equal(t, domain.BillDraft, bill.Status)

	issued, err := e.services.Reconciliation.IssueBill(e.ctx(), bill.ID, "finance")
	require.NoError(t, err)
	assert.Equal(t, domain.BillIssued, issued.Status)

	again, err := e.services.Reconciliation.IssueBill(e.ctx(), bill.ID, "finance")
	require.NoError(t, err, "re-issuing an issued bill is idempotent")
	assert.Equal(t, domain.BillIssued, again.Status)
}

// TestCorrectRecordAcceptedPromotesVerified locks in the corrected version-handling
// fix: correcting an accepted record reloads the version and promotes it to verified.
func TestCorrectRecordAcceptedPromotesVerified(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	rec := acceptedRecord(t, e, md, orderID, 2)
	assert.Equal(t, domain.RecordAccepted, rec.Status)

	corrected, err := e.services.Reconciliation.CorrectRecord(e.ctx(), rec.ID, decimal.NewFromInt(5), "staff")
	require.NoError(t, err)
	assert.Equal(t, domain.RecordVerified, corrected.Status)
	hours, err := decimal.NewFromString(corrected.Hours.String())
	require.NoError(t, err)
	assert.True(t, hours.Equal(decimal.NewFromInt(5)), "hours updated to %s", corrected.Hours)
	assert.Equal(t, rec.Version+2, corrected.Version, "correct + promote bumps version twice")
}

// TestCorrectRecordRevokedRejected ensures a revoked record cannot be corrected.
func TestCorrectRecordRevokedRejected(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	rec := acceptedRecord(t, e, md, orderID, 2)

	revoked, err := e.services.Reconciliation.RevokeRecord(e.ctx(), rec.ID, "duplicate", "staff")
	require.NoError(t, err)
	assert.Equal(t, domain.RecordRevoked, revoked.Status)

	_, err = e.services.Reconciliation.CorrectRecord(e.ctx(), rec.ID, decimal.NewFromInt(5), "staff")
	assert.ErrorIs(t, err, errorsx.ErrIllegalTransition)
}

// TestExportReturnsRows checks the finance export maps records to export rows.
func TestExportReturnsRows(t *testing.T) {
	e, md, orderID := seedAndProvision(t)
	acceptedRecord(t, e, md, orderID, 4)

	rows, total, err := e.services.Reconciliation.Export(e.ctx(), domain.RecordFilter{OrderID: orderID}, domain.Page{Size: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, orderID, rows[0].OrderID)
	assert.Equal(t, string(domain.RecordAccepted), rows[0].Status)
	assert.Equal(t, "4", rows[0].Hours)
	assert.NotEmpty(t, rows[0].OccurredAt)
}
