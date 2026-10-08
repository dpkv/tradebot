// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"testing"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvkgo/kv"
)

func checkSummary(t *testing.T, what string, s *gobs.Summary, bought, sold, unsold, premium, openPremium, profit string) {
	t.Helper()
	if !s.BoughtValue.Equal(d(bought)) || !s.SoldValue.Equal(d(sold)) || !s.UnsoldValue.Equal(d(unsold)) ||
		!s.PremiumValue.Equal(d(premium)) || !s.OpenPremiumValue.Equal(d(openPremium)) || !s.Profit().Equal(d(profit)) {
		t.Errorf("%s: bought %s sold %s unsold %s premium %s open %s profit %s; want %s %s %s %s %s %s", what,
			s.BoughtValue, s.SoldValue, s.UnsoldValue, s.PremiumValue, s.OpenPremiumValue, s.Profit(),
			bought, sold, unsold, premium, openPremium, profit)
	}
	if !s.OversoldSize.IsZero() {
		t.Errorf("%s: oversold %s", what, s.OversoldSize)
	}
}

// TestSummaryPutCycle: premium counts once the put settles, the assigned
// shares are bought at the strike, and the grid's sells pair with them.
func TestSummaryPutCycle(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)

	pos.mu.Lock()
	pos.premiums = []*optpos.Premium{{At: time.Now(), Value: d("150"), Fee: d("1")}}
	pos.mu.Unlock()
	e.step(r, "200")
	checkSummary(t, "open put", v.GetSummary(nil), "0", "0", "0", "150", "150", "0")

	pos.mu.Lock()
	pos.outcome, pos.outcomeAt = "assigned", time.Now()
	pos.assignment = &gobs.AssignmentFact{Key: "tx1", Shares: d("100"), Price: d("100")}
	pos.mu.Unlock()
	e.step(r, "99")
	if v.Mode() != "grid" {
		t.Fatalf("mode %s after assignment", v.Mode())
	}
	checkSummary(t, "assigned put", v.GetSummary(nil), "10000", "0", "10000", "150", "0", "149")

	time.Sleep(10 * time.Millisecond)
	afterAssignment := time.Now()
	e.stock.setPrice("99")
	e.step(r, "99") // sells at 101..103 start; 104 waits
	waitFor(t, "three sells", func() bool { return len(e.stock.live()) == 3 })
	e.stock.fillLive()
	waitIdle(t, r)
	r.reap()
	checkSummary(t, "after sells", v.GetSummary(nil), "10000", "7650", "2500", "150", "0", "299")

	// Within a range holding only the sells, their lots count in full; the
	// premium and level 3's lot are before it.
	s := v.GetSummary(&timerange.Range{Begin: afterAssignment})
	checkSummary(t, "ranged", s, "7500", "7650", "0", "0", "0", "150")

	var assigned int
	for _, a := range v.Actions() {
		if a.Orders[0].Status == "ASSIGNED" {
			assigned++
			if a.Orders[0].Side != "BUY" || !a.Orders[0].FilledPrice.Equal(d("100")) || !a.Orders[0].FilledSize.Equal(d("25")) {
				t.Errorf("assignment action order = %+v", a.Orders[0])
			}
		}
	}
	if assigned != 4 {
		t.Errorf("assignment actions = %d, want one per level", assigned)
	}

	// A loaded greeler reads its positions from their records. The fake
	// saves no legs, so only the assignment comes back.
	if err := kv.WithReadWriter(context.Background(), e.db, pos.Save); err != nil {
		t.Fatal(err)
	}
	w := e.reload(v)
	checkSummary(t, "reloaded", w.GetSummary(nil), "10000", "7650", "2500", "0", "0", "150")
}

// TestSummaryCallCycle: the call takes the bought shares at the strike and
// its fee and premium count.
func TestSummaryCallCycle(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	e.step(r, "101.5")
	for _, p := range []string{"100.5", "101.5", "102.5", "103.5"} {
		e.stock.setPrice(p)
		waitFor(t, "a buy at "+p, func() bool { return len(e.stock.live()) == 1 })
		e.stock.fillLive()
	}
	waitIdle(t, r)
	r.reap()
	checkSummary(t, "bought", v.GetSummary(nil), "10150", "0", "10150", "0", "0", "0")

	e.stock.setPrice("80")
	e.step(r, "80")
	e.clock.Add(time.Hour)
	e.step(r, "80")
	if v.Mode() != "wheel" {
		t.Fatalf("did not flip to a call")
	}
	pos := e.positions[v.current().PositionID]
	pos.mu.Lock()
	pos.premiums = []*optpos.Premium{{At: time.Now(), Value: d("80"), Fee: d("1")}}
	pos.outcome, pos.outcomeAt = "assigned", time.Now()
	pos.assignment = &gobs.AssignmentFact{Key: "tx2", Shares: d("-100"), Price: d("105"), Fee: d("0.5")}
	pos.mu.Unlock()
	e.step(r, "106")

	s := v.GetSummary(nil)
	checkSummary(t, "called away", s, "10150", "10500", "0", "80", "0", "428.5")
	if !s.SoldFees.Equal(d("0.5")) || !s.NumSells.Equal(d("4")) {
		t.Errorf("sold fees %s sells %s; want 0.5 and 4", s.SoldFees, s.NumSells)
	}
}
