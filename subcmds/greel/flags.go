// Copyright (c) 2026 Deepak Vankadaru

// Package greel holds the greeler and greelladder subcommands.
package greel

import (
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/subcmds/waller"
	"github.com/shopspring/decimal"
)

var d100 = decimal.NewFromInt(100)

// decimalValue is a flag.Value that keeps the decimal exactly as typed.
type decimalValue struct{ d *decimal.Decimal }

func (v decimalValue) String() string {
	if v.d == nil {
		return "0"
	}
	return v.d.String()
}

func (v decimalValue) Set(s string) error {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return err
	}
	*v.d = d
	return nil
}

// ConfigFlags are the flags that make one greeler config: the account and
// product, waller's grid range flags (fee adjustment included), the zones
// and the wheel knobs. A ladder takes the same flags and cuts the result
// with greelladder.Cut.
type ConfigFlags struct {
	// given records the required flags given on the command line. The cli
	// package sets flag values without flag.FlagSet.Parse, so FlagSet.Visit
	// can't tell.
	given map[string]bool

	account string
	product string

	grid waller.Spec

	gridPct       decimal.Decimal
	farPct        decimal.Decimal
	hysteresisPct decimal.Decimal
	dwellTime     time.Duration
	selector      string

	targetDelta        decimal.Decimal
	minDTE, maxDTE     int
	minPremiumYieldPct decimal.Decimal
	minOpenInterest    decimal.Decimal
	maxSpreadPct       decimal.Decimal
	repriceStep        decimal.Decimal
	repriceInterval    time.Duration
}

// requiredFlags must be given on the command line, even as zero, so no
// zone or knob is ever a default someone didn't choose.
var requiredFlags = []string{
	"account", "product",
	"grid-pct", "far-pct",
	"target-delta", "min-dte", "max-dte", "min-premium-yield-pct",
	"min-open-interest", "max-spread-pct", "reprice-step", "reprice-interval",
}

// trackedValue records in given when its flag is set.
type trackedValue struct {
	flag.Value
	name  string
	given map[string]bool
}

func (v *trackedValue) Set(s string) error {
	if err := v.Value.Set(s); err != nil {
		return err
	}
	v.given[v.name] = true
	return nil
}

func (f *ConfigFlags) SetFlags(fset *flag.FlagSet) {
	f.given = make(map[string]bool)
	defer func() {
		for _, name := range requiredFlags {
			fl := fset.Lookup(name)
			fl.Value = &trackedValue{Value: fl.Value, name: name, given: f.given}
		}
	}()
	fset.StringVar(&f.account, "account", "", "options account name for the job (required)")
	fset.StringVar(&f.product, "product", "", "underlying stock product id (required)")

	f.grid.SetFlags(fset)

	fset.Var(decimalValue{&f.gridPct}, "grid-pct", "grid zone: spot within this pct of the levels trades the grid (required)")
	fset.Var(decimalValue{&f.farPct}, "far-pct", "far zone: spot beyond this pct of the levels may flip to the wheel (required)")
	fset.Var(decimalValue{&f.hysteresisPct}, "hysteresis-pct", "pct spot must come back inside the grid zone before flipping back")
	fset.DurationVar(&f.dwellTime, "dwell-time", 0, "how long spot must stay in a zone before a flip")
	fset.StringVar(&f.selector, "contract-selector", "", "registered contract selector; empty means the default")

	fset.Var(decimalValue{&f.targetDelta}, "target-delta", "wanted option delta (required)")
	fset.IntVar(&f.minDTE, "min-dte", 0, "fewest days to expiry for a written option (required)")
	fset.IntVar(&f.maxDTE, "max-dte", 0, "most days to expiry; 0 means no upper bound (required)")
	fset.Var(decimalValue{&f.minPremiumYieldPct}, "min-premium-yield-pct", "smallest premium as a pct of the strike (required)")
	fset.Var(decimalValue{&f.minOpenInterest}, "min-open-interest", "smallest open interest; 0 means no filter (required)")
	fset.Var(decimalValue{&f.maxSpreadPct}, "max-spread-pct", "widest bid-ask spread as a pct of the mid; 0 means no filter (required)")
	fset.Var(decimalValue{&f.repriceStep}, "reprice-step", "fraction of the bid-ask spread per re-price, in (0, 1] (required)")
	fset.DurationVar(&f.repriceInterval, "reprice-interval", 0, "how long an option order rests before a re-price (required)")
}

func (f *ConfigFlags) AccountName() string { return f.account }

func (f *ConfigFlags) ProductID() string { return f.product }

// FeePct is the --fee-pct the grid was adjusted for.
func (f *ConfigFlags) FeePct() decimal.Decimal { return f.grid.FeePct() }

// Config builds the greeler config the flags describe and checks it with
// greeler.CheckConfig, the same check the server makes.
func (f *ConfigFlags) Config() (*gobs.GreelConfig, error) {
	if f.given == nil {
		return nil, fmt.Errorf("config flags are not registered (unexpected)")
	}
	var missing []string
	for _, name := range requiredFlags {
		if !f.given[name] {
			missing = append(missing, "--"+name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}

	if err := f.grid.Check(); err != nil {
		return nil, err
	}
	var levels []*gobs.Pair
	for _, p := range f.grid.BuySellPairs() {
		levels = append(levels, &gobs.Pair{Buy: gobs.Point(p.Buy), Sell: gobs.Point(p.Sell)})
	}

	cfg := &gobs.GreelConfig{
		ProductID:        f.product,
		AccountName:      f.account,
		GridLevels:       levels,
		GridPct:          f.gridPct,
		FarPct:           f.farPct,
		HysteresisPct:    f.hysteresisPct,
		DwellTime:        f.dwellTime,
		ContractSelector: f.selector,
		WheelKnobs: &gobs.WheelKnobs{
			TargetDelta:     f.targetDelta,
			MinDTE:          f.minDTE,
			MaxDTE:          f.maxDTE,
			MinPremiumYield: f.minPremiumYieldPct.Div(d100),
			MinOpenInterest: f.minOpenInterest,
			MaxSpreadPct:    f.maxSpreadPct,
			RepriceStep:     f.repriceStep,
			RepriceInterval: f.repriceInterval,
		},
	}
	if err := greeler.CheckConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
