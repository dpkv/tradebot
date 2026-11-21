// Copyright (c) 2026 Deepak Vankadaru

package gobs

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestTaxLotIDEqual(t *testing.T) {
	const base = ETradeTaxLotIDPrefix + "KEY/portfolio/42?"
	for _, tc := range []struct {
		a, b  string
		equal bool
	}{
		{"AccountID=1&OrderNo=7&Symbol=AAPL", "Symbol=AAPL&OrderNo=7&AccountID=1", true},
		{"AccountID=1&OrderNo=7&Symbol=AAPL", "AccountID=2&OrderNo=7&Symbol=AAPL", false},
		{"AccountID=1&OrderNo=7&Symbol=AAPL", "AccountID=1&OrderNo=8&Symbol=AAPL", false},
		{"AccountID=1&PositionLotID=9&Symbol=AAPL", "AccountID=1&PositionLotID=9&Qty=25&Symbol=AAPL", true},
		{"AccountID=1&PositionLotID=9&Symbol=AAPL", "AccountID=1&PositionLotID=8&Symbol=AAPL", false},
		{"AccountID=1&OrderNo=7&Symbol=AAPL", "AccountID=1&PositionLotID=7&Symbol=AAPL", false},
	} {
		if got := TaxLotID(base + tc.a).Equal(TaxLotID(base + tc.b)); got != tc.equal {
			t.Errorf("%s vs %s: %v, want %v", tc.a, tc.b, got, tc.equal)
		}
	}
}

func TestTaxLotIDWithQty(t *testing.T) {
	id := TaxLotID(ETradeTaxLotIDPrefix + "KEY/portfolio/42?AccountID=1&PositionLotID=9&Symbol=AAPL")
	q := id.WithQty(decimal.NewFromInt(25))
	if want := ETradeTaxLotIDPrefix + "KEY/portfolio/42?AccountID=1&PositionLotID=9&Qty=25&Symbol=AAPL"; string(q) != want {
		t.Errorf("WithQty = %s", q)
	}
	if !q.Equal(id) {
		t.Error("capped id isn't equal to the lot")
	}
	if got := TaxLotID("other").WithQty(decimal.NewFromInt(25)); got != "other" {
		t.Errorf("non-etrade WithQty = %s", got)
	}
}
