// Copyright (c) 2026 Deepak Vankadaru

package trader

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestSummarizePremium: premium counts toward profit and fees once its
// position settles; the open part is held out, like unsold shares.
func TestSummarizePremium(t *testing.T) {
	sum := Summarize([]*Status{
		{Summary: &Summary{BoughtValue: d("100"), SoldValue: d("110"), BoughtFees: d("1"), SoldFees: d("1")}},
		{Summary: &Summary{PremiumValue: d("150"), PremiumFees: d("2"), OpenPremiumValue: d("50"), OpenPremiumFees: d("1")}},
	})
	if !sum.Premium().Equal(d("100")) {
		t.Errorf("premium = %s, want 100", sum.Premium())
	}
	if !sum.Fees().Equal(d("3")) {
		t.Errorf("fees = %s, want 3", sum.Fees())
	}
	if !sum.Profit().Equal(d("107")) {
		t.Errorf("profit = %s, want 107", sum.Profit())
	}
	// Every fee over every value traded, premium included.
	if want := d("4").Mul(d("100")).Div(d("360")); !sum.FeePct().Equal(want) {
		t.Errorf("fee pct = %s, want %s", sum.FeePct(), want)
	}
}
