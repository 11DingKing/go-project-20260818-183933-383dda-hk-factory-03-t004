package reconcile

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func d(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }

func in(dev, date, hrs string) Input {
	return Input{DeviceID: dev, Date: date, Hours: d(hrs)}
}

func TestDiffMatchedIsZeroDelta(t *testing.T) {
	res := Engine{}.Diff(
		[]Input{in("D1", "2026-03-01", "8")},
		[]Input{in("D1", "2026-03-01", "8")},
	)
	require.Len(t, res.Pairs, 1)
	assert.Equal(t, KindMatched, res.Pairs[0].Kind)
	assert.True(t, res.Pairs[0].Delta.IsZero())
	assert.Equal(t, 1, res.Summary.Matched)
	assert.False(t, res.Summary.HasConflict)
	assert.True(t, res.Summary.TotalDelta.IsZero())
}

func TestDiffOverReportedConflict(t *testing.T) {
	res := Engine{}.Diff(
		[]Input{in("D1", "2026-03-01", "7")},
		[]Input{in("D1", "2026-03-01", "9")},
	)
	require.Len(t, res.Pairs, 1)
	assert.Equal(t, KindOverReported, res.Pairs[0].Kind)
	assert.True(t, res.Pairs[0].Delta.Equal(d("2")))
	assert.True(t, res.Summary.HasConflict)
	assert.Equal(t, 1, res.Summary.OverReported)
}

func TestDiffUnderReportedConflict(t *testing.T) {
	res := Engine{}.Diff(
		[]Input{in("D1", "2026-03-01", "8")},
		[]Input{in("D1", "2026-03-01", "5")},
	)
	require.Len(t, res.Pairs, 1)
	assert.Equal(t, KindUnderReported, res.Pairs[0].Kind)
	assert.True(t, res.Pairs[0].Delta.Equal(d("-3")))
	assert.True(t, res.Summary.HasConflict)
	assert.Equal(t, 1, res.Summary.UnderReported)
}

func TestDiffSystemOnlyCustomerMissing(t *testing.T) {
	res := Engine{}.Diff(
		[]Input{in("D1", "2026-03-01", "8")},
		nil,
	)
	require.Len(t, res.Pairs, 1)
	assert.Equal(t, KindSystemOnly, res.Pairs[0].Kind)
	assert.True(t, res.Pairs[0].Delta.Equal(d("-8")))
	assert.Equal(t, 1, res.Summary.SystemOnly)
}

func TestDiffCustomerOnlySystemMissing(t *testing.T) {
	res := Engine{}.Diff(
		nil,
		[]Input{in("D2", "2026-03-02", "6")},
	)
	require.Len(t, res.Pairs, 1)
	assert.Equal(t, KindCustomerOnly, res.Pairs[0].Kind)
	assert.True(t, res.Pairs[0].Delta.Equal(d("6")))
	assert.Equal(t, 1, res.Summary.CustomerOnly)
}

func TestDiffDuplicateKeysAggregatedToDayTotals(t *testing.T) {
	// Two system records on the same device-day must fold into one slot so the
	// diff compares day totals, not individual rows.
	res := Engine{}.Diff(
		[]Input{in("D1", "2026-03-01", "3"), in("D1", "2026-03-01", "5")},
		[]Input{in("D1", "2026-03-01", "8")},
	)
	require.Len(t, res.Pairs, 1)
	assert.Equal(t, KindMatched, res.Pairs[0].Kind)
	assert.True(t, res.Pairs[0].SystemHours.Equal(d("8")))
}

func TestDiffDecimalPrecisionAndTotalDelta(t *testing.T) {
	res := Engine{}.Diff(
		[]Input{in("D1", "2026-03-01", "7.50"), in("D2", "2026-03-01", "8.25")},
		[]Input{in("D1", "2026-03-01", "9.00"), in("D2", "2026-03-01", "8.25")},
	)
	// D1 over by 1.50, D2 matched: net delta 1.50, one conflict.
	assert.True(t, res.Summary.TotalDelta.Equal(d("1.5")))
	assert.Equal(t, 1, res.Summary.OverReported)
	assert.Equal(t, 1, res.Summary.Matched)
	assert.True(t, res.Summary.HasConflict)
}

func TestDiffEmptyInputsReturnsEmpty(t *testing.T) {
	res := Engine{}.Diff(nil, nil)
	assert.Empty(t, res.Pairs)
	assert.True(t, res.Summary.TotalDelta.IsZero())
}

func TestDiffOrderingDeterministicByDateThenDevice(t *testing.T) {
	res := Engine{}.Diff(
		[]Input{in("D2", "2026-03-02", "1"), in("D1", "2026-03-01", "1"), in("D1", "2026-03-02", "1")},
		nil,
	)
	require.Len(t, res.Pairs, 3)
	// Expect ascending date, then device within a date.
	assert.Equal(t, "2026-03-01", res.Pairs[0].Date)
	assert.Equal(t, "D1", res.Pairs[0].DeviceID)
	assert.Equal(t, "2026-03-02", res.Pairs[1].Date)
	assert.Equal(t, "D1", res.Pairs[1].DeviceID)
	assert.Equal(t, "2026-03-02", res.Pairs[2].Date)
	assert.Equal(t, "D2", res.Pairs[2].DeviceID)
}
