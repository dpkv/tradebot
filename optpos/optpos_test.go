// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/optlimiter"
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

const (
	putA  = "AAPL_20261218_P00200000"
	putB  = "AAPL_20261218_P00190000"
	callA = "AAPL_20261218_C00220000"
)

var expiry = time.Date(2026, 12, 18, 21, 0, 0, 0, time.UTC)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// fakeExchange implements OptionsExchange with one fakeProduct per contract.
// It fails the test if two orders are ever live at once.
type fakeExchange struct {
	exchange.OptionsExchange // unused methods panic

	t *testing.T

	mu          sync.Mutex
	chain       []*gobs.OptionContract
	products    map[string]*fakeProduct
	settlements map[string]*exchange.OptionsSettlement
	nsettle     int

	// fillAtOrBelow fills an order on placement when its price is at or below.
	fillAtOrBelow decimal.Decimal
	// failPlaceAfter places the order but returns an error, like a timeout.
	failPlaceAfter bool

	now func() time.Time // create time of new orders
}

type fakeProduct struct {
	exchange.OptionsProduct // unused methods panic

	ex      *fakeExchange
	id      string
	orders  []*fakeOrder
	updates *topic.Topic[exchange.OrderUpdate]
}

type fakeOrder struct {
	exchange.SimpleOrder
	size, price decimal.Decimal
}

func newFakeExchange(t *testing.T) *fakeExchange {
	f := &fakeExchange{
		t:           t,
		products:    make(map[string]*fakeProduct),
		settlements: make(map[string]*exchange.OptionsSettlement),
		now:         time.Now,
	}
	for _, c := range []struct{ id, typ, strike string }{{putA, "PUT", "200"}, {putB, "PUT", "190"}, {callA, "CALL", "220"}} {
		f.chain = append(f.chain, &gobs.OptionContract{
			ContractID:   c.id,
			Underlying:   "AAPL",
			OptionType:   c.typ,
			Strike:       d(c.strike),
			Expiry:       expiry,
			ContractSize: d("100"),
			Bid:          d("1.00"),
			Ask:          d("1.40"),
		})
	}
	return f
}

func (f *fakeExchange) ExchangeName() string { return "fake" }

func (f *fakeExchange) GetOptionsChain(ctx context.Context, underlying string) ([]*gobs.OptionContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var chain []*gobs.OptionContract
	for _, c := range f.chain {
		dup := *c
		chain = append(chain, &dup)
	}
	return chain, nil
}

func (f *fakeExchange) GetOptionsProduct(ctx context.Context, contractID string) (*gobs.OptionContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.chain {
		if c.ContractID == contractID {
			dup := *c
			return &dup, nil
		}
	}
	return nil, os.ErrNotExist
}

func (f *fakeExchange) OpenOptionsProduct(ctx context.Context, contractID string) (exchange.OptionsProduct, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.productLocked(contractID), nil
}

func (f *fakeExchange) productLocked(contractID string) *fakeProduct {
	p, ok := f.products[contractID]
	if !ok {
		p = &fakeProduct{ex: f, id: contractID, updates: topic.New[exchange.OrderUpdate]()}
		f.products[contractID] = p
	}
	return p
}

func (f *fakeExchange) GetOptionsSettlement(ctx context.Context, contractID string) (*exchange.OptionsSettlement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nsettle++
	if s, ok := f.settlements[contractID]; ok {
		dup := *s
		return &dup, nil
	}
	return &exchange.OptionsSettlement{Status: "open"}, nil
}

func (f *fakeExchange) GetOptionsOrderByClientID(ctx context.Context, clientID uuid.UUID) (exchange.OrderDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.products {
		for _, o := range p.orders {
			if o.ClientUUID == clientID {
				dup := o.SimpleOrder
				return &dup, nil
			}
		}
	}
	return nil, os.ErrNotExist
}

func (p *fakeProduct) ContractID() string   { return p.id }
func (p *fakeProduct) ExchangeName() string { return "fake" }
func (p *fakeProduct) Close() error         { return nil }

func (p *fakeProduct) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	return topic.Subscribe(p.updates, 0, false)
}

func (p *fakeProduct) LimitSellToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	f := p.ex
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, q := range f.products {
		for _, o := range q.orders {
			if !o.Done {
				f.t.Errorf("two live orders: %s/%s is live while placing another", q.id, o.ServerOrderID)
			}
		}
	}
	o := &fakeOrder{size: numContracts, price: limitPrice}
	o.ServerOrderID = fmt.Sprintf("%s-order-%d", p.id, len(p.orders))
	o.ClientUUID = clientID
	o.Side = "SELL"
	o.CreateTime = gobs.RemoteTime{Time: f.now()}
	o.Status = "OPEN"
	p.orders = append(p.orders, o)
	if !f.fillAtOrBelow.IsZero() && limitPrice.LessThanOrEqual(f.fillAtOrBelow) {
		p.fillLocked(o, numContracts)
	}
	if f.failPlaceAfter {
		return nil, errors.New("timeout")
	}
	dup := o.SimpleOrder
	return &dup, nil
}

func (p *fakeProduct) fillLocked(o *fakeOrder, n decimal.Decimal) {
	o.FilledSize = o.FilledSize.Add(n)
	o.FilledPrice = o.price
	if o.FilledSize.Equal(o.size) {
		o.Done, o.Status = true, "EXECUTED"
	}
	dup := o.SimpleOrder
	p.updates.Send(&dup)
}

func (p *fakeProduct) find(id string) *fakeOrder {
	for _, o := range p.orders {
		if o.ServerOrderID == id {
			return o
		}
	}
	return nil
}

func (p *fakeProduct) Cancel(ctx context.Context, serverID string) error {
	p.ex.mu.Lock()
	defer p.ex.mu.Unlock()
	o := p.find(serverID)
	if o == nil {
		return os.ErrNotExist
	}
	if o.Done {
		return errors.New("order is already done")
	}
	o.Done, o.Status = true, "CANCELLED"
	return nil
}

func (p *fakeProduct) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	p.ex.mu.Lock()
	defer p.ex.mu.Unlock()
	o := p.find(serverID)
	if o == nil {
		return nil, os.ErrNotExist
	}
	dup := o.SimpleOrder
	return &dup, nil
}

// fillLive fills the live order completely.
func (f *fakeExchange) fillLive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.products {
		for _, o := range p.orders {
			if !o.Done {
				p.fillLocked(o, o.size.Sub(o.FilledSize))
				return true
			}
		}
	}
	return false
}

// counts returns the number of orders placed for a contract and how many
// of all orders are live.
func (f *fakeExchange) counts(contractID string) (orders, live int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.products[contractID]; ok {
		orders = len(p.orders)
	}
	for _, p := range f.products {
		for _, o := range p.orders {
			if !o.Done {
				live++
			}
		}
	}
	return orders, live
}

func (f *fakeExchange) settle(contractID string, s *exchange.OptionsSettlement) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settlements[contractID] = s
}

func (f *fakeExchange) settlementCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nsettle
}

// pickSelector returns the chain contract named by pick.
type pickSelector struct {
	mu   sync.Mutex
	pick string
}

func (s *pickSelector) set(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pick = id
}

func (s *pickSelector) Select(ctx context.Context, chain []*gobs.OptionContract, c *Constraint) (*Selection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, contract := range chain {
		if contract.ContractID == s.pick {
			return &Selection{Contract: contract, MinPremium: d("0.50")}, nil
		}
	}
	return nil, os.ErrNotExist
}

func newTestDB(t *testing.T) kv.Database {
	bdb, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bdb.Close() })
	return kvbadger.New(bdb, func(string) bool { return true })
}

func alwaysOpen(t time.Time) (bool, time.Time) { return true, t.Add(time.Hour) }

// testClock is the position's clock: always in session, one session per
// calendar day (UTC).
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func dailySession(t time.Time) (bool, time.Time) {
	return true, t.Truncate(24 * time.Hour).Add(20 * time.Hour)
}

var testKnobs = &gobs.WheelKnobs{RepriceInterval: time.Hour}

type testEnv struct {
	t     *testing.T
	ex    *fakeExchange
	db    kv.Database
	sel   *pickSelector
	clock *testClock
}

func newTestEnv(t *testing.T) *testEnv {
	e := &testEnv{
		t:     t,
		ex:    newFakeExchange(t),
		db:    newTestDB(t),
		sel:   &pickSelector{pick: putA},
		clock: &testClock{now: time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC)},
	}
	e.ex.now = e.clock.Now
	return e
}

func (e *testEnv) hooks(v *Position) {
	v.now = e.clock.Now
	v.session = dailySession
	v.legHook = func(leg *optlimiter.OptLimiter) { leg.SetSession(alwaysOpen) }
	if v.leg != nil {
		v.leg.SetSession(alwaysOpen)
	}
	// Cleanups run last in, first out, so this stops the attempt before the
	// test's database closes under it.
	e.t.Cleanup(func() {
		if a := v.active; a != nil {
			a.cancel(errStopped)
			<-a.done
		}
	})
}

// newPosition creates a position and saves its empty record, as the greeler
// does before Open.
func (e *testEnv) newPosition(uid string) *Position {
	v := New(uid, "fake", "AAPL", e.sel, testKnobs, e.ex, e.db)
	e.hooks(v)
	if err := kv.WithReadWriter(context.Background(), e.db, v.Save); err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *testEnv) load(uid string) *Position {
	e.t.Helper()
	var v *Position
	if err := kv.WithReader(context.Background(), e.db, func(ctx context.Context, r kv.Reader) (err error) {
		v, err = Load(ctx, uid, r, e.sel, testKnobs, e.ex, e.db)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	e.hooks(v)
	return v
}

func (e *testEnv) loadLeg(uid string) *optlimiter.OptLimiter {
	e.t.Helper()
	var v *optlimiter.OptLimiter
	if err := kv.WithReader(context.Background(), e.db, func(ctx context.Context, r kv.Reader) (err error) {
		v, err = optlimiter.Load(ctx, uid, r)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// waitAttempt waits until the attempt returns, and so has saved its leg.
func waitAttempt(t *testing.T, a *attempt) {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the attempt to return")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func putConstraint() *Constraint {
	return &Constraint{Underlying: "AAPL", OptionType: "PUT", MaxStrike: d("200")}
}

// openResting opens a position whose first order rests unfilled.
func (e *testEnv) openResting(fctx context.Context, uid string) *Position {
	e.t.Helper()
	v := e.newPosition(uid)
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		e.t.Fatal(err)
	}
	waitFor(e.t, "first order", func() bool { n, _ := e.ex.counts(putA); return n == 1 })
	return v
}

// openFilled opens a position whose first order fills, and waits until the
// attempt has saved the fill and returned.
func (e *testEnv) openFilled(fctx context.Context, uid string, c *Constraint) *Position {
	e.t.Helper()
	e.ex.fillAtOrBelow = d("10")
	v := e.newPosition(uid)
	if err := v.Open(context.Background(), fctx, c); err != nil {
		e.t.Fatal(err)
	}
	waitAttempt(e.t, v.active)
	if !v.leg.IsDone() {
		e.t.Fatalf("attempt returned unfilled: %v", v.active.err)
	}
	return v
}

func TestOpenWritesAheadAndFills(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openFilled(fctx, "p1", putConstraint())
	if err := v.Open(context.Background(), fctx, putConstraint()); !errors.Is(err, os.ErrExist) {
		t.Errorf("second Open: got %v, want os.ErrExist", err)
	}

	w := e.load("p1")
	if len(w.legIDs) != 1 || w.legIDs[0] != "p1/leg-000000" {
		t.Fatalf("legs: %v", w.legIDs)
	}
	if w.contract == nil || w.contract.ContractID != putA {
		t.Fatalf("contract: %+v", w.contract)
	}
	leg := e.loadLeg("p1/leg-000000")
	if !leg.IsDone() || leg.ContractID() != putA || !leg.MinPremium().Equal(d("0.50")) {
		t.Errorf("leg: done %v contract %s min-premium %s", leg.IsDone(), leg.ContractID(), leg.MinPremium())
	}
}

func TestOpenRejectsBadSelection(t *testing.T) {
	e := newTestEnv(t)
	v := e.newPosition("p1")
	tests := []struct {
		pick string
		c    *Constraint
	}{
		{callA, putConstraint()},
		{putA, &Constraint{OptionType: "PUT", MaxStrike: d("195")}},
		{putA, &Constraint{OptionType: "PUT", Exclude: func(id string) bool { return id == putA }}},
		{putA, &Constraint{OptionType: "PUT", ContractSize: d("150")}},
		{"missing", putConstraint()},
	}
	for i, tc := range tests {
		e.sel.set(tc.pick)
		if err := v.Open(context.Background(), context.Background(), tc.c); err == nil {
			t.Errorf("%d: Open: want error", i)
		}
	}
	if len(v.legIDs) != 0 || len(e.ex.products) != 0 {
		t.Errorf("legs %v products %d after rejected selections", v.legIDs, len(e.ex.products))
	}
}

func TestCheckSettlement(t *testing.T) {
	tests := []struct {
		name    string
		pick    string
		c       *Constraint
		status  string
		shares  string
		strike  string
		outcome string
	}{
		{"put assigned", putA, putConstraint(), "assigned", "100", "200", "assigned"},
		{"call assigned", callA, &Constraint{OptionType: "CALL", MinStrike: d("210")}, "assigned", "-100", "220", "assigned"},
		{"expired", putA, putConstraint(), "expired", "", "", "expired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.sel.set(tc.pick)
			fctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			v := e.openFilled(fctx, "p1", tc.c)
			if err := v.Check(context.Background(), fctx, tc.c); err != nil || v.Outcome() != "" {
				t.Fatalf("Check before settlement: err %v outcome %q", err, v.Outcome())
			}

			at := time.Date(2026, 12, 19, 12, 0, 0, 0, time.UTC)
			e.ex.settle(tc.pick, &exchange.OptionsSettlement{Status: tc.status, Key: "tx1", Contracts: d("1"), At: at})
			e.clock.Set(e.clock.Now().Add(settlementInterval))
			if err := v.Check(context.Background(), fctx, tc.c); err != nil {
				t.Fatal(err)
			}

			w := e.load("p1")
			if w.Outcome() != tc.outcome || !w.OutcomeAt().Equal(at) {
				t.Fatalf("outcome %q at %s", w.Outcome(), w.OutcomeAt())
			}
			fact := w.Assignment()
			if tc.shares == "" {
				if fact != nil {
					t.Errorf("assignment on %q: %+v", tc.outcome, fact)
				}
			} else if fact == nil || fact.Key != "tx1" || !fact.Shares.Equal(d(tc.shares)) || !fact.Price.Equal(d(tc.strike)) {
				t.Errorf("assignment: %+v", fact)
			}

			// Terminal: Check is a no-op and Legs are frozen.
			calls := e.ex.settlementCalls()
			e.clock.Set(e.clock.Now().Add(settlementInterval))
			if err := w.Check(context.Background(), fctx, tc.c); err != nil || e.ex.settlementCalls() != calls || len(w.legIDs) != 1 {
				t.Errorf("Check after outcome: err %v calls %d legs %v", err, e.ex.settlementCalls()-calls, w.legIDs)
			}
		})
	}
}

func TestCheckSettlingPastExpiry(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openFilled(fctx, "p1", putConstraint())
	e.clock.Set(expiry.Add(time.Hour))
	for i := 0; i < 3; i++ {
		if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
			t.Fatal(err)
		}
	}
	// Settling: no outcome from the calendar alone, and the broker is asked
	// at most hourly.
	if v.Outcome() != "" || e.ex.settlementCalls() != 1 {
		t.Fatalf("outcome %q settlement calls %d", v.Outcome(), e.ex.settlementCalls())
	}

	e.ex.settle(putA, &exchange.OptionsSettlement{Status: "assigned", Key: "tx1", Contracts: d("1")})
	e.clock.Set(e.clock.Now().Add(settlementInterval))
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if v.Outcome() != "assigned" || !v.OutcomeAt().Equal(e.clock.Now()) {
		t.Errorf("outcome %q at %s", v.Outcome(), v.OutcomeAt())
	}
}

func TestCheckReselectsAtNewSession(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openResting(fctx, "p1")

	// Same session: no re-selection even if the selector would change.
	e.sel.set(putB)
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if len(v.legIDs) != 1 {
		t.Fatalf("legs within a session: %v", v.legIDs)
	}

	// Next session, same contract: the attempt is kept.
	e.sel.set(putA)
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if len(v.legIDs) != 1 {
		t.Fatalf("legs after same re-selection: %v", v.legIDs)
	}

	// Next session, different contract: the old order is canceled and a new
	// attempt is saved ahead of its first order.
	e.sel.set(putB)
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "order for the new contract", func() bool { n, _ := e.ex.counts(putB); return n == 1 })
	if _, live := e.ex.counts(putA); live != 1 {
		t.Errorf("live orders: %d", live)
	}

	w := e.load("p1")
	if len(w.legIDs) != 2 || w.legIDs[1] != "p1/leg-000001" || w.contract.ContractID != putB {
		t.Errorf("legs %v contract %s", w.legIDs, w.contract.ContractID)
	}
}

func TestReselectKeepsAttemptThatFills(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openResting(fctx, "p1")
	if !e.ex.fillLive() {
		t.Fatal("no live order to fill")
	}
	waitFor(t, "fill", func() bool { return v.leg.IsDone() })

	e.sel.set(putB)
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if len(v.legIDs) != 1 {
		t.Errorf("filled position re-selected: %v", v.legIDs)
	}
}

func TestAbandonUnfilled(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openResting(fctx, "p1")
	if err := v.Abandon(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, live := e.ex.counts(putA); live != 0 {
		t.Errorf("live orders after Abandon: %d", live)
	}
	if err := v.Abandon(context.Background()); err != nil {
		t.Errorf("second Abandon: %v", err)
	}
	if w := e.load("p1"); w.Outcome() != "unfilled" {
		t.Errorf("outcome %q", w.Outcome())
	}
}

func TestAbandonFilled(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openFilled(fctx, "p1", putConstraint())
	if err := v.Abandon(context.Background()); !errors.Is(err, ErrOpened) {
		t.Fatalf("Abandon: got %v, want ErrOpened", err)
	}
	if w := e.load("p1"); w.Outcome() != "" {
		t.Errorf("outcome %q", w.Outcome())
	}
}

func TestResumeReattaches(t *testing.T) {
	e := newTestEnv(t)

	// The first run stops with its order canceled, as on shutdown. Its
	// attempt must return, and so finish saving, before the leg runs again.
	fctx1, cancel1 := context.WithCancel(context.Background())
	first := e.openResting(fctx1, "p1")
	cancel1()
	waitAttempt(t, first.active)
	if _, live := e.ex.counts(putA); live != 0 {
		t.Fatalf("live orders after shutdown: %d", live)
	}

	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v := e.load("p1")
	e.sel.set(putB) // must not re-select within the session
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "order after resume", func() bool { n, _ := e.ex.counts(putA); return n == 2 })
	if len(v.legIDs) != 1 {
		t.Errorf("legs after resume: %v", v.legIDs)
	}
}

func TestResumeOpensWithoutLegs(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e.newPosition("p1") // crashed before Open
	v := e.load("p1")
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first order", func() bool { n, _ := e.ex.counts(putA); return n == 1 })
	if len(v.legIDs) != 1 {
		t.Errorf("legs: %v", v.legIDs)
	}
}

func TestAbandonAfterCrashCancelsRecoveredOrder(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The broker accepts the order but the call fails, so the attempt
	// returns with its order live and not in its record.
	e.ex.failPlaceAfter = true
	v := e.newPosition("p1")
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, v.active)
	e.ex.mu.Lock()
	e.ex.failPlaceAfter = false
	e.ex.mu.Unlock()
	if _, live := e.ex.counts(putA); live != 1 {
		t.Fatalf("live orders: %d", live)
	}

	w := e.load("p1")
	if err := w.Abandon(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, live := e.ex.counts(putA); live != 0 || n != 1 {
		t.Errorf("orders %d live %d after Abandon", n, live)
	}
	if w.Outcome() != "unfilled" {
		t.Errorf("outcome %q", w.Outcome())
	}
}

func TestCheckWaitsAfterFailedOpen(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.newPosition("p1")
	e.sel.set("missing")
	if err := v.Check(context.Background(), fctx, putConstraint()); err == nil {
		t.Fatal("Check: want a selection error")
	}
	// The contract becomes available, but Check waits out the retry delay.
	e.sel.set(putA)
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if len(v.legIDs) != 0 {
		t.Fatalf("legs before the retry delay: %v", v.legIDs)
	}
	e.clock.Set(e.clock.Now().Add(retryDelay))
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first order", func() bool { n, _ := e.ex.counts(putA); return n == 1 })
}

func TestCheckWaitsAfterFailedAttempt(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e.ex.failPlaceAfter = true
	v := e.newPosition("p1")
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitAttempt(t, v.active)
	e.ex.mu.Lock()
	e.ex.failPlaceAfter = false
	e.ex.mu.Unlock()

	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if v.active != nil {
		t.Fatal("failed attempt restarted before the retry delay")
	}
	e.clock.Set(e.clock.Now().Add(retryDelay))
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if v.active == nil {
		t.Fatal("failed attempt not restarted after the retry delay")
	}
}

func TestStopCancelsAttemptAndCheckRestarts(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openResting(fctx, "p1")
	if err := v.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, live := e.ex.counts(putA); n != 1 || live != 0 || v.active != nil {
		t.Fatalf("after Stop: orders %d live %d active %v", n, live, v.active != nil)
	}
	if v.Outcome() != "" {
		t.Errorf("outcome after Stop: %q", v.Outcome())
	}
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second order", func() bool { n, live := e.ex.counts(putA); return n == 2 && live == 1 })
}

func TestHeldContractID(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	e.openResting(fctx, "p1")
	e.newPosition("p2")
	for uid, want := range map[string]string{"p1": putA, "p2": ""} {
		var got string
		if err := kv.WithReader(context.Background(), e.db, func(ctx context.Context, r kv.Reader) (err error) {
			got, err = HeldContractID(ctx, uid, r)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: held %q, want %q", uid, got, want)
		}
	}
}

func TestResumeReselectsAfterItsSession(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	v := e.openResting(fctx, "p1")
	if err := v.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Restarted the same session: the attempt is resumed, not re-selected.
	e.sel.set(putB)
	w := e.load("p1")
	if err := w.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if len(w.legIDs) != 1 {
		t.Fatalf("legs after a same-session restart: %v", w.legIDs)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Restarted a session later: the stale contract is re-selected.
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	x := e.load("p1")
	if err := x.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "order for the re-selected contract", func() bool { n, _ := e.ex.counts(putB); return n == 1 })
	if len(x.legIDs) != 2 {
		t.Errorf("legs after a next-session restart: %v", x.legIDs)
	}
}

func TestFacts(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := putConstraint()
	v := e.openFilled(fctx, "p1", c)
	want := v.leg.FilledValue().Mul(v.leg.ContractSize())
	if !want.IsPositive() {
		t.Fatalf("leg filled value %s", v.leg.FilledValue())
	}

	read := func() *Facts {
		t.Helper()
		var f *Facts
		if err := kv.WithReader(context.Background(), e.db, func(ctx context.Context, r kv.Reader) (err error) {
			f, err = ReadFacts(ctx, "p1", r)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return f
	}
	// The leg saves its fill after it is done.
	waitFor(t, "saved fill", func() bool { return len(read().Premiums) == 1 })
	for name, f := range map[string]*Facts{"Facts": v.Facts(), "ReadFacts": read()} {
		if f.Outcome != "" || len(f.Premiums) != 1 || !f.Premiums[0].Value.Equal(want) || f.Premiums[0].At.IsZero() {
			t.Errorf("%s while open: %+v", name, f)
		}
	}

	at := time.Date(2026, 12, 19, 12, 0, 0, 0, time.UTC)
	e.ex.settle(putA, &exchange.OptionsSettlement{Status: "assigned", Key: "tx1", Contracts: d("1"), Fee: d("1.25"), At: at})
	e.clock.Set(e.clock.Now().Add(settlementInterval))
	if err := v.Check(context.Background(), fctx, c); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]*Facts{"Facts": v.Facts(), "ReadFacts": read()} {
		if f.Outcome != "assigned" || !f.OutcomeAt.Equal(at) || f.Assignment == nil || !f.Assignment.Fee.Equal(d("1.25")) || len(f.Premiums) != 1 {
			t.Errorf("%s after assignment: %+v", name, f)
		}
	}
}
