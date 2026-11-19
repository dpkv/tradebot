// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/shopspring/decimal"
)

// fakeGateway mimics the CP Gateway's trades endpoint, which answers the
// first request(s) on a cold cache with an empty list before returning
// executions.
type fakeGateway struct {
	mu           sync.Mutex
	coldRequests int // number of trades requests answered with []
	tradesJSON   string
	tradesStatus int // non-zero overrides the trades endpoint's status code
	accountCalls int
	tradeDays    []string
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch r.URL.Path {
	case "/v1/api/iserver/accounts":
		g.accountCalls++
		w.Write([]byte(`{"accounts":["All","U1"]}`))
	case "/v1/api/iserver/account/trades":
		g.tradeDays = append(g.tradeDays, r.URL.Query().Get("days"))
		if g.tradesStatus != 0 {
			w.WriteHeader(g.tradesStatus)
			return
		}
		if g.coldRequests > 0 {
			g.coldRequests--
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(g.tradesJSON))
	default:
		http.NotFound(w, r)
	}
}

func newTestExchange(t *testing.T, g *fakeGateway) *Exchange {
	old := commissionLookupDelay
	commissionLookupDelay = time.Millisecond
	t.Cleanup(func() { commissionLookupDelay = old })

	srv := httptest.NewTLSServer(g)
	t.Cleanup(srv.Close)
	return &Exchange{
		client: &Client{
			creds:      Credentials{GatewayURL: srv.URL, AccountID: "U1"},
			httpClient: *srv.Client(),
		},
	}
}

// Shaped like real gateway output: commission is a string, and a partially
// filled order has one record per execution.
const testTradesJSON = `[
  {"execution_id":"e1","order_ref":"order-a","order_id":11,"symbol":"SOXL","side":"S","size":55.0,"price":"51.10","commission":"0.06","trade_time_r":1794844400000},
  {"execution_id":"e2","order_ref":"order-b","order_id":12,"symbol":"SMCI","side":"B","size":100.0,"price":"24.00","commission":"1.0","trade_time_r":1794844401000},
  {"execution_id":"e3","order_ref":"order-a","order_id":11,"symbol":"SOXL","side":"S","size":45.0,"price":"51.10","commission":"1.05","trade_time_r":1794844404000}
]`

func filledOrder(clientOrderID string) *internal.Order {
	return &internal.Order{
		ClientOrderID:  clientOrderID,
		Status:         "Filled",
		FilledQty:      decimal.NewFromInt(100),
		FillObservedAt: 1, // provisional value set by markFillObserved
	}
}

func TestFetchCommissionColdCache(t *testing.T) {
	g := &fakeGateway{tradesJSON: testTradesJSON, coldRequests: 1}
	v := newTestExchange(t, g)

	order := filledOrder("order-a")
	v.fetchCommission(context.Background(), order)

	if want := decimal.RequireFromString("1.11"); !order.Commission.Equal(want) {
		t.Errorf("Commission = %s, want %s (sum over executions)", order.Commission, want)
	}
	if want := int64(1794844404000); order.FillObservedAt != want {
		t.Errorf("FillObservedAt = %d, want %d (latest execution)", order.FillObservedAt, want)
	}
	if g.accountCalls != 1 {
		t.Errorf("accounts called %d times, want 1", g.accountCalls)
	}
	if want := []string{"7", "7"}; !slices.Equal(g.tradeDays, want) {
		t.Errorf("trades lookbacks = %v, want %v (same days on every attempt)", g.tradeDays, want)
	}
}

func TestFetchCommissionWarmCache(t *testing.T) {
	g := &fakeGateway{tradesJSON: testTradesJSON}
	v := newTestExchange(t, g)

	order := filledOrder("order-b")
	v.fetchCommission(context.Background(), order)

	if want := decimal.RequireFromString("1.0"); !order.Commission.Equal(want) {
		t.Errorf("Commission = %s, want %s", order.Commission, want)
	}
	if want := int64(1794844401000); order.FillObservedAt != want {
		t.Errorf("FillObservedAt = %d, want %d", order.FillObservedAt, want)
	}
	if g.accountCalls != 0 {
		t.Errorf("accounts called %d times, want 0", g.accountCalls)
	}
	if want := []string{"7"}; !slices.Equal(g.tradeDays, want) {
		t.Errorf("trades lookbacks = %v, want %v", g.tradeDays, want)
	}
}

func TestFetchCommissionNoMatch(t *testing.T) {
	g := &fakeGateway{tradesJSON: testTradesJSON}
	v := newTestExchange(t, g)

	order := filledOrder("order-missing")
	v.fetchCommission(context.Background(), order)

	if !order.Commission.IsZero() {
		t.Errorf("Commission = %s, want 0", order.Commission)
	}
	if order.FillObservedAt != 1 {
		t.Errorf("FillObservedAt = %d, want provisional value 1 kept", order.FillObservedAt)
	}
	if len(g.tradeDays) != commissionLookupAttempts {
		t.Errorf("trades requested %d times, want %d", len(g.tradeDays), commissionLookupAttempts)
	}
	if g.accountCalls != 1 {
		t.Errorf("accounts called %d times, want 1", g.accountCalls)
	}
}

func TestFetchCommissionUnfilledOrder(t *testing.T) {
	g := &fakeGateway{tradesJSON: testTradesJSON}
	v := newTestExchange(t, g)

	order := filledOrder("order-a")
	order.Status = "Cancelled"
	order.FilledQty = decimal.Zero
	v.fetchCommission(context.Background(), order)

	if len(g.tradeDays) != 0 || g.accountCalls != 0 {
		t.Errorf("unfilled order made %d trades and %d accounts requests, want none", len(g.tradeDays), g.accountCalls)
	}
	if !order.Commission.IsZero() || order.FillObservedAt != 1 {
		t.Errorf("unfilled order changed: Commission=%s FillObservedAt=%d", order.Commission, order.FillObservedAt)
	}
}

func TestFetchCommissionTradesError(t *testing.T) {
	g := &fakeGateway{tradesJSON: testTradesJSON, tradesStatus: http.StatusInternalServerError}
	v := newTestExchange(t, g)

	order := filledOrder("order-a")
	v.fetchCommission(context.Background(), order)

	if !order.Commission.IsZero() || order.FillObservedAt != 1 {
		t.Errorf("order changed on trades error: Commission=%s FillObservedAt=%d", order.Commission, order.FillObservedAt)
	}
	if len(g.tradeDays) != 1 || g.accountCalls != 0 {
		t.Errorf("trades error made %d trades and %d accounts requests, want 1 and 0", len(g.tradeDays), g.accountCalls)
	}
}
