// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bvk/tradebot/etrade/autologin"
	"github.com/shopspring/decimal"
)

const (
	testAccountID    = "11112222"
	testAccountIDKey = "keyABC"
)

// newTestClient returns a command-line mode client whose requests go to an
// httptest server running handler.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	creds := &Credentials{
		Login:             &autologin.LoginCredentials{Username: "u", Password: "p"},
		ConsumerKey:       "ck",
		ConsumerSecret:    "cs",
		AccessToken:       "at",
		AccessTokenSecret: "ats",
		Accounts: []autologin.Account{{
			AccountID:    testAccountID,
			AccountIDKey: testAccountIDKey,
			AccountName:  "test",
			Status:       "ACTIVE",
		}},
	}
	c, err := New(context.Background(), creds, CommandOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if c.testBaseURL, err = url.Parse(srv.URL); err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func chainPair(strike string, adjusted bool) map[string]any {
	side := func(typ string) map[string]any {
		return map[string]any{
			"optionCategory": "STANDARD", "optionType": typ, "symbol": "AAPL",
			"strikePrice": json.Number(strike), "bid": 1.1, "ask": 1.3,
			"adjustedFlag": adjusted, "OptionGreeks": map[string]any{"delta": 0.3, "iv": 0.25},
		}
	}
	return map[string]any{"Call": side("CALL"), "Put": side("PUT")}
}

func optionChainHandler(t *testing.T, expiryCalls, chainCalls *atomic.Int32, gate <-chan struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/market/optionexpiredate", func(w http.ResponseWriter, r *http.Request) {
		expiryCalls.Add(1)
		if gate != nil {
			<-gate
		}
		if got := r.URL.Query().Get("expiryType"); got != "ALL" {
			t.Errorf("expiryType = %q, want ALL", got)
		}
		writeJSON(w, map[string]any{"OptionExpireDateResponse": map[string]any{"ExpirationDate": []map[string]any{
			{"year": 2026, "month": 12, "day": 18, "expiryType": "MONTHLY"},
			{"year": 2026, "month": 12, "day": 24, "expiryType": "WEEKLY"},
		}}})
	})
	mux.HandleFunc("/v1/market/optionchains", func(w http.ResponseWriter, r *http.Request) {
		chainCalls.Add(1)
		q := r.URL.Query()
		for k, want := range map[string]string{"symbol": "AAPL", "chainType": "CALLPUT", "optionCategory": "STANDARD", "skipAdjusted": "true", "includeWeekly": "true"} {
			if got := q.Get(k); got != want {
				t.Errorf("optionchains %s = %q, want %q", k, got, want)
			}
		}
		if q.Has("noOfStrikes") || q.Has("strikePriceNear") {
			t.Errorf("optionchains has a strike window: %v", q)
		}
		pairs := []any{chainPair("200", false), chainPair("205", true)}
		writeJSON(w, map[string]any{"OptionChainResponse": map[string]any{
			"OptionPair": pairs,
			"SelectedED": map[string]any{"year": 2026, "month": 12, "day": json.Number(q.Get("expiryDay"))},
		}})
	})
	return mux
}

func TestGetCachedOptionChain(t *testing.T) {
	var expiryCalls, chainCalls atomic.Int32
	c := newTestClient(t, optionChainHandler(t, &expiryCalls, &chainCalls, nil))
	ctx := context.Background()

	quotes, err := c.GetCachedOptionChain(ctx, "aapl")
	if err != nil {
		t.Fatal(err)
	}
	// Two expiries, each with one unadjusted call and put.
	if len(quotes) != 4 {
		t.Fatalf("got %d quotes, want 4", len(quotes))
	}
	for _, q := range quotes {
		if q.Adjusted || !q.Strike.Equal(decimal.NewFromInt(200)) {
			t.Errorf("unexpected entry %+v", q)
		}
	}
	if q := quotes[3]; q.Year != 2026 || q.Month != 12 || q.Day != 24 || q.CallPut != "PUT" {
		t.Errorf("last entry = %+v, want the 2026-12-24 put", q)
	}

	// Served from the cache.
	if _, err := c.GetCachedOptionChain(ctx, "AAPL"); err != nil {
		t.Fatal(err)
	}
	if e, n := expiryCalls.Load(), chainCalls.Load(); e != 1 || n != 2 {
		t.Fatalf("fetches = %d expiry, %d chain; want 1 and 2", e, n)
	}

	// Refetched once the entry is older than the TTL.
	c.chainMu.Lock()
	c.chainMap["AAPL"].at = time.Now().Add(-c.opts.ChainCacheTTL)
	c.chainMu.Unlock()
	if _, err := c.GetCachedOptionChain(ctx, "AAPL"); err != nil {
		t.Fatal(err)
	}
	if e := expiryCalls.Load(); e != 2 {
		t.Fatalf("expiry fetches = %d after TTL, want 2", e)
	}
}

func TestGetCachedOptionChainSharedFetch(t *testing.T) {
	var expiryCalls, chainCalls atomic.Int32
	gate := make(chan struct{})
	c := newTestClient(t, optionChainHandler(t, &expiryCalls, &chainCalls, gate))

	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			quotes, err := c.GetCachedOptionChain(context.Background(), "AAPL")
			if err == nil && len(quotes) != 4 {
				err = fmt.Errorf("got %d quotes, want 4", len(quotes))
			}
			errs <- err
		}()
	}
	// A caller that gives up doesn't fail the shared fetch.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetCachedOptionChain(cctx, "AAPL"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled caller err = %v, want context.Canceled", err)
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if e := expiryCalls.Load(); e != 1 {
		t.Fatalf("expiry fetches = %d, want 1 shared fetch", e)
	}
}

func TestGetCachedOptionChainErrorNotCached(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/market/optionexpiredate", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	c := newTestClient(t, mux)
	for i := 0; i < 2; i++ {
		if _, err := c.GetCachedOptionChain(context.Background(), "AAPL"); err == nil {
			t.Fatal("want an error")
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2 (errors are not cached)", n)
	}
}

func TestGetOptionQuote(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/market/quote/", func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/market/quote/AAPL:2026:12:18:PUT:200"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.URL.Query().Get("detailFlag"); got != "OPTIONS" {
			t.Errorf("detailFlag = %q, want OPTIONS", got)
		}
		writeJSON(w, map[string]any{"QuoteResponse": map[string]any{"QuoteData": []any{map[string]any{
			"dateTimeUTC": 1765000000,
			"Product": map[string]any{"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
				"expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": 200},
			"Option": map[string]any{"bid": 2.0, "ask": 2.2, "lastTrade": 2.1,
				"OptionGreeks": map[string]any{"delta": -0.3, "iv": 0.28}},
		}}}})
	})
	c := newTestClient(t, mux)

	k, err := parseContractID("AAPL261218P00200000")
	if err != nil {
		t.Fatal(err)
	}
	q, err := c.GetOptionQuote(context.Background(), k.QuoteSymbol())
	if err != nil {
		t.Fatal(err)
	}
	if price, _ := q.PricePoint(); !price.Equal(decimal.RequireFromString("2.1")) {
		t.Errorf("price = %s, want 2.1", price)
	}
	if !q.Delta.Equal(decimal.RequireFromString("-0.3")) || q.CallPut != "PUT" || q.Day != 18 {
		t.Errorf("quote = %+v", q)
	}
}

func TestGetOptionQuoteNotOption(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/market/quote/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"QuoteResponse": map[string]any{"QuoteData": []any{map[string]any{
			"Product": map[string]any{"symbol": "AAPL", "securityType": "EQ"},
		}}}})
	})
	c := newTestClient(t, mux)
	if _, err := c.GetOptionQuote(context.Background(), "AAPL:2026:12:18:PUT:200"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestPlaceOptionLimitOrder(t *testing.T) {
	type order struct {
		PriceType     string          `json:"priceType"`
		OrderTerm     string          `json:"orderTerm"`
		MarketSession string          `json:"marketSession"`
		LimitPrice    decimal.Decimal `json:"limitPrice"`
		Instrument    []struct {
			Product      map[string]any  `json:"Product"`
			OrderAction  string          `json:"orderAction"`
			QuantityType string          `json:"quantityType"`
			Quantity     decimal.Decimal `json:"quantity"`
		} `json:"Instrument"`
	}
	type request struct {
		ClientOrderID string  `json:"clientOrderId"`
		OrderType     string  `json:"orderType"`
		Order         []order `json:"Order"`
	}
	check := func(step string, r *request) {
		if r.OrderType != "OPTN" || r.ClientOrderID != "123" || len(r.Order) != 1 {
			t.Fatalf("%s request = %+v", step, r)
		}
		o := r.Order[0]
		if o.PriceType != "LIMIT" || o.OrderTerm != "GOOD_FOR_DAY" || o.MarketSession != "REGULAR" ||
			!o.LimitPrice.Equal(decimal.RequireFromString("1.23")) || len(o.Instrument) != 1 {
			t.Fatalf("%s order = %+v", step, o)
		}
		in := o.Instrument[0]
		if in.OrderAction != "SELL_OPEN" || in.QuantityType != "QUANTITY" || !in.Quantity.Equal(decimal.NewFromInt(2)) {
			t.Fatalf("%s instrument = %+v", step, in)
		}
		p := in.Product
		if p["securityType"] != "OPTN" || p["symbol"] != "AAPL" || p["callPut"] != "PUT" ||
			p["expiryYear"] != float64(2026) || p["expiryMonth"] != float64(12) || p["expiryDay"] != float64(18) || p["strikePrice"] != "200" {
			t.Fatalf("%s product = %v", step, p)
		}
	}

	var placed atomic.Bool
	mux := http.NewServeMux()
	base := "/v1/accounts/" + testAccountIDKey + "/orders"
	mux.HandleFunc(base+"/preview", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PreviewOrderRequest request `json:"PreviewOrderRequest"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		check("preview", &req.PreviewOrderRequest)
		writeJSON(w, map[string]any{"PreviewOrderResponse": map[string]any{"PreviewIds": []any{map[string]any{"previewId": 77}}}})
	})
	mux.HandleFunc(base+"/place", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			PlaceOrderRequest struct {
				request
				PreviewIds []struct {
					PreviewID int64 `json:"previewId"`
				} `json:"PreviewIds"`
			} `json:"PlaceOrderRequest"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		check("place", &req.PlaceOrderRequest.request)
		if ids := req.PlaceOrderRequest.PreviewIds; len(ids) != 1 || ids[0].PreviewID != 77 {
			t.Fatalf("place preview ids = %+v", ids)
		}
		placed.Store(true)
		writeJSON(w, map[string]any{"PlaceOrderResponse": map[string]any{"OrderIds": []any{map[string]any{"orderId": 9001}}}})
	})
	c := newTestClient(t, mux)

	k, err := parseContractID("AAPL261218P00200000")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := c.PlaceOptionLimitOrder(ctx, testAccountID, k, OptionSellOpen, decimal.NewFromInt(2), decimal.RequireFromString("1.23"), "123")
	if err != nil {
		t.Fatal(err)
	}
	if id != 9001 || !placed.Load() {
		t.Fatalf("order id = %d, placed = %v", id, placed.Load())
	}

	for _, bad := range []struct {
		action     string
		qty, price string
	}{
		{"SELL", "2", "1.23"},
		{OptionSellOpen, "1.5", "1.23"},
		{OptionSellOpen, "0", "1.23"},
		{OptionSellOpen, "2", "0"},
	} {
		if _, err := c.PlaceOptionLimitOrder(ctx, testAccountID, k, bad.action, decimal.RequireFromString(bad.qty), decimal.RequireFromString(bad.price), "124"); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("%+v: err = %v, want os.ErrInvalid", bad, err)
		}
	}
}

func TestListOrders(t *testing.T) {
	var pages atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/accounts/"+testAccountIDKey+"/orders", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Has("status") {
			t.Errorf("status = %q, want every status", q.Get("status"))
		}
		for k, want := range map[string]string{"symbol": "AAPL", "securityType": "OPTN", "fromDate": "11172026", "toDate": "11182026"} {
			if got := q.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		pages.Add(1)
		apiOrder := func(id int64) map[string]any {
			return map[string]any{"orderId": id, "OrderDetail": []any{map[string]any{
				"status": "EXECUTED", "priceType": "LIMIT", "limitPrice": 1.2,
				"Instrument": []any{map[string]any{"orderAction": "SELL_OPEN", "orderedQuantity": 1,
					"Product": map[string]any{"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT"}}},
			}}}
		}
		if q.Get("marker") == "" {
			writeJSON(w, map[string]any{"OrdersResponse": map[string]any{"marker": "m1", "Order": []any{apiOrder(1)}}})
			return
		}
		if got := q.Get("marker"); got != "m1" {
			t.Errorf("marker = %q, want m1", got)
		}
		writeJSON(w, map[string]any{"OrdersResponse": map[string]any{"Order": []any{apiOrder(2)}}})
	})
	c := newTestClient(t, mux)

	from := time.Date(2026, 11, 17, 12, 0, 0, 0, newYork)
	to := time.Date(2026, 11, 18, 12, 0, 0, 0, newYork)
	orders, err := c.ListOrders(context.Background(), testAccountID, &ListOrdersFilter{Symbol: "AAPL", SecurityType: "OPTN", FromDate: from, ToDate: to})
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 2 || orders[0].OrderID != 1 || orders[1].OrderID != 2 || pages.Load() != 2 {
		t.Fatalf("got %d orders over %d pages", len(orders), pages.Load())
	}
}

func TestListTransactions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/accounts/"+testAccountIDKey+"/transactions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for k, want := range map[string]string{"startDate": "10042026", "endDate": "11182026", "count": "50"} {
			if got := q.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		tx := func(id int64, typ string) map[string]any {
			return map[string]any{"transactionId": id, "transactionDate": 1765000000000, "transactionType": typ,
				"brokerage": map[string]any{"quantity": -1, "fee": 0.65, "product": map[string]any{
					"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
					"expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": 200}}}
		}
		if q.Get("marker") == "" {
			writeJSON(w, map[string]any{"TransactionListResponse": map[string]any{
				"Transaction": []any{tx(1, "Sold Short")}, "marker": "m1", "moreTransactions": true}})
			return
		}
		writeJSON(w, map[string]any{"TransactionListResponse": map[string]any{
			"Transaction": []any{tx(2, "Option Assignment")}, "moreTransactions": false}})
	})
	c := newTestClient(t, mux)

	start := time.Date(2026, 10, 4, 0, 0, 0, 0, newYork)
	end := time.Date(2026, 11, 18, 23, 0, 0, 0, newYork)
	txs, err := c.ListTransactions(context.Background(), testAccountID, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 || txs[1].ID != "2" || txs[1].Type != "Option Assignment" || txs[1].SecurityType != "OPTN" || !txs[1].Strike.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("transactions = %+v", txs)
	}
}
