// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// fakeExchange is an options exchange with one stock product.
type fakeExchange struct {
	exchange.OptionsExchange // unused methods panic
}

func (f *fakeExchange) ExchangeName() string       { return "fake" }
func (f *fakeExchange) CanDedupOnClientUUID() bool { return false }

// fakeStock is the underlying's stock product. Orders rest until the test
// fills them.
type fakeStock struct {
	exchange.Product // unused methods panic

	prices  *topic.Topic[exchange.PriceUpdate]
	updates *topic.Topic[exchange.OrderUpdate]

	mu           sync.Mutex
	orders       []*fakeOrder
	fillOnCancel bool          // a fill races the cancel
	cancelErrs   int           // cancels left to fail
	blockGet     chan struct{} // the next Get closes it, then waits for its ctx
}

type fakeOrder struct {
	exchange.SimpleOrder
	size, price decimal.Decimal
}

func newFakeStock() *fakeStock {
	return &fakeStock{
		prices:  topic.New[exchange.PriceUpdate](),
		updates: topic.New[exchange.OrderUpdate](),
	}
}

func (p *fakeStock) ProductID() string            { return "AAPL" }
func (p *fakeStock) ExchangeName() string         { return "fake" }
func (p *fakeStock) BaseMinSize() decimal.Decimal { return d("1") }
func (p *fakeStock) Close() error                 { return nil }

func (p *fakeStock) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	return topic.Subscribe(p.prices, 0, true)
}

func (p *fakeStock) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	return topic.Subscribe(p.updates, 0, false)
}

func (p *fakeStock) setPrice(price string) {
	p.prices.Send(&exchange.SimpleTicker{Price: d(price), ServerTime: exchange.RemoteTime{Time: time.Now()}})
}

func (p *fakeStock) place(side string, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := &fakeOrder{size: size, price: price}
	o.ServerOrderID = fmt.Sprintf("order-%d", len(p.orders))
	o.ClientUUID = clientID
	o.Side = side
	o.CreateTime = gobs.RemoteTime{Time: time.Now()}
	o.Status = "OPEN"
	p.orders = append(p.orders, o)
	dup := o.SimpleOrder
	return &dup, nil
}

func (p *fakeStock) LimitBuy(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	return p.place("BUY", clientID, size, price)
}

func (p *fakeStock) LimitSell(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	return p.place("SELL", clientID, size, price)
}

func (p *fakeStock) find(id string) *fakeOrder {
	for _, o := range p.orders {
		if o.ServerOrderID == id {
			return o
		}
	}
	return nil
}

func (p *fakeStock) fillLocked(o *fakeOrder) {
	o.FilledSize = o.size
	o.FilledPrice = o.price
	o.Done, o.Status = true, "FILLED"
	dup := o.SimpleOrder
	p.updates.Send(&dup)
}

func (p *fakeStock) Cancel(ctx context.Context, serverID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := p.find(serverID)
	if o == nil {
		return os.ErrNotExist
	}
	if o.Done {
		return errors.New("order is already done")
	}
	if p.cancelErrs > 0 {
		p.cancelErrs--
		return errors.New("cancel failed")
	}
	if p.fillOnCancel {
		p.fillLocked(o)
		return nil
	}
	o.Done, o.Status = true, "CANCELLED"
	return nil
}

func (p *fakeStock) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	p.mu.Lock()
	if inflight := p.blockGet; inflight != nil {
		// Like an HTTP call interrupted by its context: the error wraps the
		// context's cause.
		p.blockGet = nil
		p.mu.Unlock()
		close(inflight)
		<-ctx.Done()
		return nil, fmt.Errorf("get %s: %w", serverID, context.Cause(ctx))
	}
	defer p.mu.Unlock()
	o := p.find(serverID)
	if o == nil {
		return nil, os.ErrNotExist
	}
	dup := o.SimpleOrder
	return &dup, nil
}

// live returns the live orders.
func (p *fakeStock) live() []fakeOrder {
	p.mu.Lock()
	defer p.mu.Unlock()
	var live []fakeOrder
	for _, o := range p.orders {
		if !o.Done {
			live = append(live, *o)
		}
	}
	return live
}

// blockNextGet makes the next Get wait until its ctx is done. The returned
// channel is closed once that Get is in flight.
func (p *fakeStock) blockNextGet() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockGet = make(chan struct{})
	return p.blockGet
}

// failCancels makes the next n cancels fail, leaving their orders live.
func (p *fakeStock) failCancels(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancelErrs = n
}

// expireLive ends every live order at the exchange without an update,
// like a DAY order expiring while nobody watches.
func (p *fakeStock) expireLive() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.orders {
		if !o.Done {
			o.Done, o.Status = true, "EXPIRED"
		}
	}
}

// fillLive fills every live order.
func (p *fakeStock) fillLive() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, o := range p.orders {
		if !o.Done {
			p.fillLocked(o)
			n++
		}
	}
	return n
}

// fakePosition stands in for optpos.Position.
type fakePosition struct {
	uid string

	mu         sync.Mutex
	outcome    string
	outcomeAt  time.Time
	assignment *gobs.AssignmentFact
	premiums   []*optpos.Premium
	contract   *gobs.OptionContract // set by Open
	filled     bool                 // the opening order filled; Abandon fails
	opens      []*optpos.Constraint
	checks     int
	abandons   int
	stops      int
}

func (p *fakePosition) UID() string { return p.uid }

func (p *fakePosition) Outcome() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.outcome
}

func (p *fakePosition) Assignment() *gobs.AssignmentFact {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.assignment
}

func (p *fakePosition) Contract() *gobs.OptionContract {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contract
}

func (p *fakePosition) Facts() *optpos.Facts {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &optpos.Facts{Outcome: p.outcome, OutcomeAt: p.outcomeAt, Assignment: p.assignment, Premiums: p.premiums}
}

func (p *fakePosition) Open(ctx, fctx context.Context, c *optpos.Constraint) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens = append(p.opens, c)
	p.contract = &gobs.OptionContract{ContractID: p.uid + "/contract", Underlying: c.Underlying, OptionType: c.OptionType}
	return nil
}

func (p *fakePosition) Check(ctx, fctx context.Context, c *optpos.Constraint) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks++
	return nil
}

func (p *fakePosition) Abandon(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.abandons++
	if p.filled {
		return optpos.ErrOpened
	}
	p.outcome = "unfilled"
	return nil
}

func (p *fakePosition) Stop(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	return nil
}

func (p *fakePosition) settle(outcome string, fact *gobs.AssignmentFact) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.outcome, p.assignment = outcome, fact
}

func (p *fakePosition) Save(ctx context.Context, rw kv.ReadWriter) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	gv := &gobs.OptPositionState{V1: &gobs.OptPositionStateV1{
		Config:   &gobs.OptPositionConfig{ExchangeName: "fake", Underlying: "AAPL"},
		Progress: &gobs.OptPositionProgress{Outcome: p.outcome, OutcomeAt: p.outcomeAt, Assignment: p.assignment},
	}}
	return kvutil.Set(ctx, rw, path.Join(optpos.DefaultKeyspace, p.uid), gv)
}

func newTestDB(t *testing.T) kv.Database {
	bdb, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bdb.Close() })
	return kvbadger.New(bdb, func(string) bool { return true })
}

// level is a 25-share level buying at buy and selling one dollar higher,
// with cancel prices five dollars away.
func level(buy string) *gobs.Pair {
	b := d(buy)
	s := b.Add(d("1"))
	return &gobs.Pair{
		Buy:  gobs.Point{Size: d("25"), Price: b, Cancel: b.Add(d("5"))},
		Sell: gobs.Point{Size: d("25"), Price: s, Cancel: s.Sub(d("5"))},
	}
}

func testConfig() *gobs.GreelConfig {
	return &gobs.GreelConfig{
		ProductID:     "AAPL",
		ExchangeName:  "fake",
		GridLevels:    []*gobs.Pair{level("100"), level("101"), level("102"), level("103")},
		GridPct:       d("5"),
		FarPct:        d("10"),
		HysteresisPct: d("3"),
		DwellTime:     time.Hour,
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Add(dt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(dt)
}

type testEnv struct {
	t     *testing.T
	db    kv.Database
	stock *fakeStock
	ex    *fakeExchange
	rt    *trader.Runtime
	clock *testClock

	mu        sync.Mutex
	positions map[string]*fakePosition
}

func newTestEnv(t *testing.T) *testEnv {
	e := &testEnv{
		t:         t,
		db:        newTestDB(t),
		stock:     newFakeStock(),
		ex:        &fakeExchange{},
		clock:     &testClock{now: time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)},
		positions: make(map[string]*fakePosition),
	}
	e.rt = &trader.Runtime{Exchange: e.ex, Database: e.db, Product: e.stock}
	return e
}

// hook makes v use the test clock and fake positions.
func (e *testEnv) hook(v *Greeler) *Greeler {
	v.now = e.clock.Now
	v.newPosition = func(uid string, optEx exchange.OptionsExchange, db kv.Database) position {
		e.mu.Lock()
		defer e.mu.Unlock()
		p := &fakePosition{uid: uid}
		e.positions[uid] = p
		return p
	}
	v.loadPosition = func(ctx context.Context, uid string, r kv.Reader, optEx exchange.OptionsExchange, db kv.Database) (position, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		p, ok := e.positions[uid]
		if !ok {
			return nil, os.ErrNotExist
		}
		return p, nil
	}
	return v
}

func (e *testEnv) newGreeler(cfg *gobs.GreelConfig) *Greeler {
	v, err := New(uuid.NewString(), cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.hook(v)
}

// runner returns a runner whose children stop when the test ends.
func (e *testEnv) runner(v *Greeler) *runner {
	ctx, cancel := context.WithCancel(context.Background())
	if err := v.loadChildren(ctx, e.db, e.ex); err != nil {
		e.t.Fatal(err)
	}
	r := v.newRunner(ctx, e.rt, e.ex)
	e.t.Cleanup(func() {
		r.stopAll()
		cancel()
	})
	return r
}

func (e *testEnv) step(r *runner, spot string) bool {
	e.t.Helper()
	r.spot = d(spot)
	done, err := r.step(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return done
}

func (e *testEnv) reload(v *Greeler) *Greeler {
	e.t.Helper()
	var w *Greeler
	if err := kv.WithReader(context.Background(), e.db, func(ctx context.Context, r kv.Reader) (err error) {
		w, err = Load(ctx, v.UID(), r)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return e.hook(w)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitIdle waits until every running limiter has returned.
func waitIdle(t *testing.T, r *runner) {
	t.Helper()
	for i, rl := range r.running {
		select {
		case <-rl.done:
		case <-time.After(5 * time.Second):
			t.Fatalf("level %d limiter did not return", i)
		}
	}
}

func holdingsOf(t *testing.T, v *Greeler) []string {
	t.Helper()
	hs, err := v.fold()
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, h := range hs {
		s = append(s, h.String())
	}
	return s
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	for name, edit := range map[string]func(c *gobs.GreelConfig){
		"too few shares":  func(c *gobs.GreelConfig) { c.GridLevels = c.GridLevels[:3] },
		"unequal sizes":   func(c *gobs.GreelConfig) { c.GridLevels[1].Sell.Size = d("20") },
		"not ascending":   func(c *gobs.GreelConfig) { c.GridLevels[1], c.GridLevels[2] = c.GridLevels[2], c.GridLevels[1] },
		"far below grid":  func(c *gobs.GreelConfig) { c.FarPct = d("4") },
		"hysteresis wide": func(c *gobs.GreelConfig) { c.HysteresisPct = d("10") },
		"no product":      func(c *gobs.GreelConfig) { c.ProductID = "" },
		"no selector":     func(c *gobs.GreelConfig) { c.ContractSelector = "nonesuch" },
		"nil level":       func(c *gobs.GreelConfig) { c.GridLevels[2] = nil },
	} {
		c := testConfig()
		edit(c)
		if _, err := New(uuid.NewString(), c); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
	if _, err := New("not-a-uuid", testConfig()); err == nil {
		t.Errorf("New accepted a uid without an uuid")
	}
}

func TestAttribute(t *testing.T) {
	c := testConfig()
	c.GridLevels = append(c.GridLevels, level("104"))
	v, err := New(uuid.NewString(), c)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := v.attribute(d("100"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(parts); got != "[25 25 25 25 0]" {
		t.Errorf("attribute(100) = %s", got)
	}
	parts, err = v.attribute(d("110"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(parts); got != "[25 25 25 25 10]" {
		t.Errorf("attribute(110) = %s", got)
	}
	if _, err := v.attribute(d("126")); err == nil {
		t.Errorf("attribute(126) succeeded beyond the levels' 125 shares")
	}
}

func TestZones(t *testing.T) {
	v, err := New(uuid.NewString(), testConfig()) // levels 100..104, grid 5%, far 10%, hysteresis 3%
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		spot, want string
	}{
		{"102", ""},
		{"115", ""},             // 104 > 115*0.90 = 103.5
		{"115.56", "wheel-put"}, // 104 <= 104.004
		{"91", ""},              // 100 < 100.1
		{"90.9", "wheel-call"},
	} {
		if got := v.wheelSide(d(tc.spot)); got != tc.want {
			t.Errorf("wheelSide(%s) = %q, want %q", tc.spot, got, tc.want)
		}
	}
	// Back within far-hysteresis (7%): put side needs 104 > spot*0.93.
	if v.backToGrid("PUT", d("112")) {
		t.Errorf("backToGrid(PUT, 112) = true, want false")
	}
	if !v.backToGrid("PUT", d("111.8")) {
		t.Errorf("backToGrid(PUT, 111.8) = false, want true")
	}
	if !v.inGridBand(d("104"), d("100")) || v.inGridBand(d("105.1"), d("100")) {
		t.Errorf("inGridBand is wrong around 5%% of 100")
	}
	put, call := v.constraint("PUT"), v.constraint("CALL")
	if !put.MaxStrike.Equal(d("100")) || !put.MinStrike.IsZero() || put.Underlying != "AAPL" {
		t.Errorf("put constraint = %+v", put)
	}
	if !call.MinStrike.Equal(d("104")) || !call.MaxStrike.IsZero() {
		t.Errorf("call constraint = %+v", call)
	}
}

func TestSetOption(t *testing.T) {
	v, err := New(uuid.NewString(), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	undo, err := v.SetOption("freeze", "grid")
	if err != nil || undo != "undo:none" || !v.freezeGridOpt || v.freezeWheelOpt {
		t.Fatalf("freeze=grid: undo %q err %v", undo, err)
	}
	if undo, err = v.SetOption("freeze", "all"); err != nil || undo != "undo:grid" || !v.freezeWheelOpt {
		t.Fatalf("freeze=all: undo %q err %v", undo, err)
	}
	if _, err := v.SetOption("freeze", undo); err != nil || v.currentFreezeValue() != "grid" {
		t.Fatalf("undo freeze: %v, now %s", err, v.currentFreezeValue())
	}
	if _, err := v.SetOption("freeze", "sells"); err == nil {
		t.Errorf("freeze=sells succeeded")
	}
	if undo, err = v.SetOption("retire", "true"); err != nil || undo != "undo" || !v.retireOpt {
		t.Fatalf("retire=true: undo %q err %v", undo, err)
	}
	if _, err := v.SetOption("retire", "false"); err == nil {
		t.Errorf("retire=false undid a retire")
	}
	if _, err := v.SetOption("bogus", "1"); err == nil {
		t.Errorf("unknown option succeeded")
	}
}

func TestSaveLoad(t *testing.T) {
	e := newTestEnv(t)
	c := testConfig()
	c.WheelKnobs = &gobs.WheelKnobs{MinDTE: 20, MaxDTE: 45, RepriceInterval: time.Minute}
	v := e.newGreeler(c)
	if _, err := v.SetOption("freeze", "wheel"); err != nil {
		t.Fatal(err)
	}
	if err := kv.WithReadWriter(context.Background(), e.db, v.Save); err != nil {
		t.Fatal(err)
	}
	w := e.reload(v)
	if w.Mode() != "grid" || len(w.epochs) != 1 || len(w.epochs[0].LevelLimiterIDs) != 4 {
		t.Errorf("reloaded epochs = %+v", w.epochs)
	}
	if w.currentFreezeValue() != "wheel" || w.cfg.WheelKnobs.MaxDTE != 45 || w.selector == nil {
		t.Errorf("reloaded freeze %s knobs %+v selector %v", w.currentFreezeValue(), w.cfg.WheelKnobs, w.selector)
	}
	// The config is copied, not shared with the caller.
	c.GridLevels[0].Buy.Price = d("1")
	if !v.levels[0].Buy.Price.Equal(d("100")) || !v.cfg.GridLevels[0].Buy.Price.Equal(d("100")) {
		t.Errorf("greeler shares its caller's config")
	}
}

// TestGridCycle runs a level through buy, then sell, with each limiter
// recorded in the epoch before it runs.
func TestGridCycle(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	e.step(r, "100.5")
	if len(r.running) != 4 {
		t.Fatalf("running limiters = %d, want 4 buys", len(r.running))
	}
	// Only level 0's buy is near the ticker: [100, 105).
	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
	if o := e.stock.live()[0]; o.Side != "BUY" || !o.price.Equal(d("100")) || !o.size.Equal(d("25")) {
		t.Fatalf("live order = %+v", o)
	}
	e.stock.fillLive()
	waitFor(t, "level 0 buy to finish", func() bool {
		select {
		case <-r.running[0].done:
			return true
		default:
			return false
		}
	})

	time.Sleep(10 * time.Millisecond)
	afterBuy := time.Now()
	e.step(r, "100.5")
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[25 0 0 0]" {
		t.Fatalf("holdings = %s", got)
	}
	ids := v.current().LevelLimiterIDs[0]
	if len(ids) != 2 || path.Base(ids[1]) != "sell-000001" {
		t.Fatalf("level 0 limiters = %v", ids)
	}
	// The sell is saved with the greeler before it runs.
	w := e.reload(v)
	if got := w.epochs[0].LevelLimiterIDs[0]; len(got) != 2 || got[1] != ids[1] {
		t.Fatalf("persisted level 0 limiters = %v", got)
	}

	// The sell (101, cancel 96) places at once and fills; level 0 is flat.
	waitFor(t, "level 0 sell order", func() bool {
		for _, o := range e.stock.live() {
			if o.Side == "SELL" {
				return true
			}
		}
		return false
	})
	e.stock.fillLive()
	waitFor(t, "level 0 sell to finish", func() bool {
		select {
		case <-r.running[0].done:
			return true
		default:
			return false
		}
	})
	e.step(r, "100.5")
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[0 0 0 0]" {
		t.Fatalf("holdings after sell = %s", got)
	}
	if ids := v.current().LevelLimiterIDs[0]; len(ids) != 3 || path.Base(ids[2]) != "buy-000002" {
		t.Fatalf("level 0 limiters after sell = %v", ids)
	}
	if got, ok := v.DerivedStock(); !ok || !got.IsZero() {
		t.Errorf("DerivedStock = %s, want 0", got)
	}

	// A loaded greeler that isn't running reports its fills.
	w = e.reload(v)
	if s := w.GetSummary(nil); !s.BoughtSize.Equal(d("25")) || !s.SoldSize.Equal(d("25")) || !s.UnsoldSize.IsZero() {
		t.Errorf("loaded summary: bought %s sold %s unsold %s", s.BoughtSize, s.SoldSize, s.UnsoldSize)
	}
	// A range holding only the sell pairs it with its buy, not oversold.
	s := w.GetSummary(&timerange.Range{Begin: afterBuy})
	if !s.SoldSize.Equal(d("25")) || !s.BoughtSize.Equal(d("25")) || !s.OversoldSize.IsZero() {
		t.Errorf("ranged summary: bought %s sold %s oversold %s", s.BoughtSize, s.SoldSize, s.OversoldSize)
	}
}

// TestNewLimitersOnlyInGridBand checks levels outside spot±grid start no
// limiter.
func TestNewLimitersOnlyInGridBand(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("107")

	e.step(r, "107") // band [101.65, 112.35]: levels 2 and 3 buy
	if len(r.running) != 2 || r.running[2] == nil || r.running[3] == nil {
		t.Fatalf("running levels = %v", r.running)
	}
}

func flipToPut(t *testing.T, e *testEnv, v *Greeler, r *runner) *fakePosition {
	t.Helper()
	e.step(r, "200")
	if e := v.current(); e.PendingFlip != "wheel-put" || v.Mode() != "grid" {
		t.Fatalf("after first far step: mode %s pending %q", v.Mode(), e.PendingFlip)
	}
	e.clock.Add(30 * time.Minute)
	e.step(r, "200")
	if v.Mode() != "grid" {
		t.Fatalf("flipped before the dwell time")
	}
	e.clock.Add(30 * time.Minute)
	e.step(r, "200")
	if v.Mode() != "wheel" {
		t.Fatalf("did not flip after the dwell time")
	}
	return e.positions[v.current().PositionID]
}

func TestFlipToWheelPut(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)

	pos := flipToPut(t, e, v, r)
	if len(pos.opens) != 1 || pos.opens[0].OptionType != "PUT" || !pos.opens[0].MaxStrike.Equal(d("100")) || !pos.opens[0].ContractSize.Equal(d("100")) {
		t.Fatalf("opens = %+v", pos.opens)
	}
	if path.Base(pos.uid) != "pos-000001" {
		t.Errorf("position uid = %s", pos.uid)
	}
	if id, known := v.HeldContract(); !known || id != pos.uid+"/contract" {
		t.Errorf("HeldContract = %q, %v; want the opened contract", id, known)
	}
	// The wheel epoch and the position were saved ahead of Open.
	w := e.reload(v)
	if w.Mode() != "wheel" || w.epochs[1].PositionID != pos.uid {
		t.Fatalf("persisted epochs = %+v", w.epochs)
	}
	if _, err := kvutil.GetDB[gobs.OptPositionState](context.Background(), e.db, path.Join(optpos.DefaultKeyspace, pos.uid)); err != nil {
		t.Fatalf("position record: %v", err)
	}

	// While open, every iteration checks the position.
	e.step(r, "200")
	if pos.checks != 1 {
		t.Errorf("checks = %d, want 1", pos.checks)
	}
}

func TestDwellDoneNeedsTheSameSide(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	now := e.clock.Now()
	ep := v.current()
	ep.PendingFlip, ep.PendingFlipAt = "wheel-call", now.Add(-2*time.Hour)
	if v.dwellDone(ep, "wheel-put", now) {
		t.Error("a call's dwell clock let a put flip")
	}
	if !v.dwellDone(ep, "wheel-call", now) {
		t.Error("the call's own dwell clock didn't count")
	}
}

func TestDwellClockResets(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)

	e.step(r, "200")
	e.clock.Add(50 * time.Minute)
	e.step(r, "110") // back inside far
	if p := v.current().PendingFlip; p != "" {
		t.Fatalf("pending flip = %q after spot came back", p)
	}
	e.step(r, "200")
	e.clock.Add(50 * time.Minute)
	e.step(r, "200")
	if v.Mode() != "grid" {
		t.Fatalf("flipped before a full dwell")
	}
}

func TestFreezeWheelHoldsFlip(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	if _, err := v.SetOption("freeze", "wheel"); err != nil {
		t.Fatal(err)
	}
	r := e.runner(v)
	e.step(r, "200")
	e.clock.Add(2 * time.Hour)
	e.step(r, "200")
	if v.Mode() != "grid" || v.current().PendingFlip != "wheel-put" {
		t.Fatalf("mode %s pending %q, want grid with the clock running", v.Mode(), v.current().PendingFlip)
	}
}

// TestFlipBlockedByRacingFill: a buy fills while being canceled for the
// flip, so the levels no longer qualify.
func TestFlipBlockedByRacingFill(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	e.step(r, "100.5")
	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })

	e.stock.mu.Lock()
	e.stock.fillOnCancel = true
	e.stock.mu.Unlock()
	// Spot as the greeler sees it jumps far above the levels.
	e.step(r, "200")
	e.clock.Add(time.Hour)
	e.step(r, "200")

	if v.Mode() != "grid" {
		t.Fatalf("flipped despite the racing fill")
	}
	if p := v.current().PendingFlip; p != "" {
		t.Errorf("pending flip = %q, want the dwell clock reset", p)
	}
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[25 0 0 0]" {
		t.Errorf("holdings = %s", got)
	}
	if len(r.running) != 0 {
		t.Errorf("limiters still running after the flip attempt: %d", len(r.running))
	}
}

// TestFlipWaitsForFailedCancel: a buy's cancel fails during the flip, so its
// order is still live. The flip waits until the limiter has run again and
// canceled it.
func TestFlipWaitsForFailedCancel(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	e.step(r, "100.5")
	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
	e.step(r, "200")
	e.clock.Add(time.Hour)

	e.stock.failCancels(1)
	e.step(r, "200")
	if v.Mode() != "grid" || len(e.stock.live()) != 1 {
		t.Fatalf("mode %s with %d live orders after a failed cancel; want grid with the buy live", v.Mode(), len(e.stock.live()))
	}
	if p := v.current().PendingFlip; p != "wheel-put" {
		t.Errorf("pending flip = %q, want the dwell clock kept", p)
	}

	// The level waits out its retry, then its limiter runs again.
	e.step(r, "200")
	if v.Mode() != "grid" || r.running[0] != nil {
		t.Fatalf("mode %s, level 0 running %v during the retry wait", v.Mode(), r.running[0] != nil)
	}
	e.clock.Add(retryDelay)
	e.step(r, "200")
	if v.Mode() != "grid" || r.running[0] == nil {
		t.Fatalf("mode %s, level 0 running %v after the retry wait; want its limiter running", v.Mode(), r.running[0] != nil)
	}

	// The next attempt stops it, which cancels the buy, and flips.
	e.step(r, "200")
	if v.Mode() != "wheel" {
		t.Fatalf("did not flip once the cancel succeeded")
	}
	if live := e.stock.live(); len(live) != 0 {
		t.Errorf("orders still live after the flip: %+v", live)
	}
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[0 0 0 0]" {
		t.Errorf("holdings = %s", got)
	}
}

// crashWithLiveBuy leaves a greeler as a crash would: level 0's buy is live
// at the exchange and saved, and the put's dwell clock is saved and due. It
// returns the greeler as loaded on restart.
func crashWithLiveBuy(t *testing.T, e *testEnv) *Greeler {
	t.Helper()
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	e.step(r, "100.5")
	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
	e.step(r, "200") // starts and saves the dwell clock

	// The limiters stop without canceling the buy, which stays saved.
	l := r.running[0].limiter
	e.stock.failCancels(1)
	r.stopAll()
	if err := kv.WithReadWriter(context.Background(), e.db, l.Save); err != nil {
		t.Fatal(err)
	}
	if n := len(e.stock.live()); n != 1 {
		t.Fatalf("live orders = %d, want the buy", n)
	}
	e.clock.Add(time.Hour)
	return e.reload(v)
}

// TestFlipAfterRestartWaitsForLimiters: after a restart the dwell clock is
// already due, but no limiter has run yet to settle its order.
func TestFlipAfterRestartWaitsForLimiters(t *testing.T) {
	t.Run("filled while down", func(t *testing.T) {
		e := newTestEnv(t)
		v := crashWithLiveBuy(t, e)
		e.stock.fillLive()
		r := e.runner(v)

		e.step(r, "200")
		if v.Mode() != "grid" || r.running[0] == nil {
			t.Fatalf("mode %s, level 0 running %v on the first step; want grid with the buy recovering", v.Mode(), r.running[0] != nil)
		}
		waitFor(t, "level 0 buy to finish", func() bool {
			select {
			case <-r.running[0].done:
				return true
			default:
				return false
			}
		})
		e.step(r, "200")
		if v.Mode() != "grid" {
			t.Fatalf("flipped with level 0 holding the buy's shares")
		}
		if got := fmt.Sprint(holdingsOf(t, v)); got != "[25 0 0 0]" {
			t.Errorf("holdings = %s", got)
		}
	})

	t.Run("still live", func(t *testing.T) {
		e := newTestEnv(t)
		v := crashWithLiveBuy(t, e)
		r := e.runner(v)

		e.step(r, "200")
		if v.Mode() != "grid" {
			t.Fatalf("flipped before the buy was canceled")
		}
		e.step(r, "200")
		if v.Mode() != "wheel" {
			t.Fatalf("did not flip once the buy was canceled")
		}
		if live := e.stock.live(); len(live) != 0 {
			t.Errorf("orders still live after the flip: %+v", live)
		}
	})

	// The next flip attempt stops the limiter while it is still fetching its
	// order: it returns an error wrapping the stop cause without having
	// canceled anything.
	t.Run("stopped while recovering", func(t *testing.T) {
		e := newTestEnv(t)
		v := crashWithLiveBuy(t, e)
		r := e.runner(v)

		inflight := e.stock.blockNextGet()
		e.step(r, "200")
		select {
		case <-inflight:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the limiter to fetch its order")
		}
		e.step(r, "200")
		if v.Mode() != "grid" || len(e.stock.live()) != 1 {
			t.Fatalf("mode %s with %d live orders; want grid with the buy still live", v.Mode(), len(e.stock.live()))
		}

		// After the retry delay the limiter runs again, and the flip after
		// that cancels the buy.
		e.clock.Add(retryDelay)
		e.step(r, "200")
		e.step(r, "200")
		if v.Mode() != "wheel" || len(e.stock.live()) != 0 {
			t.Fatalf("mode %s with %d live orders; want a put and no orders", v.Mode(), len(e.stock.live()))
		}
	})
}

// TestPutAssignedSellsAtLevels: an assigned put brings 100 shares, lowest
// level first, and each level then sells what it holds.
func TestPutAssignedSellsAtLevels(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)

	pos.settle("assigned", &gobs.AssignmentFact{Key: "tx1", Shares: d("100"), Price: d("100")})
	e.step(r, "99")
	if v.Mode() != "grid" || len(v.epochs) != 3 {
		t.Fatalf("mode %s epochs %d after assignment", v.Mode(), len(v.epochs))
	}
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[25 25 25 25]" {
		t.Fatalf("holdings = %s", got)
	}
	if id, known := v.HeldContract(); !known || id != "" {
		t.Errorf("HeldContract = %q, %v after assignment; want none", id, known)
	}

	e.stock.setPrice("99")
	e.step(r, "99") // band [94.05, 103.95]: sells at 101..103 start; 104 waits
	if len(r.running) != 3 || r.running[3] != nil {
		t.Fatalf("running levels = %v", r.running)
	}
	for i := 0; i < 3; i++ {
		l := r.running[i].limiter
		if !l.IsSell() || !l.PendingSize().Equal(d("25")) {
			t.Errorf("level %d limiter %s is not a 25-share sell", i, l.UID())
		}
	}
	if got, ok := v.DerivedStock(); !ok || !got.Equal(d("100")) {
		t.Errorf("DerivedStock = %s, want 100", got)
	}

	// Restart: the fold replays the assignment from the position.
	w := e.reload(v)
	e.runner(w)
	if got := fmt.Sprint(holdingsOf(t, w)); got != "[25 25 25 25]" {
		t.Errorf("holdings after reload = %s", got)
	}
}

func TestCallAssignedEmptiesLevels(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	// Fill every level's buy by walking the ticker up through the levels.
	e.step(r, "101.5")
	for _, p := range []string{"100.5", "101.5", "102.5", "103.5"} {
		e.stock.setPrice(p)
		waitFor(t, "a buy at "+p, func() bool { return len(e.stock.live()) == 1 })
		e.stock.fillLive()
	}
	waitIdle(t, r)
	r.reap()
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[25 25 25 25]" {
		t.Fatalf("holdings = %s", got)
	}

	// Spot falls far below; the sells (cancel 96..99) never place.
	e.stock.setPrice("80")
	e.step(r, "80")
	e.clock.Add(time.Hour)
	e.step(r, "80")
	if v.Mode() != "wheel" {
		t.Fatalf("did not flip to a call")
	}
	pos := e.positions[v.current().PositionID]
	if len(pos.opens) != 1 || pos.opens[0].OptionType != "CALL" || !pos.opens[0].MinStrike.Equal(d("104")) {
		t.Fatalf("opens = %+v", pos.opens)
	}

	pos.settle("assigned", &gobs.AssignmentFact{Key: "tx2", Shares: d("-100"), Price: d("105")})
	e.step(r, "106")
	if got := fmt.Sprint(holdingsOf(t, v)); got != "[0 0 0 0]" {
		t.Fatalf("holdings after call assignment = %s", got)
	}
}

// fillLevel0 steps once, which starts level 0's next limiter, and fills
// the order it places.
func fillLevel0(t *testing.T, e *testEnv, r *runner, side string) {
	t.Helper()
	e.step(r, "100.5")
	waitFor(t, "level 0 "+side, func() bool {
		live := e.stock.live()
		return len(live) == 1 && live[0].Side == side
	})
	e.stock.fillLive()
	select {
	case <-r.running[0].done:
	case <-time.After(5 * time.Second):
		t.Fatalf("level 0 %s did not finish", side)
	}
}

// TestStatus: status and profit reports see the greeler's stock fills.
func TestStatus(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	if s := v.Status(nil); !s.BoughtSize.IsZero() || !s.TimePeriod.IsZero() || !s.Budget.Equal(d("10150")) {
		t.Errorf("status before trading: bought %s period %+v budget %s", s.BoughtSize, s.TimePeriod, s.Budget)
	}

	fillLevel0(t, e, r, "BUY")
	fillLevel0(t, e, r, "SELL")
	first := v.Actions()[0].Orders[0].CreateTime.Time
	for _, period := range []*timerange.Range{nil, {}} {
		s := v.Status(period)
		if s.UID != v.UID() || s.ProductID != "AAPL" || s.ExchangeName != "fake" {
			t.Errorf("status names %s %s %s", s.UID, s.ProductID, s.ExchangeName)
		}
		if !s.BoughtSize.Equal(d("25")) || !s.SoldSize.Equal(d("25")) || s.NumBuys != 1 || s.NumSells != 1 || !s.UnsoldSize.IsZero() {
			t.Errorf("status: bought %s sold %s buys %d sells %d unsold %s", s.BoughtSize, s.SoldSize, s.NumBuys, s.NumSells, s.UnsoldSize)
		}
		if !s.Profit().Equal(d("25")) {
			t.Errorf("profit = %s, want 25", s.Profit())
		}
		if !s.TimePeriod.Begin.Equal(first) || !s.TimePeriod.End.IsZero() {
			t.Errorf("period = %+v, want from the first order at %s", s.TimePeriod, first)
		}
	}

	// A period that ended before the first order holds nothing.
	before := &timerange.Range{Begin: first.Add(-time.Hour), End: first.Add(-time.Minute)}
	if s := v.Status(before); !s.BoughtSize.IsZero() || !s.SoldSize.IsZero() || !s.TimePeriod.Equal(before) {
		t.Errorf("status before the orders: bought %s sold %s period %+v", s.BoughtSize, s.SoldSize, s.TimePeriod)
	}
}

// TestStatusCountsPremium: status carries the option premium, held out of
// profit until its position settles.
func TestStatusCountsPremium(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)

	filled := time.Now()
	pos.mu.Lock()
	pos.premiums = []*optpos.Premium{{At: filled, Value: d("150"), Fee: d("1")}}
	pos.mu.Unlock()
	e.step(r, "200")
	if s := v.Status(nil); !s.PremiumValue.Equal(d("150")) || !s.OpenPremiumValue.Equal(d("150")) || !s.Profit().IsZero() {
		t.Errorf("open put: premium %s open %s profit %s; want 150 150 0", s.PremiumValue, s.OpenPremiumValue, s.Profit())
	}

	pos.settle("expired", nil)
	e.step(r, "200")
	s := v.Status(nil)
	if !s.Premium().Equal(d("150")) || !s.Profit().Equal(d("149")) || !s.Fees().Equal(d("1")) {
		t.Errorf("expired put: premium %s profit %s fees %s; want 150 149 1", s.Premium(), s.Profit(), s.Fees())
	}
	if !s.TimePeriod.Begin.Equal(filled) {
		t.Errorf("period %+v, want from the premium at %s", s.TimePeriod, filled)
	}
}

// TestRangedSummarySkipsUnfilledBuy: a flip to a put cancels level 0's buy
// before it fills. A sell in the range pairs with its own buy, not with the
// empty one.
func TestRangedSummarySkipsUnfilledBuy(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	fillLevel0(t, e, r, "BUY")
	fillLevel0(t, e, r, "SELL")
	e.step(r, "100.5")
	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
	pos := flipToPut(t, e, v, r)
	pos.settle("expired", nil)
	e.step(r, "100.5")
	if v.Mode() != "grid" {
		t.Fatalf("mode %s after the put expired", v.Mode())
	}

	fillLevel0(t, e, r, "BUY")
	time.Sleep(10 * time.Millisecond)
	afterBuy := time.Now()
	fillLevel0(t, e, r, "SELL")

	s := v.GetSummary(&timerange.Range{Begin: afterBuy})
	if !s.BoughtSize.Equal(d("25")) || !s.SoldSize.Equal(d("25")) || !s.OversoldSize.IsZero() || !s.UnsoldSize.IsZero() {
		t.Errorf("ranged summary: bought %s sold %s oversold %s unsold %s; want 25 25 0 0", s.BoughtSize, s.SoldSize, s.OversoldSize, s.UnsoldSize)
	}
}

// TestRangedSummarySkipsUnfilledSell: a flip to a call cancels every
// level's sell before it fills. The sells in the range pair with the buys
// before them, not with the empty sells.
func TestRangedSummarySkipsUnfilledSell(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	e.stock.setPrice("100.5")

	// Fill every level's buy by walking the ticker up through the levels.
	e.step(r, "101.5")
	for _, p := range []string{"100.5", "101.5", "102.5", "103.5"} {
		e.stock.setPrice(p)
		waitFor(t, "a buy at "+p, func() bool { return len(e.stock.live()) == 1 })
		e.stock.fillLive()
	}
	waitIdle(t, r)

	// Every level's sell places; the flip to a call cancels them all.
	e.step(r, "101.5")
	waitFor(t, "every level's sell", func() bool { return len(e.stock.live()) == 4 })
	e.step(r, "80")
	e.clock.Add(time.Hour)
	e.step(r, "80")
	if v.Mode() != "wheel" || len(e.stock.live()) != 0 {
		t.Fatalf("mode %s with %d live orders; want a call and no orders", v.Mode(), len(e.stock.live()))
	}
	e.positions[v.current().PositionID].settle("expired", nil)
	e.step(r, "80")
	if v.Mode() != "grid" {
		t.Fatalf("mode %s after the call expired", v.Mode())
	}

	time.Sleep(10 * time.Millisecond)
	afterBuys := time.Now()
	e.step(r, "101.5")
	waitFor(t, "every level's sell", func() bool { return len(e.stock.live()) == 4 })
	e.stock.fillLive()
	waitIdle(t, r)

	s := v.GetSummary(&timerange.Range{Begin: afterBuys})
	if !s.BoughtSize.Equal(d("100")) || !s.SoldSize.Equal(d("100")) || !s.OversoldSize.IsZero() || !s.UnsoldSize.IsZero() {
		t.Errorf("ranged summary: bought %s sold %s oversold %s unsold %s; want 100 100 0 0", s.BoughtSize, s.SoldSize, s.OversoldSize, s.UnsoldSize)
	}
}

func TestAbandonUnopenedPosition(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)

	e.step(r, "112") // put side: 104 > 112*0.93 = 104.16? no
	if v.current().PendingFlip != "" {
		t.Fatalf("pending flip %q at 112", v.current().PendingFlip)
	}
	e.step(r, "110")
	if v.current().PendingFlip != "grid" {
		t.Fatalf("pending flip %q at 110, want grid", v.current().PendingFlip)
	}
	e.clock.Add(time.Hour)
	e.step(r, "110")
	if pos.abandons != 1 || v.Mode() != "grid" {
		t.Fatalf("abandons %d mode %s", pos.abandons, v.Mode())
	}
}

func TestAbandonOpenedPositionHolds(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)
	pos.filled = true

	e.step(r, "110")
	e.clock.Add(time.Hour)
	e.step(r, "110")
	if pos.abandons != 1 || v.Mode() != "wheel" || !v.opened {
		t.Fatalf("abandons %d mode %s opened %v", pos.abandons, v.Mode(), v.opened)
	}
	e.clock.Add(time.Hour)
	e.step(r, "110")
	if pos.abandons != 1 {
		t.Errorf("abandoned an opened position again")
	}

	pos.settle("expired", nil)
	e.step(r, "110")
	if v.Mode() != "grid" {
		t.Fatalf("mode %s after expiry", v.Mode())
	}
}

func TestRetireEndsWhenFlat(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	if _, err := v.SetOption("retire", "true"); err != nil {
		t.Fatal(err)
	}
	r := e.runner(v)
	if done := e.step(r, "100.5"); !done {
		t.Fatalf("retired flat greeler is not done")
	}
	if len(r.running) != 0 {
		t.Errorf("retired greeler started %d limiters", len(r.running))
	}
}

// TestRetireWaitsForLiveBuy: a buy's cancel failed when the greeler last
// stopped. Retired, it doesn't start new buys, but it doesn't end with
// that order live either: it runs the buy's limiter only to cancel it,
// even if the exchange ended the order meanwhile.
func TestRetireWaitsForLiveBuy(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%v", expired), func(t *testing.T) {
			e := newTestEnv(t)
			v := e.newGreeler(testConfig())
			r := e.runner(v)
			e.stock.setPrice("100.5")
			e.step(r, "100.5")
			waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
			e.stock.failCancels(1)
			r.stopAll()
			if n := len(e.stock.live()); n != 1 {
				t.Fatalf("live orders = %d, want the buy", n)
			}
			if expired {
				e.stock.expireLive()
			}

			w := e.reload(v)
			if _, err := w.SetOption("retire", "true"); err != nil {
				t.Fatal(err)
			}
			r = e.runner(w)
			if done := e.step(r, "100.5"); done {
				t.Fatal("retired greeler ended before confirming the buy's order")
			}
			rl := r.running[0]
			if len(r.running) != 1 || rl == nil {
				t.Fatalf("running levels = %v; want level 0's buy", r.running)
			}
			select {
			case <-rl.done:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the buy's limiter to cancel its order")
			}
			if rl.err != nil {
				t.Fatalf("buy's limiter: %v", rl.err)
			}
			if done := e.step(r, "100.5"); !done {
				t.Fatal("retired greeler didn't end once the buy's order was done")
			}
			e.stock.mu.Lock()
			n := len(e.stock.orders)
			e.stock.mu.Unlock()
			if live := e.stock.live(); len(live) != 0 || n != 1 {
				t.Errorf("orders %d live %d; want only the first buy, done", n, len(live))
			}
		})
	}
}

func TestFoldRejectsImpossibleHoldings(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)
	// A call assignment against flat levels can't happen.
	pos.settle("assigned", &gobs.AssignmentFact{Key: "tx", Shares: d("-100"), Price: d("100")})
	if _, err := r.step(context.Background()); err == nil {
		t.Fatalf("step succeeded with negative holdings")
	}
}

// TestRun runs the job loop end to end until canceled.
func TestRun(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	v.interval = 10 * time.Millisecond
	e.stock.setPrice("100.5")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, e.rt) }()

	waitFor(t, "level 0 buy order", func() bool { return len(e.stock.live()) == 1 })
	e.stock.fillLive()
	waitFor(t, "level 0 sell", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return len(v.epochs[0].LevelLimiterIDs[0]) == 2
	})
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if live := e.stock.live(); len(live) != 0 {
		t.Errorf("orders still live after Run returned: %+v", live)
	}
}

func TestRunStopsPositionBeforeReturning(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	v.interval = 10 * time.Millisecond
	pos := flipToPut(t, e, v, e.runner(v))
	e.stock.setPrice("200")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, e.rt) }()
	waitFor(t, "position check", func() bool {
		pos.mu.Lock()
		defer pos.mu.Unlock()
		return pos.checks > 0
	})
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	pos.mu.Lock()
	defer pos.mu.Unlock()
	if pos.stops != 1 {
		t.Errorf("position stops = %d, want 1", pos.stops)
	}
}

func TestLoadKnowsHeldContract(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	flipToPut(t, e, v, e.runner(v))
	w := e.reload(v)
	if _, known := w.HeldContract(); !known {
		t.Error("HeldContract unknown after Load")
	}
}

func TestRunNeedsOptionsExchange(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	rt := *e.rt
	rt.Exchange = struct{ exchange.Exchange }{}
	if err := v.Run(context.Background(), &rt); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("Run = %v, want os.ErrInvalid", err)
	}
}
