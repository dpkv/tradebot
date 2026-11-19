// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"flag"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var sampleArgs = []string{
	"--account", "etrade/main", "--product", "AAPL",
	"--begin-price", "150", "--end-price", "160", "--buy-interval", "2",
	"--profit-margin", "150", "--buy-size", "50", "--fee-pct", "0",
	"--grid-pct", "5", "--far-pct", "12", "--hysteresis-pct", "2", "--dwell-time", "48h",
	"--target-delta", "0.3", "--min-dte", "21", "--max-dte", "45",
	"--min-premium-yield-pct", "0.5", "--min-open-interest", "100", "--max-spread-pct", "10",
	"--reprice-step", "0.25", "--reprice-interval", "5m",
}

func parse(t *testing.T, args []string) *ConfigFlags {
	t.Helper()
	f := new(ConfigFlags)
	fset := flag.NewFlagSet("test", flag.ContinueOnError)
	f.SetFlags(fset)
	if err := fset.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestConfig(t *testing.T) {
	cfg, err := parse(t, sampleArgs).Config()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccountName != "etrade/main" || cfg.ProductID != "AAPL" {
		t.Errorf("config trades %s on %s", cfg.ProductID, cfg.AccountName)
	}
	var buys []string
	for _, p := range cfg.GridLevels {
		buys = append(buys, p.Buy.Price.String()+"/"+p.Sell.Price.String())
	}
	if got := strings.Join(buys, " "); got != "150/153 152/155 154/157 156/159 158/161" {
		t.Errorf("levels = %s", got)
	}
	k := cfg.WheelKnobs
	if !cfg.GridPct.Equal(d("5")) || !cfg.FarPct.Equal(d("12")) || !cfg.HysteresisPct.Equal(d("2")) || cfg.DwellTime != 48*time.Hour {
		t.Errorf("zones = %s %s %s %s", cfg.GridPct, cfg.FarPct, cfg.HysteresisPct, cfg.DwellTime)
	}
	if !k.MinPremiumYield.Equal(d("0.005")) {
		t.Errorf("min premium yield = %s, want 0.005", k.MinPremiumYield)
	}
	if !k.TargetDelta.Equal(d("0.3")) || k.MinDTE != 21 || k.MaxDTE != 45 || !k.MinOpenInterest.Equal(d("100")) ||
		!k.MaxSpreadPct.Equal(d("10")) || !k.RepriceStep.Equal(d("0.25")) || k.RepriceInterval != 5*time.Minute {
		t.Errorf("knobs = %+v", k)
	}
}

func TestConfigAdjustsForFees(t *testing.T) {
	args := slices.Clone(sampleArgs)
	i := slices.Index(args, "--fee-pct")
	args[i+1] = "0.25"
	cfg, err := parse(t, args).Config()
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.GridLevels[0]; !p.Sell.Price.GreaterThan(d("153")) {
		t.Errorf("sell price %s is not fee adjusted above 153", p.Sell.Price)
	}
}

func TestConfigRequiresFlags(t *testing.T) {
	for _, name := range requiredFlags {
		var args []string
		for i := 0; i < len(sampleArgs); i += 2 {
			if sampleArgs[i] != "--"+name {
				args = append(args, sampleArgs[i], sampleArgs[i+1])
			}
		}
		if _, err := parse(t, args).Config(); err == nil || !strings.Contains(err.Error(), "--"+name) {
			t.Errorf("without --%s: err = %v", name, err)
		}
	}
	// Zero is accepted when given.
	args := slices.Clone(sampleArgs)
	args[slices.Index(args, "--max-dte")+1] = "0"
	if _, err := parse(t, args).Config(); err != nil {
		t.Errorf("--max-dte 0: %v", err)
	}
}

func TestConfigChecksTheConfig(t *testing.T) {
	args := slices.Clone(sampleArgs)
	args[slices.Index(args, "--reprice-step")+1] = "0"
	if _, err := parse(t, args).Config(); err == nil {
		t.Errorf("--reprice-step 0 succeeded")
	}
	args = slices.Clone(sampleArgs)
	args[slices.Index(args, "--buy-size")+1] = "10" // 50 shares, not a contract
	if _, err := parse(t, args).Config(); err == nil {
		t.Errorf("a grid under one contract succeeded")
	}
}
