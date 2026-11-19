// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"fmt"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/shopspring/decimal"
)

// Cut splits one greeler config over a whole range into bands of one
// contract each, for New. Walking up from the lowest level, each band takes
// the fewest levels that hold greeler.ContractShares; levels left over at
// the top, too few for a contract, join the top band. Every band keeps
// cfg's other settings. cfg must pass greeler.CheckConfig, which also
// makes sure there is at least one band.
func Cut(cfg *gobs.GreelConfig) ([]*gobs.GreelConfig, error) {
	if err := greeler.CheckConfig(cfg); err != nil {
		return nil, err
	}
	var groups [][]*gobs.Pair
	var group []*gobs.Pair
	var shares decimal.Decimal
	for _, p := range cfg.GridLevels {
		pp := *p
		group = append(group, &pp)
		shares = shares.Add(p.Buy.Size)
		if shares.GreaterThanOrEqual(greeler.ContractShares) {
			groups = append(groups, group)
			group, shares = nil, decimal.Zero
		}
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("greel levels hold less than one contract (unexpected)")
	}
	if len(group) > 0 {
		last := len(groups) - 1
		groups[last] = append(groups[last], group...)
	}
	bands := make([]*gobs.GreelConfig, 0, len(groups))
	for _, levels := range groups {
		b := *cfg
		b.GridLevels = levels
		if cfg.WheelKnobs != nil {
			knobs := *cfg.WheelKnobs
			b.WheelKnobs = &knobs
		}
		bands = append(bands, &b)
	}
	return bands, nil
}
