// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"github.com/bvk/tradebot/timerange"
	"github.com/bvk/tradebot/trader"
)

var _ trader.Statuser = &GreelLadder{}

// Status sums the greelers' statuses, as Waller.Status does its loopers'.
// Like theirs, it leaves out option premium and assignments until the
// accounting model lands.
func (v *GreelLadder) Status(period *timerange.Range) *trader.Status {
	var ss []*trader.Status
	for _, g := range v.greelers {
		ss = append(ss, g.Status(period))
	}
	return &trader.Status{
		UID:          v.uid,
		ProductID:    v.cfg.ProductID,
		ExchangeName: v.cfg.ExchangeName,
		Summary:      trader.Summarize(ss),
	}
}
