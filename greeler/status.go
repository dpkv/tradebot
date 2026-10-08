// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"github.com/bvk/tradebot/timerange"
	"github.com/bvk/tradebot/trader"
)

var _ trader.Statuser = &Greeler{}

// Status reports the stock limiters' fills within period, as GetSummary
// counts them, so status and profit reports include the greeler. A nil or
// zero period covers everything since the first order. Option premium and
// assignments are left out until the accounting model lands.
func (v *Greeler) Status(period *timerange.Range) *trader.Status {
	var r *timerange.Range
	if period != nil && !period.IsZero() {
		r = period
	}
	gs := v.GetSummary(r)

	var tp timerange.Range
	if r != nil {
		tp = *r
	} else if actions := v.Actions(); len(actions) > 0 {
		tp = timerange.Range{Begin: actions[0].Orders[0].CreateTime.Time}
	}

	s := &trader.Status{
		UID:          v.uid,
		ProductID:    v.cfg.ProductID,
		ExchangeName: v.cfg.ExchangeName,

		Summary: &trader.Summary{
			TimePeriod: tp,

			NumBuys:  int(gs.NumBuys.Round(0).IntPart()),
			NumSells: int(gs.NumSells.Round(0).IntPart()),

			SoldFees:  gs.SoldFees,
			SoldSize:  gs.SoldSize,
			SoldValue: gs.SoldValue,

			BoughtFees:  gs.BoughtFees,
			BoughtSize:  gs.BoughtSize,
			BoughtValue: gs.BoughtValue,

			UnsoldFees:  gs.UnsoldFees,
			UnsoldSize:  gs.UnsoldSize,
			UnsoldValue: gs.UnsoldValue,

			OversoldFees:  gs.OversoldFees,
			OversoldSize:  gs.OversoldSize,
			OversoldValue: gs.OversoldValue,
		},
	}
	s.Budget = v.BudgetAt(s.FeePct())
	return s
}
