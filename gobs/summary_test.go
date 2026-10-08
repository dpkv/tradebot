// Copyright (c) 2026 Deepak Vankadaru

package gobs

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestProfitCountsSettledPremium(t *testing.T) {
	d := decimal.RequireFromString
	s := &Summary{
		BoughtSize: d("100"), BoughtValue: d("1000"), BoughtFees: d("1"),
		SoldSize: d("100"), SoldValue: d("1100"), SoldFees: d("1"),
		PremiumValue: d("80"), PremiumFees: d("2"),
		OpenPremiumValue: d("30"), OpenPremiumFees: d("1"),
	}
	// 1100 - 1000 - 2 stock fees + (80 - 30) - (2 - 1) premium.
	if got := s.Profit(); !got.Equal(d("147")) {
		t.Errorf("Profit = %s, want 147", got)
	}
	if got := s.Fees(); !got.Equal(d("4")) {
		t.Errorf("Fees = %s, want 4", got)
	}

	var sum Summary
	sum.Add(s)
	sum.Add(&Summary{PremiumValue: d("20")})
	if !sum.PremiumValue.Equal(d("100")) || !sum.OpenPremiumValue.Equal(d("30")) || !sum.OpenPremiumFees.Equal(d("1")) {
		t.Errorf("Add: premium %s open %s open fees %s", sum.PremiumValue, sum.OpenPremiumValue, sum.OpenPremiumFees)
	}

	if (&Summary{PremiumValue: d("1")}).IsZero() {
		t.Errorf("a premium-only summary is zero")
	}
}
