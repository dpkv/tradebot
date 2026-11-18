// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bvk/tradebot/etrade/internal"
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const testContractID = "AAPL261218P00200000"

func newTestDB(t *testing.T) kv.Database {
	bdb, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bdb.Close() })
	return kvbadger.New(bdb, func(string) bool { return true })
}

// fakeOrder is one option order the fake broker knows.
type fakeOrder struct {
	id         int64
	action     string
	strike     int
	qty, price string
	placed     int64 // Unix milliseconds
	status     string
}

func (o *fakeOrder) api() map[string]any {
	return map[string]any{"orderId": o.id, "OrderDetail": []any{map[string]any{
		"status": o.status, "priceType": "LIMIT", "orderTerm": "GOOD_FOR_DAY", "placedTime": o.placed,
		"limitPrice": num(o.price),
		"Instrument": []any{map[string]any{"orderAction": o.action, "orderedQuantity": num(o.qty),
			"Product": map[string]any{"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
				"expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": o.strike}}},
	}}}
}

func num(s string) any { return decimal.RequireFromString(s).InexactFloat64() }

// fakeBroker serves quotes, option order placement, single orders and the
// order list for one account.
type fakeBroker struct {
	t *testing.T

	mu         sync.Mutex
	orders     map[int64]*fakeOrder
	nextID     int64
	failPlace  bool
	onPreview  func()
	placeCalls atomic.Int32
}

func newFakeBroker(t *testing.T) *fakeBroker {
	return &fakeBroker{t: t, orders: make(map[int64]*fakeOrder), nextID: 100}
}

func (b *fakeBroker) add(o *fakeOrder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.orders[o.id] = o
}

func (b *fakeBroker) handler() http.Handler {
	mux := http.NewServeMux()
	base := "/v1/accounts/" + testAccountIDKey + "/orders"
	mux.HandleFunc("/v1/market/quote/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"QuoteResponse": map[string]any{"QuoteData": []any{map[string]any{
			"Product": map[string]any{"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
				"expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": 200},
			"Option": map[string]any{"bid": 1.0, "ask": 1.2, "OptionGreeks": map[string]any{"delta": -0.25}},
		}}}})
	})
	mux.HandleFunc(base+"/preview", func(w http.ResponseWriter, r *http.Request) {
		if b.onPreview != nil {
			b.onPreview()
		}
		if b.failPlace {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"PreviewOrderResponse": map[string]any{"PreviewIds": []any{map[string]any{"previewId": 1}}}})
	})
	mux.HandleFunc(base+"/place", func(w http.ResponseWriter, r *http.Request) {
		b.placeCalls.Add(1)
		b.mu.Lock()
		b.nextID++
		id := b.nextID
		b.orders[id] = &fakeOrder{id: id, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: time.Now().UnixMilli(), status: "OPEN"}
		b.mu.Unlock()
		writeJSON(w, map[string]any{"PlaceOrderResponse": map[string]any{"OrderIds": []any{map[string]any{"orderId": id}}}})
	})
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		var list []any
		for _, o := range b.orders {
			list = append(list, o.api())
		}
		writeJSON(w, map[string]any{"OrdersResponse": map[string]any{"Order": list}})
	})
	mux.HandleFunc(base+"/", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, base+"/"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		b.mu.Lock()
		o, ok := b.orders[id]
		b.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"OrdersResponse": map[string]any{"Order": []any{o.api()}}})
	})
	return mux
}

func openTestOptionProduct(t *testing.T, c *Client, db kv.Database) *OptionProduct {
	t.Helper()
	p, err := newOptionProduct(context.Background(), db, c, c.creds.Load().Accounts[0], testContractID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestOptionProductSellToOpen(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	db := newTestDB(t)
	p := openTestOptionProduct(t, c, db)

	if p.ContractID() != testContractID || p.Underlying() != "AAPL" || p.OptionType() != "PUT" ||
		!p.Strike().Equal(decimal.NewFromInt(200)) || !p.ContractSize().Equal(decimal.NewFromInt(100)) {
		t.Fatalf("contract identity is wrong: %s %s %s %s", p.ContractID(), p.Underlying(), p.OptionType(), p.Strike())
	}

	uid := uuid.New()
	// The entry is written before the broker sees the order.
	b.onPreview = func() {
		e, err := getOptionOrderEntry(ctx, db, uid)
		if err != nil || e.ServerOrderID != 0 || e.ContractID != testContractID || e.Placement.Side != OptionSellOpen {
			t.Errorf("entry at preview = %+v, %v; want a write-ahead entry", e, err)
		}
	}
	order, err := p.LimitSellToOpen(ctx, uid, decimal.NewFromInt(1), decimal.RequireFromString("1.1"))
	if err != nil {
		t.Fatal(err)
	}
	b.onPreview = nil
	if order.ClientID() != uid || order.ServerID() == "" {
		t.Fatalf("order = %+v", order)
	}
	e, err := getOptionOrderEntry(ctx, db, uid)
	if err != nil || strconv.FormatInt(e.ServerOrderID, 10) != order.ServerID() {
		t.Fatalf("entry after place = %+v, %v", e, err)
	}

	// The same client ID returns the existing order without placing again.
	again, err := p.LimitSellToOpen(ctx, uid, decimal.NewFromInt(1), decimal.RequireFromString("1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ServerID() != order.ServerID() || again.ClientID() != uid || b.placeCalls.Load() != 1 {
		t.Fatalf("second call: order %s, place calls %d", again.ServerID(), b.placeCalls.Load())
	}

	// Get restores the client ID.
	detail, err := p.Get(ctx, order.ServerID())
	if err != nil || detail.ClientID() != uid {
		t.Fatalf("Get = %v, %v; want client id %s", detail, err, uid)
	}

	g, err := p.GetGreeks(ctx)
	if err != nil || !g.Delta.Equal(decimal.RequireFromString("-0.25")) {
		t.Fatalf("greeks = %+v, %v", g, err)
	}
}

func TestOptionProductPlaceFailure(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t)
	b.failPlace = true
	c := newTestClient(t, b.handler())
	db := newTestDB(t)
	p := openTestOptionProduct(t, c, db)

	uid := uuid.New()
	if _, err := p.LimitSellToOpen(ctx, uid, decimal.NewFromInt(1), decimal.RequireFromString("1.1")); err == nil {
		t.Fatal("want a placement error")
	}
	// The entry stays, with no server ID, for the lookup to resolve.
	if e, err := getOptionOrderEntry(ctx, db, uid); err != nil || e.ServerOrderID != 0 {
		t.Fatalf("entry = %+v, %v", e, err)
	}
	// The same client ID is never placed twice.
	b.failPlace = false
	if _, err := p.LimitSellToOpen(ctx, uid, decimal.NewFromInt(1), decimal.RequireFromString("1.1")); err == nil {
		t.Fatal("want an error for a client id with an unknown outcome")
	}
	if n := b.placeCalls.Load(); n != 0 {
		t.Fatalf("place calls = %d, want 0", n)
	}
}

func TestLookupOptionOrder(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	db := newTestDB(t)

	now := time.Now().UnixMilli()
	placement := orderPlacementInfo{RequestTimeMilli: now, Side: OptionSellOpen, Price: decimal.RequireFromString("1.1"),
		Qty: decimal.NewFromInt(1), PriceType: "LIMIT", OrderTerm: "GOOD_FOR_DAY", MarketSession: "REGULAR"}
	set := func(uid uuid.UUID, e *optionOrderEntry) {
		if err := setOptionOrderEntry(ctx, db, uid, e); err != nil {
			t.Fatal(err)
		}
	}

	// No entry.
	if _, err := lookupOptionOrder(ctx, db, c, testAccountID, uuid.New()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no entry: err = %v, want os.ErrNotExist", err)
	}

	// Another account's entry is invisible.
	other := uuid.New()
	set(other, &optionOrderEntry{AccountID: "someone-else", ContractID: testContractID, ServerOrderID: 1, Placement: placement})
	if _, err := lookupOptionOrder(ctx, db, c, testAccountID, other); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other account: err = %v, want os.ErrNotExist", err)
	}

	// Known server ID.
	b.add(&fakeOrder{id: 7, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: now, status: "EXECUTED"})
	known := uuid.New()
	set(known, &optionOrderEntry{AccountID: testAccountID, ContractID: testContractID, ServerOrderID: 7, Placement: placement})
	if o, err := lookupOptionOrder(ctx, db, c, testAccountID, known); err != nil || o.OrderID != 7 || o.ClientUUID != known {
		t.Fatalf("known server id: %+v, %v", o, err)
	}

	// Unknown server ID: order 7 is claimed by the known entry, and orders on
	// another strike, with another action or placed too long ago don't match.
	b.add(&fakeOrder{id: 8, action: "SELL_OPEN", strike: 205, qty: "1", price: "1.1", placed: now, status: "OPEN"})
	b.add(&fakeOrder{id: 9, action: "BUY_CLOSE", strike: 200, qty: "1", price: "1.1", placed: now, status: "OPEN"})
	b.add(&fakeOrder{id: 10, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: now - 120_000, status: "OPEN"})
	pending := uuid.New()
	set(pending, &optionOrderEntry{AccountID: testAccountID, ContractID: testContractID, Placement: placement})
	if _, err := lookupOptionOrder(ctx, db, c, testAccountID, pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no match: err = %v, want os.ErrNotExist", err)
	}

	// One match is adopted and saved.
	b.add(&fakeOrder{id: 11, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: now + 2_000, status: "EXECUTED"})
	o, err := lookupOptionOrder(ctx, db, c, testAccountID, pending)
	if err != nil || o.OrderID != 11 || o.ClientUUID != pending {
		t.Fatalf("one match: %+v, %v", o, err)
	}
	if e, err := getOptionOrderEntry(ctx, db, pending); err != nil || e.ServerOrderID != 11 || !e.Done {
		t.Fatalf("saved entry = %+v, %v", e, err)
	}

	// Two matches are an error, never a guess.
	b.add(&fakeOrder{id: 12, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: now, status: "OPEN"})
	b.add(&fakeOrder{id: 13, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: now, status: "OPEN"})
	ambiguous := uuid.New()
	set(ambiguous, &optionOrderEntry{AccountID: testAccountID, ContractID: testContractID, Placement: placement})
	if _, err := lookupOptionOrder(ctx, db, c, testAccountID, ambiguous); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("two matches: err = %v, want an ambiguity error", err)
	}
}

func TestPruneOptionOrders(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	old, recent := uuid.New(), uuid.New()
	setOptionOrderEntry(ctx, db, old, &optionOrderEntry{AccountID: testAccountID, ContractID: "AAPL250117P00200000"})
	setOptionOrderEntry(ctx, db, recent, &optionOrderEntry{AccountID: testAccountID, ContractID: testContractID})

	now := time.Date(2026, 11, 18, 0, 0, 0, 0, newYork)
	n, err := pruneOptionOrders(ctx, db, now)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	if _, err := getOptionOrderEntry(ctx, db, old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old entry: err = %v, want os.ErrNotExist", err)
	}
	if _, err := getOptionOrderEntry(ctx, db, recent); err != nil {
		t.Fatalf("recent entry: %v", err)
	}
}

func TestOptionProductOrderUpdates(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	db := newTestDB(t)
	p := openTestOptionProduct(t, c, db)

	uid := uuid.New()
	order, err := p.LimitSellToOpen(ctx, uid, decimal.NewFromInt(1), decimal.RequireFromString("1.1"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(order.ServerID(), 10, 64)

	r, err := p.GetOrderUpdates()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Another account's update on the contract is filtered out; ours arrives
	// with the client ID restored.
	update := func(account, status string) *internal.Order {
		return &internal.Order{AccountID: account, OrderID: id, Status: status, Symbol: "AAPL", SecurityType: "OPTN",
			CallPut: "PUT", ExpiryYear: 2026, ExpiryMonth: 12, ExpiryDay: 18, StrikePrice: decimal.NewFromInt(200)}
	}
	c.publishOrder(update("someone-else", "EXECUTED"))
	c.publishOrder(update(testAccountID, "EXECUTED"))
	// Filtered updates arrive as nil, as with equity products.
	got, err := r.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("other account's update = %+v, want nil", got)
	}
	if got, err = r.Receive(); err != nil {
		t.Fatal(err)
	}
	if got.ClientID() != uid || !got.IsDone() {
		t.Fatalf("update = %+v", got)
	}

	// The finished order's entry is marked done.
	deadline := time.Now().Add(5 * time.Second)
	for {
		e, err := getOptionOrderEntry(ctx, db, uid)
		if err == nil && e.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("entry = %+v, %v; want Done", e, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOptionProductSharedWatch(t *testing.T) {
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	db := newTestDB(t)
	account := c.creds.Load().Accounts[0]

	p1, err := newOptionProduct(context.Background(), db, c, account, testContractID)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := newOptionProduct(context.Background(), db, c, account, testContractID)
	if err != nil {
		t.Fatal(err)
	}
	p1.Close()
	r, err := p2.GetPriceUpdates()
	if err != nil {
		t.Fatalf("price updates after the other product closed: %v", err)
	}
	r.Close()
	p2.Close()
	if tp := c.getOptionPriceTopic(testContractID); tp != nil {
		t.Fatal("contract still watched after both products closed")
	}
}

func TestOptionProductGetOtherContract(t *testing.T) {
	b := newFakeBroker(t)
	b.add(&fakeOrder{id: 5, action: "SELL_OPEN", strike: 205, qty: "1", price: "1.1", placed: time.Now().UnixMilli(), status: "OPEN"})
	c := newTestClient(t, b.handler())
	p := openTestOptionProduct(t, c, newTestDB(t))
	if _, err := p.Get(context.Background(), "5"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist for another contract's order", err)
	}
}
