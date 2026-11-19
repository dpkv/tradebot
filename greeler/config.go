// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"fmt"
	"os"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvk/tradebot/point"
	"github.com/shopspring/decimal"
)

// CheckConfig reports whether cfg is a config New would accept, without a
// uid or an exchange, so clients can check a config before sending it. New
// and Load check every config with it.
func CheckConfig(cfg *gobs.GreelConfig) error {
	if cfg == nil {
		return fmt.Errorf("greeler config is nil: %w", os.ErrInvalid)
	}
	account := cfg.AccountName
	if account == "" {
		// Configs from before accounts name only the exchange.
		account = cfg.ExchangeName
	}
	if cfg.ProductID == "" || account == "" {
		return fmt.Errorf("greeler product or account name is empty")
	}
	if err := checkGrid(cfg.GridLevels); err != nil {
		return err
	}
	g, f, h := cfg.GridPct, cfg.FarPct, cfg.HysteresisPct
	if !g.IsPositive() || !f.GreaterThan(g) {
		return fmt.Errorf("zone percentages need 0 < grid (%s) < far (%s)", g, f)
	}
	if h.IsNegative() || !h.LessThan(f) {
		return fmt.Errorf("hysteresis %s must be within [0, far %s)", h, f)
	}
	if cfg.DwellTime < 0 {
		return fmt.Errorf("dwell time %s is negative", cfg.DwellTime)
	}
	if err := checkKnobs(cfg.WheelKnobs); err != nil {
		return err
	}
	if _, err := optpos.NewSelector(cfg.ContractSelector, cfg.WheelKnobs); err != nil {
		return err
	}
	return nil
}

func checkGrid(levels []*gobs.Pair) error {
	if len(levels) == 0 {
		return fmt.Errorf("greeler has no grid levels")
	}
	var total decimal.Decimal
	var prev *point.Pair
	for i, gp := range levels {
		if gp == nil {
			return fmt.Errorf("greeler grid level %d is nil: %w", i, os.ErrInvalid)
		}
		p := point.NewPairFromGobPair(gp)
		if err := p.Check(); err != nil {
			return fmt.Errorf("grid level %d is invalid: %w", i, err)
		}
		// A level is either all cash or all shares between cycles, which is
		// what all-or-nothing qualification counts on.
		if !p.Sell.Size.Equal(p.Buy.Size) {
			return fmt.Errorf("grid level %d sell size %s differs from buy size %s", i, p.Sell.Size, p.Buy.Size)
		}
		if prev != nil && !p.Buy.Price.GreaterThan(prev.Buy.Price) {
			return fmt.Errorf("grid level %d is not above level %d", i, i-1)
		}
		total = total.Add(p.Buy.Size)
		prev = p
	}
	if total.LessThan(ContractShares) {
		return fmt.Errorf("grid levels hold %s shares, want at least %s for one contract", total, ContractShares)
	}
	return nil
}

// checkKnobs requires every knob to be chosen: zero is accepted only where
// it already means something (MinDTE, and MaxDTE as no upper bound, and the
// filters that treat zero as no filter), never as a stand-in for a default.
func checkKnobs(k *gobs.WheelKnobs) error {
	if k == nil {
		return fmt.Errorf("greeler wheel knobs are missing: %w", os.ErrInvalid)
	}
	if k.MinDTE < 0 {
		return fmt.Errorf("wheel min dte %d is negative", k.MinDTE)
	}
	if k.MaxDTE < 0 {
		return fmt.Errorf("wheel max dte %d is negative", k.MaxDTE)
	}
	if k.MaxDTE > 0 && k.MaxDTE < k.MinDTE {
		return fmt.Errorf("wheel max dte %d is below min dte %d", k.MaxDTE, k.MinDTE)
	}
	for _, kv := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"target delta", k.TargetDelta},
		{"min premium yield", k.MinPremiumYield},
		{"min open interest", k.MinOpenInterest},
		{"max spread pct", k.MaxSpreadPct},
	} {
		if kv.v.IsNegative() {
			return fmt.Errorf("wheel %s %s is negative", kv.name, kv.v)
		}
	}
	if !k.RepriceStep.IsPositive() || k.RepriceStep.GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("wheel reprice step %s must be within (0, 1]", k.RepriceStep)
	}
	if k.RepriceInterval <= 0 {
		return fmt.Errorf("wheel reprice interval %s must be positive", k.RepriceInterval)
	}
	return nil
}
