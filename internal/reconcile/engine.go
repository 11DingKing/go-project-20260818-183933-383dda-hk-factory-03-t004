// Package reconcile computes the diff between system-side operation records and
// customer-reported work hours for a deployment period. The engine is pure: it
// takes normalized inputs and returns a structured diff with decimal-precise
// deltas and a conflict classification, so it can be unit-tested without a
// database and reused by the reconciliation service and the export pipeline.
package reconcile

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Input is one side of a reconciliation pair, normalized to the (device, date)
// key and an hours total. The service layer assembles these from operation
// records (system side) and customer work-hour rows (customer side).
type Input struct {
	DeviceID string
	Date     string // canonical "2006-01-02"
	Hours    decimal.Decimal
}

// PairKind classifies how the system and customer figures for one (device,
// date) key relate. Each kind maps to a distinct downstream action.
type PairKind string

const (
	// KindMatched means both sides agree exactly.
	KindMatched PairKind = "matched"
	// KindOverReported means the customer reported more hours than the system
	// recorded; this is a conflict that must be adjudicated.
	KindOverReported PairKind = "over_reported"
	// KindUnderReported means the system recorded more than the customer
	// reported; the customer's return is incomplete for that day.
	KindUnderReported PairKind = "under_reported"
	// KindSystemOnly means the system has a record the customer never reported.
	KindSystemOnly PairKind = "system_only"
	// KindCustomerOnly means the customer reported hours for a day the system
	// has no record of, i.e. a possible missing registration.
	KindCustomerOnly PairKind = "customer_only"
)

// Pair is one reconciled (device, date) slot with both figures, the delta
// (customer - system) and the classification.
type Pair struct {
	DeviceID      string
	Date          string
	SystemHours   decimal.Decimal
	CustomerHours decimal.Decimal
	Delta         decimal.Decimal
	Kind          PairKind
}

// Summary aggregates the pair counts and the net delta across the whole diff.
type Summary struct {
	Matched       int             `json:"matched"`
	OverReported  int             `json:"over_reported"`
	UnderReported int             `json:"under_reported"`
	SystemOnly    int             `json:"system_only"`
	CustomerOnly  int             `json:"customer_only"`
	TotalDelta    decimal.Decimal `json:"total_delta"`
	HasConflict   bool            `json:"has_conflict"`
}

// Diff is the full result of comparing the two sides.
type Diff struct {
	Pairs   []Pair  `json:"pairs"`
	Summary Summary `json:"summary"`
}

// slotKey is the (device, date) identity of one reconciled slot.
type slotKey struct{ device, date string }

// Engine computes the reconciliation diff. It is stateless; the zero value is
// ready to use.
type Engine struct{}

// aggregate sums the hours of each input by its (device, date) key. Duplicate
// keys (several records on the same device-day) are folded into one slot so the
// diff compares day totals, not individual rows. The bool reports presence.
func aggregate(in []Input) (map[slotKey]decimal.Decimal, map[slotKey]struct{}) {
	totals := make(map[slotKey]decimal.Decimal, len(in))
	present := make(map[slotKey]struct{}, len(in))
	for _, e := range in {
		k := slotKey{device: e.DeviceID, date: e.Date}
		totals[k] = totals[k].Add(e.Hours)
		present[k] = struct{}{}
	}
	return totals, present
}

// Diff compares the system and customer sides, producing one Pair per distinct
// (device, date) key present on either side. Pairs are sorted by date then
// device for a stable, replayable order. The delta is customer minus system;
// positive means the customer over-reported, negative means under-reported.
func (Engine) Diff(system, customer []Input) Diff {
	sys, sysPresent := aggregate(system)
	cust, custPresent := aggregate(customer)
	keys := make(map[slotKey]struct{}, len(sysPresent)+len(custPresent))
	for k := range sysPresent {
		keys[k] = struct{}{}
	}
	for k := range custPresent {
		keys[k] = struct{}{}
	}
	list := make([]slotKey, 0, len(keys))
	for k := range keys {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].date != list[j].date {
			return list[i].date < list[j].date
		}
		return list[i].device < list[j].device
	})
	pairs := make([]Pair, 0, len(list))
	var sum Summary
	for _, k := range list {
		s, sysOK := sys[k]
		c, custOK := cust[k]
		p := Pair{DeviceID: k.device, Date: k.date, SystemHours: s, CustomerHours: c}
		switch {
		case !sysOK && custOK:
			p.Kind = KindCustomerOnly
			p.Delta = c
			sum.CustomerOnly++
		case sysOK && !custOK:
			p.Kind = KindSystemOnly
			p.Delta = s.Neg()
			sum.SystemOnly++
		default:
			p.Delta = c.Sub(s)
			switch {
			case p.Delta.IsZero():
				p.Kind = KindMatched
				sum.Matched++
			case p.Delta.IsPositive():
				p.Kind = KindOverReported
				sum.OverReported++
				sum.HasConflict = true
			default:
				p.Kind = KindUnderReported
				sum.UnderReported++
				sum.HasConflict = true
			}
		}
		sum.TotalDelta = sum.TotalDelta.Add(p.Delta)
		pairs = append(pairs, p)
	}
	sum.TotalDelta = sum.TotalDelta.Round(4)
	return Diff{Pairs: pairs, Summary: sum}
}
