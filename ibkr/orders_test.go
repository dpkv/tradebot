// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// fakeOrdersGateway mimics the gateway's orders list, single-order status,
// trades and cancel endpoints.
type fakeOrdersGateway struct {
	mu sync.Mutex

	lists    map[string]string // orders list body by filters value
	notReady map[string]int    // snapshot=false answers left, by filters value
	statuses map[int64]string  // order status body by order id
	busy     bool              // order status answers a plain 503
	trades   string            // trades body; "[]" if empty

	filters      []string // filters value of each orders list request
	statusCalls  int
	cancelledIDs []int64
}

func (g *fakeOrdersGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	const statusPrefix = "/v1/api/iserver/account/order/status/"
	const cancelPrefix = "/v1/api/iserver/account/U1/order/"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api/iserver/account/orders":
		f := r.URL.Query().Get("filters")
		g.filters = append(g.filters, f)
		if g.notReady[f] > 0 {
			g.notReady[f]--
			w.Write([]byte(`{"orders":[],"snapshot":false}`))
			return
		}
		body, ok := g.lists[f]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"Bad Request: Invalid filter","statusCode":400}`))
			return
		}
		w.Write([]byte(body))

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, statusPrefix):
		g.statusCalls++
		if g.busy {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"Service Unavailable"}`))
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, statusPrefix), 10, 64)
		body, ok := g.statuses[id]
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":"Order %d is not found","statusCode":503}`, id)
			return
		}
		w.Write([]byte(body))

	case r.Method == http.MethodGet && r.URL.Path == "/v1/api/iserver/account/trades":
		if g.trades == "" {
			w.Write([]byte(`[]`))
			return
		}
		w.Write([]byte(g.trades))

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, cancelPrefix):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, cancelPrefix), 10, 64)
		g.cancelledIDs = append(g.cancelledIDs, id)
		w.Write([]byte(`{"msg":"Request was submitted"}`))

	default:
		http.NotFound(w, r)
	}
}

func (g *fakeOrdersGateway) requestedFilters() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.filters)
}

// ordersBody is an orders list response with the given order objects.
func ordersBody(orders ...string) string {
	return `{"orders":[` + strings.Join(orders, ",") + `],"snapshot":true}`
}

func listOrder(id int64, ref, status string) string {
	return fmt.Sprintf(`{"orderId":%d,"order_ref":%q,"ticker":"SOXL","secType":"STK","side":"SELL","status":%q,"totalSize":100.0,"filledQuantity":0.0,"price":"41.08"}`, id, ref, status)
}

// statusBody is shaped like a real order status response.
func statusBody(id int64, status, cumFill, avgPrice string) string {
	s := fmt.Sprintf(`{"order_id":%d,"conid":73340487,"symbol":"SOXL","sec_type":"STK","side":"S","order_status":%q,"cum_fill":%q,"total_size":"100.0","limit_price":"41.08","order_ccp_status":"2"`, id, status, cumFill)
	if avgPrice != "" {
		s += fmt.Sprintf(`,"average_price":%q`, avgPrice)
	}
	return s + "}"
}

func shortenOrderRetryDelays(t *testing.T) {
	oldOrders, oldMiss := ordersRetryDelay, missRetryDelay
	ordersRetryDelay, missRetryDelay = time.Millisecond, time.Millisecond
	t.Cleanup(func() { ordersRetryDelay, missRetryDelay = oldOrders, oldMiss })
}

func newOrdersGateway() *fakeOrdersGateway {
	return &fakeOrdersGateway{
		lists: map[string]string{
			openAndFilledFilter: ordersBody(),
			cancelledFilter:     ordersBody(),
		},
		notReady: map[string]int{},
		statuses: map[int64]string{},
	}
}

func TestGetOrdersMergesFilteredLists(t *testing.T) {
	g := newOrdersGateway()
	g.lists[openAndFilledFilter] = ordersBody(
		listOrder(1, "a", "Submitted"),
		listOrder(2, "b", "Filled"),
		listOrder(3, "c", "Submitted"), // cancelled before the second request
	)
	g.lists[cancelledFilter] = ordersBody(
		listOrder(3, "c", "Cancelled"),
		listOrder(4, "d", "Cancelled"),
	)
	v := newTestExchange(t, g)

	orders, err := v.client.GetOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range orders {
		got = append(got, fmt.Sprintf("%d:%s", o.OrderID, o.Status))
	}
	if want := []string{"1:Submitted", "2:Filled", "3:Cancelled", "4:Cancelled"}; !slices.Equal(got, want) {
		t.Errorf("orders = %v, want %v", got, want)
	}
	if want := []string{openAndFilledFilter, cancelledFilter}; !slices.Equal(g.requestedFilters(), want) {
		t.Errorf("requested filters = %v, want %v", g.requestedFilters(), want)
	}
}

// The first request with a filter answers snapshot=false with an empty list.
func TestGetOrdersRetriesWhileListLoads(t *testing.T) {
	shortenOrderRetryDelays(t)
	g := newOrdersGateway()
	g.lists[openAndFilledFilter] = ordersBody(listOrder(1, "a", "Submitted"))
	g.notReady[openAndFilledFilter] = 1
	v := newTestExchange(t, g)

	orders, err := v.client.GetOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].OrderID != 1 {
		t.Errorf("orders = %v, want order 1", orders)
	}
	if want := []string{openAndFilledFilter, openAndFilledFilter, cancelledFilter}; !slices.Equal(g.requestedFilters(), want) {
		t.Errorf("requested filters = %v, want %v", g.requestedFilters(), want)
	}
}

// A list that never finishes loading is an error, never an empty result.
func TestGetOrdersFailsWhileListStaysIncomplete(t *testing.T) {
	shortenOrderRetryDelays(t)
	g := newOrdersGateway()
	g.notReady[cancelledFilter] = 100
	v := newTestExchange(t, g)

	orders, err := v.client.GetOrders(context.Background())
	if !errors.Is(err, errOrdersNotReady) {
		t.Fatalf("GetOrders = %v, %v; want errOrdersNotReady", orders, err)
	}
	var n int
	for _, f := range g.requestedFilters() {
		if f == cancelledFilter {
			n++
		}
	}
	if n != 5 {
		t.Errorf("cancelled list requested %d times, want 5", n)
	}
}

func TestGetOrdersWithoutSnapshotField(t *testing.T) {
	g := newOrdersGateway()
	g.lists[openAndFilledFilter] = `{"orders":[` + listOrder(1, "a", "Submitted") + `]}`
	v := newTestExchange(t, g)

	orders, err := v.client.GetOrders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Errorf("got %d orders, want 1", len(orders))
	}
}

func TestGetOrderStatus(t *testing.T) {
	g := newOrdersGateway()
	g.statuses[10] = statusBody(10, "Filled", "100.0", "41.08")
	g.statuses[11] = `{"order_id":11,"symbol":"SOXL","side":"S","order_status":"Cancelled","cum_fill":"0.0","total_size":"100.0","limit_price":"41.08","order_ccp_status":"8","order_cancellation_by_system_reason":"Rejected by System:\nYour account has a minimum of 15 orders working"}`
	v := newTestExchange(t, g)
	ctx := context.Background()

	filled, err := v.client.GetOrderStatus(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if filled.Status != "Filled" || filled.Side != "SELL" || !filled.IsDone() {
		t.Errorf("order 10 = status %q side %q done %v, want Filled SELL done", filled.Status, filled.Side, filled.IsDone())
	}
	if want := decimal.RequireFromString("4108"); !filled.ExecutedValue().Equal(want) {
		t.Errorf("order 10 executed value = %s, want %s", filled.ExecutedValue(), want)
	}
	if filled.ClientOrderID != "" {
		t.Errorf("order 10 client order id = %q, want empty", filled.ClientOrderID)
	}

	rejected, err := v.client.GetOrderStatus(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if !rejected.IsRejected() || !rejected.IsDone() {
		t.Errorf("order 11 rejected %v done %v, want both", rejected.IsRejected(), rejected.IsDone())
	}

	if _, err := v.client.GetOrderStatus(ctx, 12); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unknown order: err = %v, want os.ErrNotExist", err)
	}

	g.busy = true
	if _, err := v.client.GetOrderStatus(ctx, 10); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("busy gateway: err = %v, want an error that is not os.ErrNotExist", err)
	}
}

// After a restart, an order the gateway reports as working but leaves out
// of the lists must not be reported missing: that made limiters mark live
// orders cancelled and place duplicates.
func TestGetWorkingOrderMissingFromListIsNotNotFound(t *testing.T) {
	g := newOrdersGateway()
	g.statuses[20] = statusBody(20, "PreSubmitted", "0.0", "")
	p := newTestProduct(t, g)

	order, err := p.Get(context.Background(), "20")
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get = %v, %v; want an error that is not os.ErrNotExist", order, err)
	}
}

// A filled order missing from the lists is found by order status, with its
// client order id recovered from its executions.
func TestGetFilledOrderMissingFromList(t *testing.T) {
	g := newOrdersGateway()
	cid := uuid.New()
	g.statuses[21] = statusBody(21, "Filled", "100.0", "41.08")
	g.trades = fmt.Sprintf(`[{"execution_id":"e1","order_ref":%q,"order_id":21,"symbol":"SOXL","side":"S","size":100.0,"price":"41.08","commission":"1.02","trade_time_r":1795222427095}]`, cid)
	p := newTestProduct(t, g)

	detail, err := p.Get(context.Background(), "21")
	if err != nil {
		t.Fatal(err)
	}
	if detail.ClientID() != cid || !detail.IsDone() {
		t.Errorf("order = client id %s done %v, want %s done", detail.ClientID(), detail.IsDone(), cid)
	}
	if want := decimal.NewFromInt(100); !detail.ExecutedSize().Equal(want) {
		t.Errorf("executed size = %s, want %s", detail.ExecutedSize(), want)
	}
	if want := decimal.RequireFromString("1.02"); !detail.ExecutedFee().Equal(want) {
		t.Errorf("fee = %s, want %s", detail.ExecutedFee(), want)
	}
}

// A cancelled order without fills has no executions to recover its client
// order id from; reporting it as not found records it as cancelled, which
// it was. It is settled by one status lookup, without retries.
func TestGetCancelledOrderWithoutFillIsNotFound(t *testing.T) {
	g := newOrdersGateway()
	g.statuses[22] = statusBody(22, "Cancelled", "0.0", "")
	p := newTestProduct(t, g)

	if _, err := p.Get(context.Background(), "22"); err != os.ErrNotExist {
		t.Fatalf("Get err = %v, want os.ErrNotExist", err)
	}
	if g.statusCalls != 1 {
		t.Errorf("order status requested %d times, want 1", g.statusCalls)
	}
}

func TestGetUnknownOrderIsNotFoundAfterRetries(t *testing.T) {
	shortenOrderRetryDelays(t)
	g := newOrdersGateway()
	p := newTestProduct(t, g)

	if _, err := p.Get(context.Background(), "23"); err != os.ErrNotExist {
		t.Fatalf("Get err = %v, want os.ErrNotExist", err)
	}
	if g.statusCalls != 3 {
		t.Errorf("order status requested %d times, want 3", g.statusCalls)
	}
}

// pendingCancelProduct returns a product whose order 30 was cancelled long
// enough ago that Get stops waiting for the orders list to show it done.
func pendingCancelProduct(t *testing.T, g *fakeOrdersGateway, cid uuid.UUID) *Product {
	p := newTestProduct(t, g)
	order := listedOrder(cid, "")
	order.OrderID = 30
	order.Status = "Submitted"
	order.OrderedQty = decimal.NewFromInt(100)
	p.clientIDStatusMap.Store(cid, &clientIDStatus{order: order})
	p.pendingCancels.Store(30, &cancelEntry{at: time.Now().Add(-2 * time.Minute), missCount: 10})
	return p
}

// A cancelled order that never shows up in the cancelled list gets its real
// status, including a partial fill, instead of an invented zero-fill one.
func TestGetPendingCancelUsesOrderStatus(t *testing.T) {
	g := newOrdersGateway()
	g.statuses[30] = statusBody(30, "Cancelled", "40.0", "41.08")
	cid := uuid.New()
	p := pendingCancelProduct(t, g, cid)

	detail, err := p.Get(context.Background(), "30")
	if err != nil {
		t.Fatal(err)
	}
	if detail.ClientID() != cid || detail.OrderStatus() != "Cancelled" {
		t.Errorf("order = client id %s status %q, want %s Cancelled", detail.ClientID(), detail.OrderStatus(), cid)
	}
	if want := decimal.NewFromInt(40); !detail.ExecutedSize().Equal(want) {
		t.Errorf("executed size = %s, want %s (partial fill)", detail.ExecutedSize(), want)
	}
	if _, ok := p.pendingCancels.Load(30); ok {
		t.Error("pending cancel entry is still present")
	}
}

// An order still working a minute after its cancel was not cancelled; it
// must not be reported cancelled, and the cancel is sent again.
func TestGetPendingCancelResendsCancel(t *testing.T) {
	g := newOrdersGateway()
	g.statuses[30] = statusBody(30, "Submitted", "0.0", "")
	p := pendingCancelProduct(t, g, uuid.New())

	detail, err := p.Get(context.Background(), "30")
	if err != nil {
		t.Fatal(err)
	}
	if detail.IsDone() {
		t.Errorf("order status = %q, want not done", detail.OrderStatus())
	}
	if want := []int64{30}; !slices.Equal(g.cancelledIDs, want) {
		t.Errorf("cancel requests = %v, want %v", g.cancelledIDs, want)
	}
	entry, ok := p.pendingCancels.Load(30)
	if !ok || entry.missCount != 0 || time.Since(entry.at) > time.Minute {
		t.Errorf("pending cancel entry = %+v, %v; want restarted", entry, ok)
	}
}
