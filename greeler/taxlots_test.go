// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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

// lotStock is a fakeStock that resolves and sells by tax lots, one lot per
// order, named after it.
type lotStock struct {
	*fakeStock

	lmu       sync.Mutex
	resolveOK bool
	resolves  int
	sells     [][]gobs.TaxLotID // the lots of each sell placed
}

func newLotStock(s *fakeStock) *lotStock {
	return &lotStock{fakeStock: s, resolveOK: true}
}

func (p *lotStock) ResolveTaxLots(ctx context.Context, serverIDs []string) ([]gobs.TaxLotID, decimal.Decimal, error) {
	p.lmu.Lock()
	defer p.lmu.Unlock()
	p.resolves++
	if !p.resolveOK {
		return nil, decimal.Zero, errors.New("lots are not posted yet")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var lots []gobs.TaxLotID
	var sum decimal.Decimal
	for _, id := range serverIDs {
		if o := p.find(id); o != nil && o.FilledSize.IsPositive() {
			lots = append(lots, gobs.TaxLotID("lot-"+id))
			sum = sum.Add(o.FilledSize)
		}
	}
	return lots, sum, nil
}

func (p *lotStock) LimitSellTaxLots(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal, lots []gobs.TaxLotID) (exchange.Order, error) {
	p.lmu.Lock()
	p.sells = append(p.sells, slices.Clone(lots))
	p.lmu.Unlock()
	return p.place("SELL", clientID, size, price)
}

func (p *lotStock) setResolveOK(ok bool) {
	p.lmu.Lock()
	defer p.lmu.Unlock()
	p.resolveOK = ok
}

func (p *lotStock) sellLots() [][]gobs.TaxLotID {
	p.lmu.Lock()
	defer p.lmu.Unlock()
	return slices.Clone(p.sells)
}

func newLotEnv(t *testing.T) (*testEnv, *lotStock) {
	e := newTestEnv(t)
	ls := newLotStock(e.stock)
	e.rt.Product = ls
	return e, ls
}

func waitLevelDone(t *testing.T, r *runner, level int) {
	t.Helper()
	rl := r.running[level]
	if rl == nil {
		t.Fatalf("level %d has no running limiter", level)
	}
	select {
	case <-rl.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("level %d limiter did not return", level)
	}
}

func waitSellOrder(t *testing.T, e *testEnv) {
	t.Helper()
	waitFor(t, "a sell order", func() bool {
		for _, o := range e.stock.live() {
			if o.Side == "SELL" {
				return true
			}
		}
		return false
	})
}

// buyLevel0 fills level 0's buy and returns once its limiter has.
func buyLevel0(t *testing.T, e *testEnv, r *runner) {
	t.Helper()
	e.stock.setPrice("100.5")
	e.step(r, "100.5")
	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
	e.stock.fillLive()
	waitLevelDone(t, r, 0)
}

func checkSellLots(t *testing.T, ls *lotStock, want ...string) {
	t.Helper()
	sells := ls.sellLots()
	if len(sells) == 0 {
		t.Fatal("no sell placed")
	}
	var w []gobs.TaxLotID
	for _, l := range want {
		w = append(w, gobs.TaxLotID(l))
	}
	if got := sells[len(sells)-1]; !slices.Equal(got, w) {
		t.Errorf("sell lots = %v, want %v", got, w)
	}
}

// TestSellNamesBuyLots: a level's sell names the lots its buy resolved.
func TestSellNamesBuyLots(t *testing.T) {
	e, ls := newLotEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)

	buyLevel0(t, e, r)
	e.step(r, "100.5")
	waitSellOrder(t, e)
	checkSellLots(t, ls, "lot-order-0")

	// The lots are on the sell's record.
	w := e.reload(v)
	if got := w.epochs[0].limiters[0][1].TaxLotIDs(); len(got) != 1 || got[0] != "lot-order-0" {
		t.Errorf("saved sell lots = %v", got)
	}
}

// TestSellWaitsForBuyLots: a buy whose lots didn't resolve holds its
// level's sell until they do.
func TestSellWaitsForBuyLots(t *testing.T) {
	e, ls := newLotEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)

	ls.setResolveOK(false)
	buyLevel0(t, e, r)
	r.reap()
	e.clock.Add(retryDelay)
	e.step(r, "100.5")
	if ids := v.current().LevelLimiterIDs[0]; len(ids) != 1 {
		t.Fatalf("level 0 limiters = %v, want only the buy", ids)
	}
	if _, ok := r.retryAt[0]; !ok {
		t.Error("level 0 has no retry set")
	}

	// Within the retry delay, the level doesn't ask again.
	ls.setResolveOK(true)
	n := ls.resolves
	e.step(r, "100.5")
	if ls.resolves != n || len(v.current().LevelLimiterIDs[0]) != 1 {
		t.Fatalf("level 0 retried early")
	}

	e.clock.Add(retryDelay)
	e.step(r, "100.5")
	waitSellOrder(t, e)
	checkSellLots(t, ls, "lot-order-0")
	w := e.reload(v)
	if got := w.epochs[0].limiters[0][0].TaxLotIDs(); len(got) != 1 {
		t.Errorf("saved buy lots = %v", got)
	}
}

// TestResumedSellGetsLots: a sell made without lots (before this code, or
// on a product without them) gets the level's lots when it resumes, unless
// it has filled.
func TestResumedSellGetsLots(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%v", partial), func(t *testing.T) {
			e := newTestEnv(t)
			v := e.newGreeler(testConfig())
			r := e.runner(v)
			buyLevel0(t, e, r)
			e.step(r, "100.5")
			waitSellOrder(t, e)
			if partial {
				e.stock.mu.Lock()
				for _, o := range e.stock.orders {
					if !o.Done {
						o.FilledSize, o.FilledPrice = d("10"), o.price
					}
				}
				e.stock.mu.Unlock()
			}
			r.stopAll()

			ls := newLotStock(e.stock)
			e.rt.Product = ls
			w := e.reload(v)
			r2 := e.runner(w)
			e.step(r2, "100.5")
			waitSellOrder(t, e)
			if partial {
				checkSellLots(t, ls)
				if got := w.epochs[0].limiters[0][1].TaxLotIDs(); len(got) != 0 {
					t.Errorf("partly filled sell got lots %v", got)
				}
				return
			}
			checkSellLots(t, ls, "lot-order-0")
		})
	}
}

// TestPutAssignedSharesSellWithoutLots: assigned shares have no lots to
// name, so their sells go out without any.
func TestPutAssignedSharesSellWithoutLots(t *testing.T) {
	e, ls := newLotEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)
	pos.settle("assigned", &gobs.AssignmentFact{Key: "tx1", Shares: d("100"), Price: d("100")})
	e.step(r, "99")
	if v.Mode() != "grid" {
		t.Fatalf("mode %s after assignment", v.Mode())
	}
	e.stock.setPrice("99")
	e.step(r, "99")
	waitFor(t, "three sells", func() bool { return len(e.stock.live()) == 3 })
	sells := ls.sellLots()
	if len(sells) != 3 {
		t.Fatalf("sells = %v", sells)
	}
	for _, s := range sells {
		if len(s) != 0 {
			t.Errorf("sell lots = %v, want none", s)
		}
	}
	if ls.resolves != 0 {
		t.Errorf("resolved lots %d times for assigned shares", ls.resolves)
	}
}
