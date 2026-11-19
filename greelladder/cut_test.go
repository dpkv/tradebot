// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"fmt"
	"testing"

	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
)

// prices lists each band's buy prices.
func prices(bands []*gobs.GreelConfig) string {
	var out [][]string
	for _, b := range bands {
		var ps []string
		for _, p := range b.GridLevels {
			ps = append(ps, p.Buy.Price.String())
		}
		out = append(out, ps)
	}
	return fmt.Sprint(out)
}

func rangeConfig(from, n int) *gobs.GreelConfig {
	cfg := band(from)
	cfg.GridLevels = nil
	for i := 0; i < n; i++ {
		cfg.GridLevels = append(cfg.GridLevels, level(fmt.Sprint(from+i)))
	}
	cfg.ProductID, cfg.AccountName = "AAPL", "fake"
	return cfg
}

func TestCut(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{4, "[[100 101 102 103]]"},
		{7, "[[100 101 102 103 104 105 106]]"},
		{8, "[[100 101 102 103] [104 105 106 107]]"},
		{10, "[[100 101 102 103] [104 105 106 107 108 109]]"},
	} {
		cfg := rangeConfig(100, tc.n)
		bands, err := Cut(cfg)
		if err != nil {
			t.Fatalf("%d levels: %v", tc.n, err)
		}
		if got := prices(bands); got != tc.want {
			t.Errorf("%d levels: bands = %s, want %s", tc.n, got, tc.want)
		}
		if err := CheckBands("fake", "AAPL", bands); err != nil {
			t.Errorf("%d levels: CheckBands: %v", tc.n, err)
		}
		if _, err := New(uuid.NewString(), "fake", "AAPL", bands); err != nil {
			t.Errorf("%d levels: New: %v", tc.n, err)
		}
	}
}

func TestCutUnequalSizes(t *testing.T) {
	cfg := rangeConfig(100, 5)
	cfg.GridLevels[0].Buy.Size, cfg.GridLevels[0].Sell.Size = d("100"), d("100")
	bands, err := Cut(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The 100-share level is a contract alone; the next four make another.
	if got := prices(bands); got != "[[100] [101 102 103 104]]" {
		t.Errorf("bands = %s, want [[100] [101 102 103 104]]", got)
	}
}

func TestCutCopies(t *testing.T) {
	cfg := rangeConfig(100, 8)
	bands, err := Cut(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bands[0].GridLevels[0].Buy.Price = d("1")
	bands[0].WheelKnobs.MinDTE = 99
	if !cfg.GridLevels[0].Buy.Price.Equal(d("100")) || cfg.WheelKnobs.MinDTE != 0 {
		t.Errorf("Cut shares levels or knobs with its input")
	}
	if bands[1].WheelKnobs == bands[0].WheelKnobs {
		t.Errorf("bands share knobs")
	}
}

func TestCutRejectsInvalidConfig(t *testing.T) {
	if _, err := Cut(rangeConfig(100, 3)); err == nil {
		t.Errorf("Cut accepted 75 shares")
	}
	if _, err := Cut(nil); err == nil {
		t.Errorf("Cut accepted a nil config")
	}
}
