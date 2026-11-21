// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// fakePlaceGateway answers every order placement with a fixed response body
// and counts the placements it receives.
type fakePlaceGateway struct {
	mu         sync.Mutex
	response   string
	placeCalls int
}

func (g *fakePlaceGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if r.Method == http.MethodPost && r.URL.Path == "/v1/api/iserver/account/U1/orders" {
		g.placeCalls++
		w.Write([]byte(g.response))
		return
	}
	http.NotFound(w, r)
}

func (g *fakePlaceGateway) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.placeCalls
}

func newTestProduct(t *testing.T, g http.Handler) *Product {
	v := newTestExchange(t, g)
	return &Product{exchange: v, client: v.client, symbol: "SOXL", conid: 1}
}

// listedOrder is an order as goWatchOrders reports it from the gateway's
// orders list: a rejection and a cancellation both have Status "Cancelled".
func listedOrder(clientID uuid.UUID, ccpStatus string) *internal.Order {
	return &internal.Order{
		OrderID:       1052773499,
		ClientOrderID: clientID.String(),
		Symbol:        "SOXL",
		Side:          "SELL",
		Status:        "Cancelled",
		CCPStatus:     ccpStatus,
	}
}

var (
	testSize  = decimal.NewFromInt(100)
	testPrice = decimal.RequireFromString("41.08")
)

// The gateway's own rejection of a placement must be reported as
// exchange.ErrRejected, and a retry with the same client ID must return the
// cached rejection without placing again.
func TestPlaceOrderGatewayRejection(t *testing.T) {
	g := &fakePlaceGateway{response: `[{"error":"Your account has a minimum of 15 orders working on either the buy or sell side for this particular contract."}]`}
	p := newTestProduct(t, g)
	cid := uuid.New()

	for attempt := 1; attempt <= 2; attempt++ {
		order, err := p.LimitSell(context.Background(), cid, testSize, testPrice)
		if !errors.Is(err, exchange.ErrRejected) {
			t.Fatalf("attempt %d: err = %v, want exchange.ErrRejected", attempt, err)
		}
		if order != nil {
			t.Fatalf("attempt %d: order = %v, want nil", attempt, order)
		}
	}
	if n := g.calls(); n != 1 {
		t.Errorf("placement requests = %d, want 1", n)
	}
}

// A rejected order saved from the orders list must not be returned as a
// placed order (this made the limiter place a new order about every 20s).
func TestPlaceOrderSavedRejectedOrder(t *testing.T) {
	g := &fakePlaceGateway{}
	p := newTestProduct(t, g)
	cid := uuid.New()
	p.exchange.orderMap.Store(cid, listedOrder(cid, "Rejected"))

	order, err := p.LimitSell(context.Background(), cid, testSize, testPrice)
	if !errors.Is(err, exchange.ErrRejected) {
		t.Fatalf("err = %v, want exchange.ErrRejected", err)
	}
	if order != nil {
		t.Fatalf("order = %v, want nil", order)
	}
	if n := g.calls(); n != 0 {
		t.Errorf("placement requests = %d, want 0", n)
	}
}

// goWatchOrderUpdates replaces a cached placement error with the listed
// order; a rejected one must still come back as exchange.ErrRejected.
func TestPlaceOrderCachedRejectedOrder(t *testing.T) {
	g := &fakePlaceGateway{}
	p := newTestProduct(t, g)
	cid := uuid.New()
	p.clientIDStatusMap.Store(cid, &clientIDStatus{order: listedOrder(cid, "Rejected")})

	order, err := p.LimitSell(context.Background(), cid, testSize, testPrice)
	if !errors.Is(err, exchange.ErrRejected) {
		t.Fatalf("err = %v, want exchange.ErrRejected", err)
	}
	if order != nil {
		t.Fatalf("order = %v, want nil", order)
	}
	if n := g.calls(); n != 0 {
		t.Errorf("placement requests = %d, want 0", n)
	}
}

// An order that was accepted and later cancelled is still returned as is, so
// the limiter records it as done like before.
func TestPlaceOrderCancelledOrderNotRejected(t *testing.T) {
	g := &fakePlaceGateway{}
	p := newTestProduct(t, g)

	saved := uuid.New()
	p.exchange.orderMap.Store(saved, listedOrder(saved, "Cancelled"))
	cached := uuid.New()
	p.clientIDStatusMap.Store(cached, &clientIDStatus{order: listedOrder(cached, "")})

	for _, cid := range []uuid.UUID{saved, cached} {
		order, err := p.LimitSell(context.Background(), cid, testSize, testPrice)
		if err != nil {
			t.Fatalf("client id %s: err = %v, want nil", cid, err)
		}
		if order == nil || order.ClientID() != cid {
			t.Fatalf("client id %s: order = %v, want the listed order", cid, order)
		}
	}
	if n := g.calls(); n != 0 {
		t.Errorf("placement requests = %d, want 0", n)
	}
}
