// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bvk/tradebot/gobs"
)

func TestKnobSelector(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	near := now.Add(30 * 24 * time.Hour)
	far := now.Add(60 * 24 * time.Hour)
	contract := func(id, typ, strike string, expiry time.Time, bid, ask, oi string) *gobs.OptionContract {
		return &gobs.OptionContract{
			ContractID: id, Underlying: "AAPL", OptionType: typ, Strike: d(strike),
			Expiry: expiry, ContractSize: d("100"), Bid: d(bid), Ask: d(ask), OpenInterest: d(oi),
		}
	}
	chain := []*gobs.OptionContract{
		contract("P200-near", "PUT", "200", near, "2.00", "2.20", "500"),
		contract("P190-near", "PUT", "190", near, "1.00", "1.10", "500"),
		contract("P190-far", "PUT", "190", far, "1.50", "1.60", "500"),
		contract("P195-wide", "PUT", "195", near, "1.00", "2.00", "500"),
		contract("P185-thin", "PUT", "185", near, "1.00", "1.10", "5"),
		contract("C220-near", "CALL", "220", near, "1.00", "1.10", "500"),
		contract("C230-near", "CALL", "230", near, "0.50", "0.55", "500"),
	}
	knobs := &gobs.WheelKnobs{MinDTE: 20, MaxDTE: 45, MinOpenInterest: d("100"), MaxSpreadPct: d("20"), MinPremiumYield: d("0.004")}
	sel, err := NewSelector("", knobs)
	if err != nil {
		t.Fatal(err)
	}
	sel.(*KnobSelector).now = func() time.Time { return now }
	ctx := context.Background()

	// P200 is above MaxStrike, P195 too wide, P190-far beyond MaxDTE.
	got, err := sel.Select(ctx, chain, &Constraint{Underlying: "AAPL", OptionType: "PUT", MaxStrike: d("195")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Contract.ContractID != "P190-near" || !got.MinPremium.Equal(d("0.76")) {
		t.Errorf("put = %s min %s, want P190-near min 0.76", got.Contract.ContractID, got.MinPremium)
	}

	got, err = sel.Select(ctx, chain, &Constraint{Underlying: "AAPL", OptionType: "CALL", MinStrike: d("215")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Contract.ContractID != "C220-near" {
		t.Errorf("call = %s, want C220-near", got.Contract.ContractID)
	}

	// Excluded by a sibling; C230's mid is below its 0.92 floor.
	_, err = sel.Select(ctx, chain, &Constraint{Underlying: "AAPL", OptionType: "CALL", MinStrike: d("215"),
		Exclude: func(id string) bool { return id == "C220-near" }})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("select with C220 excluded = %v, want os.ErrNotExist", err)
	}

	// The nearest expiry wins over a better strike further out.
	unbounded, err := NewSelector("", &gobs.WheelKnobs{MinDTE: 20, MinOpenInterest: d("100"), MaxSpreadPct: d("20"), MinPremiumYield: d("0.004")})
	if err != nil {
		t.Fatal(err)
	}
	unbounded.(*KnobSelector).now = func() time.Time { return now }
	got, err = unbounded.Select(ctx, []*gobs.OptionContract{
		contract("P195-far", "PUT", "195", far, "1.50", "1.60", "500"),
		contract("P190-near", "PUT", "190", near, "1.00", "1.10", "500"),
	}, &Constraint{Underlying: "AAPL", OptionType: "PUT", MaxStrike: d("200")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Contract.ContractID != "P190-near" {
		t.Errorf("put without a DTE bound = %s, want P190-near", got.Contract.ContractID)
	}

	// An adjusted contract doesn't cover the shares the levels are sized for.
	adjusted := contract("P190-adj", "PUT", "190", near, "1.00", "1.10", "500")
	adjusted.ContractSize = d("150")
	_, err = sel.Select(ctx, []*gobs.OptionContract{adjusted}, &Constraint{Underlying: "AAPL", OptionType: "PUT", ContractSize: d("100")})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("select adjusted contract = %v, want os.ErrNotExist", err)
	}

	if _, err := NewSelector("nonesuch", knobs); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("NewSelector(nonesuch) = %v", err)
	}
	if err := RegisterSelector(DefaultSelector, func(*gobs.WheelKnobs) (ContractSelector, error) { return nil, nil }); !errors.Is(err, os.ErrExist) {
		t.Errorf("re-registering the default = %v", err)
	}
}
