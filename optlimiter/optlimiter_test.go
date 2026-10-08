// Copyright (c) 2026 Deepak Vankadaru

package optlimiter

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
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

const testContract = "AAPL_20261218_P00200000"

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// fakeBroker implements OptionsExchange and OptionsProduct for one contract.
// It fails the test if two of its orders are ever live at once.
type fakeBroker struct {
	exchange.OptionsExchange // unused methods panic
	exchange.OptionsProduct  // unused methods panic

	t *testing.T

	mu       sync.Mutex
	bid, ask decimal.Decimal
	orders   []*fakeOrder

	// fillAtOrBelow fills an order on placement when its price is at or below.
	fillAtOrBelow decimal.Decimal
	// fillOnCancel fills this many contracts of an order as it is canceled.
	fillOnCancel decimal.Decimal
	// failPlaceAfter places the order but returns an error, like a timeout.
	failPlaceAfter bool
	// rejectAll rejects every order as soon as it is placed.
	rejectAll bool
	// sendOnPlace sends an update for every order as it is placed.
	sendOnPlace bool
	// getSkew shifts the create time Get reports, which updates can't merge.
	getSkew time.Duration
	// getErr makes Get fail.
	getErr error
	// placeHook, if set, runs as each placement starts, before the order is
	// at the broker; a test can block in it. Set it before Run.
	placeHook func()

	updates *topic.Topic[exchange.OrderUpdate]
}

type fakeOrder struct {
	exchange.SimpleOrder
	size, price decimal.Decimal
}

func newFakeBroker(t *testing.T, bid, ask string) *fakeBroker {
	return &fakeBroker{t: t, bid: d(bid), ask: d(ask), updates: topic.New[exchange.OrderUpdate]()}
}

func (f *fakeBroker) ExchangeName() string { return "fake" }
func (f *fakeBroker) ContractID() string   { return testContract }
func (f *fakeBroker) Close() error         { return nil }

func (f *fakeBroker) GetOptionsProduct(ctx context.Context, contractID string) (*gobs.OptionContract, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &gobs.OptionContract{ContractID: contractID, Bid: f.bid, Ask: f.ask, ContractSize: d("100")}, nil
}

func (f *fakeBroker) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	return topic.Subscribe(f.updates, 0, false)
}

func (f *fakeBroker) LimitSellToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	if f.placeHook != nil {
		f.placeHook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, o := range f.orders {
		if !o.Done {
			f.t.Errorf("two live orders: %s is live while placing another", o.ServerOrderID)
		}
		if o.ClientUUID == clientID {
			f.t.Errorf("client id %s reused", clientID)
		}
	}
	o := &fakeOrder{size: numContracts, price: limitPrice}
	o.ServerOrderID = fmt.Sprintf("order-%d", len(f.orders))
	o.ClientUUID = clientID
	o.Side = "SELL"
	o.CreateTime = gobs.RemoteTime{Time: time.Now()}
	o.Status = "OPEN"
	f.orders = append(f.orders, o)
	if f.rejectAll {
		o.Done, o.Status = true, "REJECTED"
	}
	if f.rejectAll || f.sendOnPlace {
		dup := o.SimpleOrder
		f.updates.Send(&dup)
	}
	if !f.fillAtOrBelow.IsZero() && limitPrice.LessThanOrEqual(f.fillAtOrBelow) {
		f.fillLocked(o, numContracts)
	}
	if f.failPlaceAfter {
		return nil, errors.New("timeout")
	}
	dup := o.SimpleOrder
	return &dup, nil
}

func (f *fakeBroker) fillLocked(o *fakeOrder, n decimal.Decimal) {
	o.FilledSize = o.FilledSize.Add(n)
	o.FilledPrice = o.price
	if o.FilledSize.Equal(o.size) {
		o.Done, o.Status = true, "EXECUTED"
	}
	dup := o.SimpleOrder
	f.updates.Send(&dup)
}

func (f *fakeBroker) find(id string) *fakeOrder {
	for _, o := range f.orders {
		if o.ServerOrderID == id {
			return o
		}
	}
	return nil
}

func (f *fakeBroker) Cancel(ctx context.Context, serverID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	o := f.find(serverID)
	if o == nil {
		return os.ErrNotExist
	}
	if o.Done {
		return errors.New("order is already done")
	}
	if !f.fillOnCancel.IsZero() {
		f.fillLocked(o, f.fillOnCancel)
		if o.Done {
			return errors.New("order is already done")
		}
	}
	o.Done, o.Status = true, "CANCELLED"
	return nil
}

func (f *fakeBroker) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.getErr != nil {
		return nil, f.getErr
	}
	o := f.find(serverID)
	if o == nil {
		return nil, os.ErrNotExist
	}
	dup := o.SimpleOrder
	dup.CreateTime.Time = dup.CreateTime.Time.Add(f.getSkew)
	return &dup, nil
}

func (f *fakeBroker) GetOptionsOrderByClientID(ctx context.Context, clientID uuid.UUID) (exchange.OrderDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, o := range f.orders {
		if o.ClientUUID == clientID {
			dup := o.SimpleOrder
			return &dup, nil
		}
	}
	return nil, os.ErrNotExist
}

// fillLive fills the live order completely.
func (f *fakeBroker) fillLive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.orders {
		if !o.Done {
			f.fillLocked(o, o.size.Sub(o.FilledSize))
			return true
		}
	}
	return false
}

func (f *fakeBroker) prices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ps []string
	for _, o := range f.orders {
		ps = append(ps, o.price.String())
	}
	return ps
}

func (f *fakeBroker) numOrders() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.orders)
}

func (f *fakeBroker) numLive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.orders {
		if !o.Done {
			n++
		}
	}
	return n
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

func newTestLimiter(t *testing.T, uid string, numContracts, minPremium string) *OptLimiter {
	v, err := New(uid, "fake", testContract, d("100"), d(numContracts), d(minPremium), decimal.Zero, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	setTestHooks(v)
	return v
}

func setTestHooks(v *OptLimiter) {
	v.session = alwaysOpen
	v.pollInterval = time.Millisecond
}

func runAsync(ctx context.Context, v *OptLimiter, f *fakeBroker, db kv.Database) <-chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- v.Run(ctx, f, f, db) }()
	return errCh
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

func waitErr(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNew(t *testing.T) {
	if _, err := New("u", "fake", testContract, d("100"), d("1.5"), d("1"), decimal.Zero, 0); err == nil {
		t.Error("fractional contracts: want error")
	}
	if _, err := New("u", "fake", testContract, d("100"), d("1"), decimal.Zero, decimal.Zero, 0); err == nil {
		t.Error("zero floor: want error")
	}
	v, err := New("u", "fake", testContract, d("100"), d("1"), d("1"), decimal.Zero, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !v.repriceStep.Equal(DefaultRepriceStep) || v.repriceInterval != DefaultRepriceInterval {
		t.Errorf("defaults not applied: step %s interval %s", v.repriceStep, v.repriceInterval)
	}
}

func TestNextPrice(t *testing.T) {
	v, err := New("u", "fake", testContract, d("100"), d("1"), d("0.50"), decimal.Zero, 0)
	if err != nil {
		t.Fatal(err)
	}
	quote := func(bid, ask string) *gobs.OptionContract {
		return &gobs.OptionContract{Bid: d(bid), Ask: d(ask)}
	}
	tests := []struct {
		name     string
		bid, ask string
		floor    string
		k        int
		last     string
		want     string
	}{
		{"mid", "1.00", "1.40", "0.50", 0, "0", "1.2"},
		{"step 1", "1.00", "1.40", "0.50", 1, "1.20", "1.12"},
		{"step 4 stops at bid", "1.00", "1.40", "0.50", 4, "1.04", "1"},
		{"floor above bid", "1.00", "1.40", "1.10", 3, "1.12", "1.1"},
		{"floor above mid", "1.00", "1.40", "1.30", 0, "0", "1.3"},
		{"half penny mid rounds up", "1.00", "1.01", "0.50", 0, "0", "1.01"},
		{"at least one tick below last", "1.00", "1.04", "0.50", 1, "1.02", "1.01"},
		{"tick rule never below bid", "1.00", "1.04", "0.50", 2, "1.00", "1"},
		{"market moved above last", "2.00", "2.40", "0.50", 2, "1.20", "2"},
		{"penny below $3.00", "2.98", "3.02", "0.50", 1, "3.00", "2.99"},
		{"nickel tick at $3+", "4.00", "4.50", "0.50", 0, "0", "4.25"},
		{"nickel tick rounds up", "4.00", "4.52", "0.50", 0, "0", "4.3"},
		{"no bid", "0", "0.20", "0.05", 0, "0", "0.1"},
	}
	for _, tc := range tests {
		v.minPremium = d(tc.floor)
		got, err := v.nextPrice(quote(tc.bid, tc.ask), tc.k, d(tc.last))
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !got.Equal(d(tc.want)) {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}

	for _, q := range [][2]string{{"1", "0"}, {"1.2", "1.1"}, {"-1", "1"}} {
		if _, err := v.nextPrice(quote(q[0], q[1]), 0, decimal.Zero); err == nil {
			t.Errorf("quote %v: want error", q)
		}
	}
}

func TestOrdersOldestFirst(t *testing.T) {
	v := newTestLimiter(t, "u1", "2", "0.50")
	for i, at := range []time.Time{{}, time.Now().Add(-time.Minute)} {
		o, err := exchange.NewSimpleOrder(fmt.Sprintf("order-%d", i), uuid.New(), "SELL")
		if err != nil {
			t.Fatal(err)
		}
		o.CreateTime.Time = at
		v.orders[o.ServerOrderID] = o
	}
	// The order without a create time yet is the newest one.
	if orders := v.Orders(); orders[0].ServerOrderID != "order-1" || orders[1].ServerOrderID != "order-0" {
		t.Errorf("orders = %s, %s; want order-1, order-0", orders[0].ServerOrderID, orders[1].ServerOrderID)
	}
}

func TestRegularSession(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, newYork)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	tests := []struct {
		now  string
		open bool
		next string
	}{
		{"2026-10-01 09:29", false, "2026-10-01 09:30"}, // Thursday
		{"2026-10-01 09:30", true, "2026-10-01 16:00"},
		{"2026-10-01 15:59", true, "2026-10-01 16:00"},
		{"2026-10-01 16:00", false, "2026-10-02 09:30"},
		{"2026-10-02 17:00", false, "2026-10-05 09:30"}, // Friday evening
		{"2026-10-03 12:00", false, "2026-10-05 09:30"}, // Saturday
		{"2026-10-30 16:30", false, "2026-11-02 09:30"}, // across DST end
	}
	for _, tc := range tests {
		open, next := RegularSession(at(tc.now).UTC())
		if open != tc.open || !next.Equal(at(tc.next)) {
			t.Errorf("%s: got (%v, %s), want (%v, %s)", tc.now, open, next.In(newYork), tc.open, tc.next)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	v := newTestLimiter(t, "pos/leg-000001", "2", "0.75")
	v.idgen.NextID()
	o, err := exchange.NewSimpleOrder("order-0", v.idgen.NextID(), "SELL")
	if err != nil {
		t.Fatal(err)
	}
	o.FilledSize, o.FilledPrice, o.Done, o.DoneReason = d("1"), d("1.10"), true, "EXECUTED"
	v.orders[o.ServerOrderID] = o

	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		t.Fatal(err)
	}
	var w *OptLimiter
	if err := kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) (err error) {
		w, err = Load(ctx, v.uid, r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w.contractID != v.contractID || !w.numContracts.Equal(v.numContracts) || !w.minPremium.Equal(v.minPremium) ||
		!w.contractSize.Equal(v.contractSize) || !w.repriceStep.Equal(v.repriceStep) || w.repriceInterval != v.repriceInterval {
		t.Errorf("loaded config differs: %+v", w)
	}
	if w.idgen.Offset() != 2 || w.idgen.Seed() != v.uid {
		t.Errorf("loaded idgen: seed %q offset %d", w.idgen.Seed(), w.idgen.Offset())
	}
	if !w.FilledSize().Equal(d("1")) || !w.FilledValue().Equal(d("1.10")) || !w.PendingSize().Equal(d("1")) {
		t.Errorf("loaded fills: size %s value %s pending %s", w.FilledSize(), w.FilledValue(), w.PendingSize())
	}
	if got := w.orders["order-0"]; got == nil || got.DoneReason != "EXECUTED" || got.ClientUUID != o.ClientUUID {
		t.Errorf("loaded order: %+v", got)
	}
}

func TestRunRepricesUntilFilled(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.fillAtOrBelow = d("1.00")
	v := newTestLimiter(t, "u1", "1", "0.50")

	if err := waitErr(t, runAsync(context.Background(), v, f, db)); err != nil {
		t.Fatal(err)
	}
	// Step is 20% of the 0.40 spread: mid, three steps, then the bid fills.
	if want := []string{"1.2", "1.12", "1.04", "1"}; !equal(f.prices(), want) {
		t.Errorf("prices: got %v, want %v", f.prices(), want)
	}
	if !v.IsDone() || !v.FilledValue().Equal(d("1")) {
		t.Errorf("filled %s value %s", v.FilledSize(), v.FilledValue())
	}
}

func TestRunRestsAtFloor(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	v := newTestLimiter(t, "u1", "1", "1.10")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "order at floor", func() bool { n := len(f.prices()); return n > 0 && f.prices()[n-1] == "1.1" })
	time.Sleep(100 * time.Millisecond) // several re-price intervals
	if want := []string{"1.2", "1.12", "1.1"}; !equal(f.prices(), want) {
		t.Errorf("prices: got %v, want %v", f.prices(), want)
	}
	f.fillLive()
	if err := waitErr(t, errCh); err != nil {
		t.Fatal(err)
	}
	cancel()
}

func TestRunCancelRacesFill(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.fillOnCancel = d("1")
	v := newTestLimiter(t, "u1", "1", "0.50")

	if err := waitErr(t, runAsync(context.Background(), v, f, db)); err != nil {
		t.Fatal(err)
	}
	// The first order filled while being canceled; nothing more is placed.
	if f.numOrders() != 1 || !v.IsDone() {
		t.Errorf("orders %d filled %s", f.numOrders(), v.FilledSize())
	}
}

func TestRunPartialFillPlacesRemainder(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.fillOnCancel = d("1")
	v := newTestLimiter(t, "u1", "2", "0.50")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "second order", func() bool { return f.numOrders() >= 2 })

	f.mu.Lock()
	f.fillOnCancel = decimal.Zero
	second := f.orders[1]
	size := second.size
	f.mu.Unlock()
	if !size.Equal(d("1")) {
		t.Errorf("second order size: got %s, want 1", size)
	}
	f.fillLive()
	if err := waitErr(t, errCh); err != nil {
		t.Fatal(err)
	}
	if !v.FilledSize().Equal(d("2")) {
		t.Errorf("filled %s, want 2", v.FilledSize())
	}
}

func TestRunCancelUsesBrokerDetail(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.sendOnPlace = true
	f.fillOnCancel = d("1")
	f.getSkew = time.Hour
	v := newTestLimiter(t, "u1", "2", "0.50")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "second order", func() bool { return f.numOrders() >= 2 })

	// The canceled order's final detail can't be merged as an update, but
	// its fill still counts: only the remainder is placed.
	f.mu.Lock()
	f.fillOnCancel = decimal.Zero
	size := f.orders[1].size
	f.mu.Unlock()
	if !size.Equal(d("1")) {
		t.Errorf("second order size: got %s, want 1", size)
	}
	f.fillLive()
	if err := waitErr(t, errCh); err != nil {
		t.Fatal(err)
	}
	if !v.FilledSize().Equal(d("2")) {
		t.Errorf("filled %s, want 2", v.FilledSize())
	}
}

func TestRunRejectedOrderWaitsToReprice(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.rejectAll = true
	v := newTestLimiter(t, "u1", "1", "0.50")
	v.repriceInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "first order", func() bool { return f.numOrders() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := f.numOrders(); n != 1 {
		t.Errorf("orders after a rejection: got %d, want 1", n)
	}
	cancel()
	if err := waitErr(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: got %v, want context.Canceled", err)
	}
}

func TestRunStopsWhenUpdatesStop(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	v := newTestLimiter(t, "u1", "1", "0.50")
	v.repriceInterval = time.Hour

	errCh := runAsync(context.Background(), v, f, db)
	waitFor(t, "first order", func() bool { return f.numOrders() == 1 })
	f.updates.Close()
	if err := waitErr(t, errCh); err == nil {
		t.Fatal("Run: want an error once order updates stop")
	}
	if f.numLive() != 0 {
		t.Errorf("live orders after updates stopped: %d", f.numLive())
	}
}

func TestRunCancelGivesUp(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	v := newTestLimiter(t, "u1", "1", "0.50")
	v.repriceInterval = time.Hour
	v.cancelTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "first order", func() bool { return f.numOrders() == 1 })
	f.mu.Lock()
	f.getErr = errors.New("broker is down")
	f.mu.Unlock()
	cancel()
	if err := waitErr(t, errCh); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Run: got %v, want an error confirming the cancel", err)
	}
}

func TestRunStopDuringFailedPlacementIsNotClean(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.failPlaceAfter = true
	v := newTestLimiter(t, "u1", "1", "0.50")

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	placing := make(chan struct{})
	f.placeHook = func() {
		close(placing)
		<-ctx.Done()
	}
	errCh := runAsync(ctx, v, f, db)
	select {
	case <-placing:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not place an order")
	}

	// The owner stops Run while the placement is in flight; the broker then
	// accepts the order but the call fails. The order is live and not in the
	// list, so Run must not report a clean stop.
	cause := errors.New("stopped by the owner")
	cancel(cause)
	if err := waitErr(t, errCh); err == nil || errors.Is(err, cause) {
		t.Fatalf("Run: got %v, want an error other than the stop cause", err)
	}
	if f.numLive() != 1 {
		t.Fatalf("broker live orders: got %d, want 1", f.numLive())
	}
}

func TestSaveKeepsHigherOffset(t *testing.T) {
	db := newTestDB(t)
	v := newTestLimiter(t, "u1", "1", "0.50")
	v.idgen.NextID()
	v.idgen.NextID()
	if err := kv.WithReadWriter(context.Background(), db, v.Save); err != nil {
		t.Fatal(err)
	}
	// An older copy saving later must not move the offset back.
	stale := newTestLimiter(t, "u1", "1", "0.50")
	if err := kv.WithReadWriter(context.Background(), db, stale.Save); err != nil {
		t.Fatal(err)
	}
	w := loadLimiter(t, db, "u1")
	if got := w.idgen.Offset(); got != 2 {
		t.Errorf("offset after a stale save: got %d, want 2", got)
	}
}

func TestRunStopCancelsLiveOrder(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	v := newTestLimiter(t, "u1", "1", "0.50")
	v.repriceInterval = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "first order", func() bool { return f.numOrders() == 1 })
	cancel()
	if err := waitErr(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: got %v, want context.Canceled", err)
	}
	if f.numLive() != 0 || len(v.liveOrders()) != 0 {
		t.Errorf("live orders after stop: broker %d optlimiter %d", f.numLive(), len(v.liveOrders()))
	}
}

func TestRunCancelsAtClose(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	v := newTestLimiter(t, "u1", "1", "0.50")
	v.repriceInterval = time.Hour

	var mu sync.Mutex
	isOpen := true
	v.session = func(t time.Time) (bool, time.Time) {
		mu.Lock()
		defer mu.Unlock()
		if isOpen {
			return true, t.Add(30 * time.Millisecond)
		}
		return false, t.Add(time.Hour)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := runAsync(ctx, v, f, db)
	waitFor(t, "first order", func() bool { return f.numOrders() == 1 })
	mu.Lock()
	isOpen = false
	mu.Unlock()
	waitFor(t, "cancel at close", func() bool { return f.numLive() == 0 })
	time.Sleep(50 * time.Millisecond)
	if f.numOrders() != 1 {
		t.Errorf("placed %d orders outside the session", f.numOrders()-1)
	}
	cancel()
	if err := waitErr(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: got %v", err)
	}
}

func TestRunNextSessionStartsFromMid(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.fillAtOrBelow = d("1.00")
	v := newTestLimiter(t, "u1", "1", "0.50")

	var mu sync.Mutex
	calls := 0
	v.session = func(t time.Time) (bool, time.Time) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch {
		case calls <= 4: // first session: two orders, then the close
			return true, t.Add(time.Hour)
		case calls == 5:
			return false, t.Add(5 * time.Millisecond)
		default:
			return true, t.Add(time.Hour)
		}
	}
	if err := waitErr(t, runAsync(context.Background(), v, f, db)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.2", "1.12", "1.2", "1.12", "1.04", "1"}; !equal(f.prices(), want) {
		t.Errorf("prices: got %v, want %v", f.prices(), want)
	}
}

func loadLimiter(t *testing.T, db kv.Database, uid string) *OptLimiter {
	t.Helper()
	var v *OptLimiter
	if err := kv.WithReader(context.Background(), db, func(ctx context.Context, r kv.Reader) (err error) {
		v, err = Load(ctx, uid, r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	setTestHooks(v)
	return v
}

func TestRecoverLiveOrderPlacedBeforeCrash(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.failPlaceAfter = true
	v := newTestLimiter(t, "u1", "1", "0.50")

	// The broker accepts the order but the call fails, as if we crashed.
	if err := waitErr(t, runAsync(context.Background(), v, f, db)); err == nil {
		t.Fatal("Run: want placement error")
	}
	if f.numLive() != 1 {
		t.Fatalf("broker live orders: %d", f.numLive())
	}

	f.mu.Lock()
	f.failPlaceAfter = false
	f.fillAtOrBelow = d("1.00")
	f.mu.Unlock()

	w := loadLimiter(t, db, "u1")
	if w.idgen.Offset() != 1 || len(w.orders) != 0 {
		t.Fatalf("saved state: offset %d orders %d", w.idgen.Offset(), len(w.orders))
	}
	if err := waitErr(t, runAsync(context.Background(), w, f, db)); err != nil {
		t.Fatal(err)
	}
	// The adopted order is canceled before anything new is placed (the fake
	// fails the test on two live orders) and its client ID is never reused.
	if _, ok := w.orders["order-0"]; !ok {
		t.Error("order placed before the crash was not adopted")
	}
	if !w.IsDone() || f.numLive() != 0 {
		t.Errorf("filled %s live %d", w.FilledSize(), f.numLive())
	}
}

func TestRecoverFilledOrderPlacedBeforeCrash(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.failPlaceAfter = true
	f.fillAtOrBelow = d("2")
	v := newTestLimiter(t, "u1", "1", "0.50")

	if err := waitErr(t, runAsync(context.Background(), v, f, db)); err == nil {
		t.Fatal("Run: want placement error")
	}

	f.mu.Lock()
	f.failPlaceAfter = false
	f.mu.Unlock()

	w := loadLimiter(t, db, "u1")
	if err := waitErr(t, runAsync(context.Background(), w, f, db)); err != nil {
		t.Fatal(err)
	}
	// A filled order missing from the record must not be placed again.
	if f.numOrders() != 1 || !w.IsDone() {
		t.Errorf("orders %d filled %s", f.numOrders(), w.FilledSize())
	}

	x := loadLimiter(t, db, "u1")
	if !x.IsDone() {
		t.Error("adopted fill was not saved")
	}
}

func TestRunCanceledBeforeStartCancelsRecoveredOrder(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	f.failPlaceAfter = true
	v := newTestLimiter(t, "u1", "1", "0.50")

	if err := waitErr(t, runAsync(context.Background(), v, f, db)); err == nil {
		t.Fatal("Run: want placement error")
	}
	if f.numLive() != 1 {
		t.Fatalf("broker live orders: %d", f.numLive())
	}

	// A Run whose context is canceled before it starts still recovers the
	// order and cancels it, without placing anything new.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := loadLimiter(t, db, "u1")
	if err := waitErr(t, runAsync(ctx, w, f, db)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: got %v, want context.Canceled", err)
	}
	if f.numLive() != 0 || f.numOrders() != 1 {
		t.Errorf("broker live %d orders %d", f.numLive(), f.numOrders())
	}
	if _, ok := w.orders["order-0"]; !ok {
		t.Error("order placed before the crash was not adopted")
	}
}

func TestRunRejectsWrongContract(t *testing.T) {
	db := newTestDB(t)
	f := newFakeBroker(t, "1.00", "1.40")
	v, err := New("u1", "fake", "OTHER", d("100"), d("1"), d("0.5"), decimal.Zero, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Run(context.Background(), f, f, db); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("Run: got %v, want os.ErrInvalid", err)
	}
}
