// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/point"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

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

// band is four levels from the given buy price, one dollar apart.
func band(from int) *gobs.GreelConfig {
	var levels []*gobs.Pair
	for i := 0; i < 4; i++ {
		levels = append(levels, level(fmt.Sprint(from+i)))
	}
	return &gobs.GreelConfig{
		GridLevels:    levels,
		GridPct:       d("5"),
		FarPct:        d("10"),
		HysteresisPct: d("3"),
		DwellTime:     time.Hour,
	}
}

func newTestLadder(t *testing.T, bands ...*gobs.GreelConfig) *GreelLadder {
	t.Helper()
	v, err := New(uuid.NewString(), "fake", "AAPL", bands)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNewRejectsInvalidInput(t *testing.T) {
	other := band(100)
	other.ProductID = "MSFT"
	bad := band(100)
	bad.GridLevels = bad.GridLevels[:1] // 25 shares, not a contract
	for name, tc := range map[string]struct {
		uid   string
		bands []*gobs.GreelConfig
	}{
		"bad uid":       {"not-a-uuid", []*gobs.GreelConfig{band(100)}},
		"no bands":      {uuid.NewString(), nil},
		"nil band":      {uuid.NewString(), []*gobs.GreelConfig{nil}},
		"other product": {uuid.NewString(), []*gobs.GreelConfig{band(100), other}},
		"invalid band":  {uuid.NewString(), []*gobs.GreelConfig{bad}},
	} {
		if _, err := New(tc.uid, "fake", "AAPL", tc.bands); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
}

func TestNewSpawnsOneGreelerPerBand(t *testing.T) {
	cfg := band(100)
	v := newTestLadder(t, cfg, band(110))
	gs := v.Greelers()
	if len(gs) != 2 {
		t.Fatalf("greelers = %d, want 2", len(gs))
	}
	for i, g := range gs {
		if want := fmt.Sprintf("%s/greeler-%06d", v.UID(), i); g.UID() != want {
			t.Errorf("greeler %d uid = %s, want %s", i, g.UID(), want)
		}
		if g.ProductID() != "AAPL" || g.ExchangeName() != "fake" {
			t.Errorf("greeler %d trades %s on %s", i, g.ProductID(), g.ExchangeName())
		}
	}
	if cfg.ProductID != "" {
		t.Errorf("New changed the caller's band")
	}
	// Each band needs four levels' cash.
	if got := v.BudgetAt(decimal.Zero); !got.Equal(d("21300")) {
		t.Errorf("BudgetAt = %s, want 21150", got)
	}
	if s := v.GetSummary(nil); !s.Budget.Equal(d("21300")) || s.ProductID != "AAPL" {
		t.Errorf("summary = %+v", s)
	}
	if v.Actions() != nil {
		t.Errorf("Actions before trading = %v", v.Actions())
	}
}

func TestSaveLoad(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := newTestLadder(t, band(100), band(110))
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		t.Fatal(err)
	}
	gv, err := kvutil.GetDB[gobs.GreelLadderState](ctx, db, DefaultKeyspace+v.UID())
	if err != nil {
		t.Fatal(err)
	}
	if gv.V1.Progress == nil || len(gv.V1.Config.GreelerIDs) != 2 {
		t.Fatalf("saved state = %+v", gv.V1)
	}

	var w *GreelLadder
	if err := kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) (err error) {
		w, err = Load(ctx, v.UID(), r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w.ProductID() != "AAPL" || w.ExchangeName() != "fake" || len(w.Greelers()) != 2 {
		t.Fatalf("loaded ladder = %s/%s with %d greelers", w.ProductID(), w.ExchangeName(), len(w.Greelers()))
	}
	for i, g := range w.Greelers() {
		if g.UID() != v.cfg.GreelerIDs[i] {
			t.Errorf("greeler %d = %s, want %s", i, g.UID(), v.cfg.GreelerIDs[i])
		}
	}
	// Loaded greelers know what they hold from their saved records, before
	// any Run: here nothing, so a sibling can take any contract.
	if w.exclude(w.cfg.GreelerIDs[0], "C1") {
		t.Errorf("a loaded sibling that holds nothing excluded C1")
	}

	if err := kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		_, err := Load(ctx, uuid.NewString(), r)
		return err
	}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load of a missing ladder = %v, want os.ErrNotExist", err)
	}
}

func TestSetOption(t *testing.T) {
	v := newTestLadder(t, band(100), band(110))
	undo, err := v.SetOption("freeze", "wheel")
	if err != nil {
		t.Fatal(err)
	}
	if undo != "undo:none" {
		t.Errorf("undo = %q", undo)
	}
	if _, err := v.SetOption("bogus", "x"); err == nil {
		t.Errorf("an unknown option was accepted")
	}
	if _, err := v.SetOption("freeze", undo); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetOption("freeze", "bogus"); err == nil {
		t.Errorf("an invalid freeze was accepted")
	}
	if undo, err := v.SetOption("retire", "true"); err != nil || undo != "undo" {
		t.Fatalf("retire = %q, %v", undo, err)
	}

	// Greelers that disagree roll back.
	w := newTestLadder(t, band(100), band(110))
	if _, err := w.greelers[1].SetOption("freeze", "grid"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SetOption("freeze", "all"); err == nil {
		t.Fatalf("SetOption succeeded on disagreeing greelers")
	}
	if _, err := w.greelers[0].SetOption("freeze", "none"); err != nil {
		t.Fatal(err)
	}
	if undo, _ := w.greelers[0].SetOption("freeze", "none"); undo != "" {
		t.Errorf("greeler 0 freeze was not rolled back")
	}
}

// saveFilledCycle records a filled 25-share buy at buy and sell at sell on
// level 0 of greeler gid's saved first epoch.
func saveFilledCycle(t *testing.T, db kv.Database, gid string, buy, sell decimal.Decimal) {
	t.Helper()
	ctx := context.Background()
	key := path.Join(greeler.DefaultKeyspace, gid)
	gv, err := kvutil.GetDB[gobs.GreelerState](ctx, db, key)
	if err != nil {
		t.Fatal(err)
	}
	ep := gv.V1.Progress.Epochs[0]
	at := time.Now()
	for i, pt := range []gobs.Point{
		{Size: d("25"), Price: buy, Cancel: buy.Add(d("5"))},
		{Size: d("25"), Price: sell, Cancel: sell.Sub(d("5"))},
	} {
		p := point.Point(pt)
		side := p.Side()
		id := path.Join(gid, fmt.Sprintf("epoch-000000/level-000/%s-%06d", strings.ToLower(side), i))
		order := &gobs.Order{
			ServerOrderID: id,
			ClientOrderID: uuid.NewString(),
			CreateTime:    gobs.RemoteTime{Time: at.Add(time.Duration(i) * time.Minute)},
			Side:          side,
			Status:        "FILLED",
			FilledSize:    pt.Size,
			FilledPrice:   pt.Price,
			Done:          true,
		}
		ls := &gobs.LimiterState{V2: &gobs.LimiterStateV2{
			ProductID:        "AAPL",
			ExchangeName:     "fake",
			TradePoint:       pt,
			ServerIDOrderMap: map[string]*gobs.Order{order.ServerOrderID: order},
		}}
		if err := kvutil.SetDB(ctx, db, path.Join(limiter.DefaultKeyspace, id), ls); err != nil {
			t.Fatal(err)
		}
		ep.LevelLimiterIDs[0] = append(ep.LevelLimiterIDs[0], id)
	}
	if err := kvutil.SetDB(ctx, db, key, gv); err != nil {
		t.Fatal(err)
	}
}

// TestStatus: the ladder's status sums its greelers' stock fills.
func TestStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := newTestLadder(t, band(100), band(110))
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		t.Fatal(err)
	}
	saveFilledCycle(t, db, v.cfg.GreelerIDs[0], d("100"), d("101"))
	saveFilledCycle(t, db, v.cfg.GreelerIDs[1], d("110"), d("112"))

	var w *GreelLadder
	if err := kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) (err error) {
		w, err = Load(ctx, v.UID(), r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := w.Status(nil)
	if s.UID != w.UID() || s.ProductID != "AAPL" || s.ExchangeName != "fake" {
		t.Errorf("status names %s %s %s", s.UID, s.ProductID, s.ExchangeName)
	}
	if !s.BoughtSize.Equal(d("50")) || !s.SoldSize.Equal(d("50")) || s.NumBuys != 2 || s.NumSells != 2 {
		t.Errorf("status: bought %s sold %s buys %d sells %d; want 50 50 2 2", s.BoughtSize, s.SoldSize, s.NumBuys, s.NumSells)
	}
	// 25 shares a dollar up in the first band, two dollars up in the second.
	if !s.Profit().Equal(d("75")) {
		t.Errorf("profit = %s, want 75", s.Profit())
	}
	if !s.Budget.Equal(d("21300")) {
		t.Errorf("budget = %s, want 21300", s.Budget)
	}
}

type fakeSibling struct {
	uid   string
	held  string
	known bool
}

func (s *fakeSibling) UID() string                  { return s.uid }
func (s *fakeSibling) HeldContract() (string, bool) { return s.held, s.known }

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

func TestExclude(t *testing.T) {
	v := newTestLadder(t, band(100))
	clock := &testClock{now: time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)}
	v.now = clock.Now
	a := &fakeSibling{uid: "a", known: true}
	b := &fakeSibling{uid: "b", known: true}
	c := &fakeSibling{uid: "c"}
	v.siblings = []sibling{a, b, c}

	if !v.exclude("a", "C1") {
		t.Errorf("a sibling with an unknown position didn't exclude")
	}
	c.known = true

	// a selects C1; b can't have it while the claim is fresh.
	if v.exclude("a", "C1") {
		t.Fatalf("C1 excluded for a")
	}
	if !v.exclude("b", "C1") {
		t.Errorf("C1 claimed by a wasn't excluded for b")
	}
	if v.exclude("a", "C1") {
		t.Errorf("a's own claim excluded C1 for a")
	}

	// Once a holds C1, the claim no longer matters.
	a.held = "C1"
	clock.Add(claimTTL)
	if !v.exclude("b", "C1") {
		t.Errorf("C1 held by a wasn't excluded for b")
	}
	if v.exclude("a", "C1") {
		t.Errorf("a's own held contract was excluded for a")
	}

	// A claim moves with the greeler's latest question, and lapses.
	if v.exclude("b", "C2") || v.exclude("b", "C3") {
		t.Fatalf("free contracts excluded for b")
	}
	if v.exclude("c", "C2") {
		t.Errorf("C2 is still claimed after b moved on to C3")
	}
	if !v.exclude("a", "C3") {
		t.Errorf("C3 claimed by b wasn't excluded for a")
	}
	clock.Add(claimTTL)
	if v.exclude("a", "C3") {
		t.Errorf("b's lapsed claim on C3 still excludes")
	}
}

func TestReconciler(t *testing.T) {
	var r reconciler
	// The first check sets the base; the account may hold other shares.
	if m := r.observe(d("500"), d("0")); m != nil {
		t.Fatalf("first check = %+v", m)
	}
	// Matching movement.
	if m := r.observe(d("600"), d("100")); m != nil {
		t.Fatalf("matched movement = %+v", m)
	}
	// A fill the greelers haven't derived yet clears by the next check.
	if m := r.observe(d("625"), d("100")); m != nil {
		t.Fatalf("first mismatch alerted")
	}
	if m := r.observe(d("625"), d("125")); m != nil {
		t.Fatalf("cleared mismatch = %+v", m)
	}
	// Movement the greelers never explain alerts once, on the second check.
	if m := r.observe(d("525"), d("125")); m != nil {
		t.Fatalf("first mismatch alerted")
	}
	m := r.observe(d("525"), d("125"))
	if m == nil || !m.account.Equal(d("-100")) || !m.derived.IsZero() || !m.unexplained().Equal(d("-100")) {
		t.Fatalf("mismatch = %+v, want account -100", m)
	}
	if m := r.observe(d("525"), d("125")); m != nil {
		t.Fatalf("mismatch alerted twice: %+v", m)
	}
}

// fakeExchange is an options exchange with one stock product that reports
// the account's shares.
type fakeExchange struct {
	exchange.OptionsExchange // unused methods panic

	mu     sync.Mutex
	shares decimal.Decimal
}

func (f *fakeExchange) ExchangeName() string       { return "fake" }
func (f *fakeExchange) CanDedupOnClientUUID() bool { return false }

func (f *fakeExchange) GetStockHolding(ctx context.Context, productID string) (decimal.Decimal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if productID != "AAPL" {
		return decimal.Zero, os.ErrNotExist
	}
	return f.shares, nil
}

func (f *fakeExchange) setShares(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shares = d(s)
}

// fakeStock is the underlying's stock product. Orders rest forever.
type fakeStock struct {
	exchange.Product // unused methods panic

	prices *topic.Topic[exchange.PriceUpdate]

	mu     sync.Mutex
	orders []*exchange.SimpleOrder
}

func (p *fakeStock) ProductID() string            { return "AAPL" }
func (p *fakeStock) ExchangeName() string         { return "fake" }
func (p *fakeStock) BaseMinSize() decimal.Decimal { return d("1") }
func (p *fakeStock) Close() error                 { return nil }

func (p *fakeStock) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	return topic.Subscribe(p.prices, 0, true)
}

func (p *fakeStock) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	return topic.Subscribe(topic.New[exchange.OrderUpdate](), 0, false)
}

func (p *fakeStock) place(side string, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := &exchange.SimpleOrder{
		ServerOrderID: fmt.Sprintf("order-%d", len(p.orders)),
		ClientUUID:    clientID,
		Side:          side,
		CreateTime:    gobs.RemoteTime{Time: time.Now()},
		Status:        "OPEN",
	}
	p.orders = append(p.orders, o)
	dup := *o
	return &dup, nil
}

func (p *fakeStock) LimitBuy(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	return p.place("BUY", clientID, size, price)
}

func (p *fakeStock) LimitSell(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	return p.place("SELL", clientID, size, price)
}

func (p *fakeStock) Cancel(ctx context.Context, serverID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.orders {
		if o.ServerOrderID == serverID {
			o.Done, o.Status = true, "CANCELLED"
			return nil
		}
	}
	return os.ErrNotExist
}

func (p *fakeStock) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.orders {
		if o.ServerOrderID == serverID {
			dup := *o
			return &dup, nil
		}
	}
	return nil, os.ErrNotExist
}

func (p *fakeStock) live() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, o := range p.orders {
		if !o.Done {
			n++
		}
	}
	return n
}

type fakeMessenger struct {
	mu   sync.Mutex
	msgs []string
}

func (m *fakeMessenger) SendMessage(ctx context.Context, at time.Time, format string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, fmt.Sprintf(format, args...))
}

func (m *fakeMessenger) messages() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.msgs...)
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

// TestRun drives two greelers near spot and reconciles against the
// account until canceled.
func TestRun(t *testing.T) {
	db := newTestDB(t)
	ex := &fakeExchange{shares: d("40")}
	stock := &fakeStock{prices: topic.New[exchange.PriceUpdate]()}
	msgr := &fakeMessenger{}
	rt := &trader.Runtime{Exchange: ex, Database: db, Product: stock, Messenger: msgr}

	// Spot 102.5 with a 5% grid: every level of both bands is in the grid
	// band, and a buy rests while spot is between its price and its cancel
	// price: 98-101 of the first band and 102 of the second.
	v := newTestLadder(t, band(98), band(102))
	v.reconcileInterval = 20 * time.Millisecond
	stock.prices.Send(&exchange.SimpleTicker{Price: d("102.5"), ServerTime: exchange.RemoteTime{Time: time.Now()}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, rt) }()

	waitFor(t, "buys in both bands", func() bool { return stock.live() == 5 })
	for _, g := range v.Greelers() {
		if id, known := g.HeldContract(); !known || id != "" {
			t.Errorf("greeler %s holds %q, %v", g.UID(), id, known)
		}
	}

	// Nothing has moved: no alert. Then 100 shares leave the account
	// without any greeler selling them.
	time.Sleep(100 * time.Millisecond)
	if msgs := msgr.messages(); len(msgs) != 0 {
		t.Fatalf("alerts without movement: %q", msgs)
	}
	ex.setShares("-60")
	waitFor(t, "a reconciliation alert", func() bool { return len(msgr.messages()) > 0 })
	if msg := msgr.messages()[0]; !strings.Contains(msg, "-100 shares of AAPL") {
		t.Errorf("alert = %q", msg)
	}

	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if n := stock.live(); n != 0 {
		t.Errorf("%d orders still live after Run returned", n)
	}
}

// TestRunReturnsWhenRetired: a retired ladder with nothing held ends.
func TestRunReturnsWhenRetired(t *testing.T) {
	db := newTestDB(t)
	stock := &fakeStock{prices: topic.New[exchange.PriceUpdate]()}
	rt := &trader.Runtime{Exchange: &fakeExchange{}, Database: db, Product: stock}
	v := newTestLadder(t, band(98), band(102))
	if _, err := v.SetOption("retire", "true"); err != nil {
		t.Fatal(err)
	}
	stock.prices.Send(&exchange.SimpleTicker{Price: d("102.5"), ServerTime: exchange.RemoteTime{Time: time.Now()}})
	if err := v.Run(context.Background(), rt); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// TestRunReportsGreelerFailures: greelers that fail make Run fail once all
// have returned.
func TestRunReportsGreelerFailures(t *testing.T) {
	db := newTestDB(t)
	rt := &trader.Runtime{Exchange: struct{ exchange.Exchange }{}, Database: db}
	v := newTestLadder(t, band(98), band(102))
	if err := v.Run(context.Background(), rt); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("Run = %v, want os.ErrInvalid", err)
	}
}

func TestLoadFunc(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	v := newTestLadder(t, band(100), band(110))
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		t.Fatal(err)
	}
	topLevel := func(keyspace string) func(string) bool {
		return func(k string) bool {
			_, err := uuid.Parse(strings.TrimPrefix(k, keyspace))
			return err == nil
		}
	}
	if err := kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		ladders, err := LoadFunc(ctx, r, topLevel(DefaultKeyspace))
		if err != nil {
			return err
		}
		if len(ladders) != 1 || ladders[0].UID() != v.UID() {
			t.Errorf("ladders = %d, want this one", len(ladders))
		}
		all, err := greeler.LoadFunc(ctx, r, nil)
		if err != nil {
			return err
		}
		top, err := greeler.LoadFunc(ctx, r, topLevel(greeler.DefaultKeyspace))
		if err != nil {
			return err
		}
		// The ladder's greelers are keyed under it, not top level.
		if len(all) != 2 || len(top) != 0 {
			t.Errorf("greelers: all %d, top level %d; want 2 and 0", len(all), len(top))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
