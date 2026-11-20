// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"fmt"
	"path"
	"slices"
	"testing"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
)

// lotsBuilder builds a greeler's epochs by hand from limiters loaded with
// the given fills, for the replay in sellSources.
type lotsBuilder struct {
	t  *testing.T
	db kv.Database
	v  *Greeler
	n  int
}

func newLotsBuilder(t *testing.T) *lotsBuilder {
	v, err := New(uuid.NewString(), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return &lotsBuilder{t: t, db: newTestDB(t), v: v}
}

// limiter adds to the last epoch, which must be a grid epoch, a level-0
// limiter of side with filled shares of size and the given lots.
func (b *lotsBuilder) limiter(side, size, filled string, lots ...string) *limiter.Limiter {
	b.t.Helper()
	b.n++
	pt := b.v.cfg.GridLevels[0].Buy
	if side == "SELL" {
		pt = b.v.cfg.GridLevels[0].Sell
	}
	pt.Size = d(size)
	orders := make(map[string]*gobs.Order)
	if d(filled).IsPositive() {
		id := fmt.Sprintf("order-%d", b.n)
		orders[id] = &gobs.Order{ServerOrderID: id, ClientOrderID: uuid.NewString(), Side: side, FilledSize: d(filled), FilledPrice: pt.Price, Done: d(filled).Equal(d(size))}
	}
	var ids []gobs.TaxLotID
	for _, l := range lots {
		ids = append(ids, gobs.TaxLotID(l))
	}
	uid := path.Join(b.v.uid, fmt.Sprintf("l-%d", b.n))
	gv := &gobs.LimiterState{V2: &gobs.LimiterStateV2{
		ProductID: "AAPL", AccountName: "fake", ExchangeName: "fake",
		TradePoint: pt, ServerIDOrderMap: orders, TaxLots: ids,
	}}
	var l *limiter.Limiter
	err := kv.WithReadWriter(context.Background(), b.db, func(ctx context.Context, rw kv.ReadWriter) error {
		if err := kvutil.Set(ctx, rw, path.Join(limiter.DefaultKeyspace, uid), gv); err != nil {
			return err
		}
		var err error
		l, err = limiter.Load(ctx, uid, rw)
		return err
	})
	if err != nil {
		b.t.Fatal(err)
	}
	e := b.v.epochs[len(b.v.epochs)-1]
	e.limiters[0] = append(e.limiters[0], l)
	return l
}

// assigned adds a wheel epoch assigned shares (negative for a call), then
// a grid epoch after it. loaded picks whether the epoch carries a
// position, as under Run, or only its saved facts, as after Load.
func (b *lotsBuilder) assigned(shares string, loaded bool) {
	fact := &gobs.AssignmentFact{Key: "tx", Shares: d(shares), Price: d("100")}
	e := &epoch{GreelEpoch: gobs.GreelEpoch{Mode: "wheel", PositionID: "pos"}}
	if loaded {
		p := &fakePosition{uid: "pos"}
		p.settle("assigned", fact)
		e.position = p
	} else {
		e.facts = &optpos.Facts{Outcome: "assigned", Assignment: fact}
	}
	b.v.epochs = append(b.v.epochs, e, b.v.newGridEpoch(b.v.now()))
}

func (b *lotsBuilder) check(what string, stop *limiter.Limiter, unlotted bool, buys []*limiter.Limiter, lots ...string) {
	b.t.Helper()
	s, err := b.v.sellSources(0, stop)
	if err != nil {
		b.t.Fatalf("%s: %v", what, err)
	}
	if s.unlotted != unlotted {
		b.t.Errorf("%s: unlotted = %v, want %v", what, s.unlotted, unlotted)
	}
	if !slices.Equal(s.buys, buys) {
		b.t.Errorf("%s: buys = %v, want %v", what, s.buys, buys)
	}
	var want []gobs.TaxLotID
	for _, l := range lots {
		want = append(want, gobs.TaxLotID(l))
	}
	if got := s.lots(); !slices.Equal(got, want) {
		b.t.Errorf("%s: lots = %v, want %v", what, got, want)
	}
}

func TestSellSourcesGridCycles(t *testing.T) {
	b := newLotsBuilder(t)
	b.check("empty", nil, false, nil)

	b1 := b.limiter("BUY", "25", "25", "lot-1")
	b.check("after first buy", nil, false, []*limiter.Limiter{b1}, "lot-1")

	s1 := b.limiter("SELL", "25", "10")
	b.check("resuming the partial sell", s1, false, []*limiter.Limiter{b1}, "lot-1")

	b.v.epochs[0].limiters[0] = b.v.epochs[0].limiters[0][:1]
	b.limiter("SELL", "25", "25", "lot-1")
	b.check("after the sell", nil, false, nil)

	// A buy with nothing filled is skipped; the next cycle's lots stand
	// alone, and a buy without lots shows up as missing.
	b.limiter("BUY", "25", "0")
	b2 := b.limiter("BUY", "25", "25")
	b.check("second cycle", nil, false, []*limiter.Limiter{b2})
	s, err := b.v.sellSources(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := s.missing(); len(m) != 1 || m[0] != b2 {
		t.Errorf("missing = %v, want the second buy", m)
	}
	b2.SetTaxLotIDs([]gobs.TaxLotID{"lot-2"})
	b.check("second cycle with lots", nil, false, []*limiter.Limiter{b2}, "lot-2")

	// Old records may hold two buys in one cycle.
	b3 := b.limiter("BUY", "25", "25", "lot-3a", "lot-3b")
	b.check("two buys in a cycle", nil, false, []*limiter.Limiter{b2, b3}, "lot-2", "lot-3a", "lot-3b")
}

func TestSellSourcesAcrossEpochs(t *testing.T) {
	b := newLotsBuilder(t)
	b1 := b.limiter("BUY", "25", "25", "lot-1")
	// A grid epoch with no limiters on the level, as after a flip that
	// found the level still full.
	b.v.epochs = append(b.v.epochs, b.v.newGridEpoch(b.v.now()))
	b.check("next epoch", nil, false, []*limiter.Limiter{b1}, "lot-1")
	b.limiter("SELL", "25", "25")
	b.check("sold in the next epoch", nil, false, nil)
}

func TestSellSourcesPutAssignment(t *testing.T) {
	for _, loaded := range []bool{true, false} {
		b := newLotsBuilder(t)
		b.assigned("100", loaded)
		b.check(fmt.Sprintf("put assigned (loaded %v)", loaded), nil, true, nil)

		b.limiter("SELL", "25", "25")
		b.check("put shares sold", nil, false, nil)

		b1 := b.limiter("BUY", "25", "25", "lot-1")
		b.check("next cycle", nil, false, []*limiter.Limiter{b1}, "lot-1")
	}
}

func TestSellSourcesCallAssignment(t *testing.T) {
	// Level 0 is lowest, so a call takes its shares first: the level ends
	// flat.
	b := newLotsBuilder(t)
	b.limiter("BUY", "25", "25", "lot-1")
	b.assigned("-100", true)
	b.check("called away", nil, false, nil)

	// A call of fewer shares than the level holds leaves it partly held,
	// and the lots left are the broker's choice.
	b = newLotsBuilder(t)
	b1 := b.limiter("BUY", "25", "25", "lot-1")
	b.assigned("-10", false)
	b.check("partly called", nil, true, []*limiter.Limiter{b1}, "lot-1")
}

func TestSellSourcesNeedsPositions(t *testing.T) {
	b := newLotsBuilder(t)
	b.v.epochs = append(b.v.epochs, &epoch{GreelEpoch: gobs.GreelEpoch{Mode: "wheel", PositionID: "pos"}})
	if _, err := b.v.sellSources(0, nil); err == nil {
		t.Error("no error for a wheel epoch with nothing loaded")
	}
	p := &fakePosition{uid: "pos"}
	p.settle("assigned", nil)
	b.v.epochs[1].position = p
	if _, err := b.v.sellSources(0, nil); err == nil {
		t.Error("no error for an assignment without a fact")
	}
	p.settle("expired", nil)
	if _, err := b.v.sellSources(0, nil); err != nil {
		t.Errorf("expired position: %v", err)
	}
}
