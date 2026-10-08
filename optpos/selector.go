// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// DefaultSelector is the name an empty selector name resolves to.
const DefaultSelector = "default"

// SelectorFactory builds a selector from the owning greeler's persisted
// knobs, so a restart rebuilds the same behavior.
type SelectorFactory func(knobs *gobs.WheelKnobs) (ContractSelector, error)

var (
	selectorsMu sync.Mutex
	selectors   = map[string]SelectorFactory{
		DefaultSelector: func(knobs *gobs.WheelKnobs) (ContractSelector, error) {
			return NewKnobSelector(knobs), nil
		},
	}
)

// RegisterSelector adds a compiled-in selector implementation by name.
func RegisterSelector(name string, f SelectorFactory) error {
	selectorsMu.Lock()
	defer selectorsMu.Unlock()
	if name == "" || f == nil {
		return fmt.Errorf("selector name or factory is empty: %w", os.ErrInvalid)
	}
	if _, ok := selectors[name]; ok {
		return fmt.Errorf("selector %q is already registered: %w", name, os.ErrExist)
	}
	selectors[name] = f
	return nil
}

// NewSelector builds the selector registered under name; empty means
// DefaultSelector.
func NewSelector(name string, knobs *gobs.WheelKnobs) (ContractSelector, error) {
	if name == "" {
		name = DefaultSelector
	}
	selectorsMu.Lock()
	f, ok := selectors[name]
	selectorsMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("contract selector %q is not registered: %w", name, os.ErrNotExist)
	}
	return f(knobs)
}

// KnobSelector is the default selector. It filters the chain by the
// constraint and the knobs, then picks the strike nearest the levels (the
// highest put, the lowest call) at the nearest expiry.
//
// TargetDelta is not applied: chain snapshots carry no greeks.
type KnobSelector struct {
	knobs gobs.WheelKnobs
	now   func() time.Time
}

func NewKnobSelector(knobs *gobs.WheelKnobs) *KnobSelector {
	v := &KnobSelector{now: time.Now}
	if knobs != nil {
		v.knobs = *knobs
	}
	return v
}

// minTick is the smallest premium worth selling for.
var minTick = decimal.RequireFromString("0.01")

// Select returns the best candidate. MinPremium is MinPremiumYield (a
// fraction of the strike) times the strike, at least one cent.
func (v *KnobSelector) Select(ctx context.Context, chain []*gobs.OptionContract, c *Constraint) (*Selection, error) {
	if c == nil {
		return nil, fmt.Errorf("constraint is nil: %w", os.ErrInvalid)
	}
	now := v.now()
	var candidates []*Selection
	for _, contract := range chain {
		if sel := v.candidate(now, contract, c); sel != nil {
			candidates = append(candidates, sel)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no %s contract for %s matches the constraint and knobs: %w", c.OptionType, c.Underlying, os.ErrNotExist)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].Contract, candidates[j].Contract
		if !a.Expiry.Equal(b.Expiry) {
			return a.Expiry.Before(b.Expiry)
		}
		if a.OptionType == "PUT" {
			return a.Strike.GreaterThan(b.Strike)
		}
		return a.Strike.LessThan(b.Strike)
	})
	return candidates[0], nil
}

func (v *KnobSelector) candidate(now time.Time, contract *gobs.OptionContract, c *Constraint) *Selection {
	if contract == nil || contract.ContractID == "" {
		return nil
	}
	if c.Underlying != "" && contract.Underlying != c.Underlying {
		return nil
	}
	if c.OptionType != "" && contract.OptionType != c.OptionType {
		return nil
	}
	if c.MaxStrike.IsPositive() && contract.Strike.GreaterThan(c.MaxStrike) {
		return nil
	}
	if c.MinStrike.IsPositive() && contract.Strike.LessThan(c.MinStrike) {
		return nil
	}
	if c.ContractSize.IsPositive() && !contract.ContractSize.Equal(c.ContractSize) {
		return nil
	}
	if c.Exclude != nil && c.Exclude(contract.ContractID) {
		return nil
	}

	dte := int(contract.Expiry.Sub(now).Hours() / 24)
	if dte < v.knobs.MinDTE || (v.knobs.MaxDTE > 0 && dte > v.knobs.MaxDTE) || !contract.Expiry.After(now) {
		return nil
	}
	if contract.OpenInterest.LessThan(v.knobs.MinOpenInterest) {
		return nil
	}
	if !contract.Bid.IsPositive() || contract.Ask.LessThan(contract.Bid) {
		return nil
	}
	mid := contract.Bid.Add(contract.Ask).Div(decimal.NewFromInt(2))
	if v.knobs.MaxSpreadPct.IsPositive() {
		spreadPct := contract.Ask.Sub(contract.Bid).Mul(decimal.NewFromInt(100)).Div(mid)
		if spreadPct.GreaterThan(v.knobs.MaxSpreadPct) {
			return nil
		}
	}
	minPremium := decimal.Max(v.knobs.MinPremiumYield.Mul(contract.Strike), minTick)
	if mid.LessThan(minPremium) {
		return nil
	}
	return &Selection{Contract: contract, MinPremium: minPremium}
}
