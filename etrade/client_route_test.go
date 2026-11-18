// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bvk/tradebot/etrade/internal"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

func TestNewContractEmptyCallPut(t *testing.T) {
	// ID reads the first letter of CallPut; an empty one must be an error,
	// not a panic.
	if _, err := newContract("AAPL", "", 2026, 12, 18, decimal.NewFromInt(200)); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("err = %v, want os.ErrInvalid", err)
	}
}

func TestOrderTopicKey(t *testing.T) {
	equity := &internal.Order{Symbol: "AAPL", SecurityType: "EQ"}
	option := &internal.Order{Symbol: "AAPL", SecurityType: "OPTN", CallPut: "PUT",
		ExpiryYear: 2026, ExpiryMonth: 12, ExpiryDay: 18, StrikePrice: decimal.NewFromInt(200)}
	bad := &internal.Order{Symbol: "AAPL", SecurityType: "OPTN"}

	if key, err := orderTopicKey(equity); err != nil || key != "AAPL" {
		t.Errorf("equity key = %q, %v; want AAPL", key, err)
	}
	if key, err := orderTopicKey(option); err != nil || key != "AAPL261218P00200000" {
		t.Errorf("option key = %q, %v; want AAPL261218P00200000", key, err)
	}
	if _, err := orderTopicKey(bad); err == nil {
		t.Error("option order without contract fields: want an error")
	}
}

func TestPublishOrderRouting(t *testing.T) {
	c := newTestClient(t, http.NotFoundHandler())

	recv := func(key string) *topic.Receiver[*internal.Order] {
		r, err := topic.Subscribe(c.getSymbolOrdersTopic(key), 0, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		return r
	}
	equityRecv, optionRecv := recv("AAPL"), recv("AAPL261218P00200000")

	c.publishOrder(&internal.Order{OrderID: 1, Symbol: "AAPL", SecurityType: "OPTN", CallPut: "PUT",
		ExpiryYear: 2026, ExpiryMonth: 12, ExpiryDay: 18, StrikePrice: decimal.NewFromInt(200)})
	c.publishOrder(&internal.Order{OrderID: 2, Symbol: "AAPL", SecurityType: "EQ"})

	if o, err := optionRecv.Receive(); err != nil || o.OrderID != 1 {
		t.Fatalf("option topic got %v, %v; want order 1", o, err)
	}
	if o, err := equityRecv.Receive(); err != nil || o.OrderID != 2 {
		t.Fatalf("equity topic got %v, %v; want order 2 (the option order must not reach it)", o, err)
	}
}

func TestMatchOpenOrderSkipsOptions(t *testing.T) {
	p := orderPlacementInfo{RequestTimeMilli: 1000, Side: "SELL", Price: decimal.NewFromInt(5),
		Qty: decimal.NewFromInt(1), PriceType: "LIMIT", OrderTerm: "GOOD_FOR_DAY"}
	o := &internal.Order{AccountID: "a", Symbol: "AAPL", SecurityType: "OPTN", Side: "SELL",
		PriceType: "LIMIT", OrderTerm: "GOOD_FOR_DAY", LimitPrice: p.Price, OrderedQty: p.Qty, PlacedTimeMilli: 1000}
	if got := matchOpenOrder([]*internal.Order{o}, "a", "AAPL", p); got != nil {
		t.Fatalf("matched an option order for an equity placement")
	}
	o.SecurityType = "EQ"
	if got := matchOpenOrder([]*internal.Order{o}, "a", "AAPL", p); got != o {
		t.Fatalf("did not match the equity order")
	}
}

func TestPollOptionPrices(t *testing.T) {
	var quoteCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/market/quote/", func(w http.ResponseWriter, r *http.Request) {
		quoteCalls.Add(1)
		if got := r.URL.Query().Get("detailFlag"); got != "OPTIONS" {
			t.Errorf("detailFlag = %q, want OPTIONS", got)
		}
		var data []any
		for _, sym := range strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/market/quote/"), ",") {
			k := strings.Split(sym, ":") // AAPL:2026:12:18:PUT:200
			data = append(data, map[string]any{
				"Product": map[string]any{"symbol": k[0], "securityType": "OPTN", "callPut": k[4],
					"expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": k[5]},
				"Option": map[string]any{"bid": 1.0, "ask": 1.2},
			})
		}
		writeJSON(w, map[string]any{"QuoteResponse": map[string]any{"QuoteData": data}})
	})
	c := newTestClient(t, mux)
	ctx := context.Background()

	// No watched contracts, no request.
	c.pollOptionPrices(ctx)
	if n := quoteCalls.Load(); n != 0 {
		t.Fatalf("quote calls = %d with nothing watched, want 0", n)
	}

	const id = "AAPL261218P00200000"
	if _, err := c.WatchOption(ctx, id); err != nil {
		t.Fatal(err)
	}
	r, err := topic.Subscribe(c.getOptionPriceTopic(id), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	c.pollOptionPrices(ctx)
	q, err := r.Receive()
	if err != nil {
		t.Fatal(err)
	}
	if price, _ := q.PricePoint(); !price.Equal(decimal.RequireFromString("1.1")) {
		t.Fatalf("price = %s, want 1.1", price)
	}
	// Option quotes never reach the underlying's equity price topic.
	if tp := c.getSymbolPriceTopic("AAPL"); tp != nil {
		t.Fatal("option poll created an AAPL equity price topic")
	}

	c.UnwatchOption(id)
	before := quoteCalls.Load()
	c.pollOptionPrices(ctx)
	if n := quoteCalls.Load(); n != before {
		t.Fatalf("quote calls after unwatch = %d, want %d", n, before)
	}
}

func TestGetOptionQuotesBatches(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/market/quote/", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if n := len(strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/market/quote/"), ",")); n > maxQuoteSymbols {
			t.Errorf("request has %d symbols, want at most %d", n, maxQuoteSymbols)
		}
		writeJSON(w, map[string]any{"QuoteResponse": map[string]any{}})
	})
	c := newTestClient(t, mux)

	symbols := make([]string, maxQuoteSymbols+1)
	for i := range symbols {
		symbols[i] = "AAPL:2026:12:18:PUT:" + decimal.NewFromInt(int64(100+i)).String()
	}
	if _, err := c.GetOptionQuotes(context.Background(), symbols); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("quote calls = %d, want 2", n)
	}
}

func TestGetPortfolioSkipsOptionLots(t *testing.T) {
	base := "/v1/accounts/" + testAccountIDKey + "/portfolio"
	var optionLotsFetched atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"PortfolioResponse": map[string]any{"AccountPortfolio": []any{map[string]any{
			"accountId": testAccountID,
			"Position": []any{
				map[string]any{"positionId": 1, "quantity": 100, "positionType": "LONG", "lotsDetails": "http://x" + base + "/lots/1",
					"Product": map[string]any{"symbol": "AAPL", "securityType": "EQ"}},
				map[string]any{"positionId": 2, "quantity": -1, "positionType": "SHORT", "lotsDetails": "http://x" + base + "/lots/2",
					"Product": map[string]any{"symbol": "AAPL", "securityType": "OPTN"}},
			},
		}}}})
	})
	mux.HandleFunc(base+"/lots/1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"PositionLotsResponse": map[string]any{"PositionLot": []any{
			map[string]any{"positionLotId": 11, "orderNo": 5, "remainingQty": 100}}}})
	})
	mux.HandleFunc(base+"/lots/2", func(w http.ResponseWriter, r *http.Request) {
		optionLotsFetched.Store(true)
		writeJSON(w, map[string]any{"PositionLotsResponse": map[string]any{"PositionLot": []any{
			map[string]any{"positionLotId": 22, "orderNo": 6, "remainingQty": 1}}}})
	})
	c := newTestClient(t, mux)

	lots, err := c.GetPortfolio(context.Background(), testAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lots) != 1 || lots[0].PositionLotID != 11 || lots[0].Symbol != "AAPL" {
		t.Fatalf("lots = %+v, want only the stock lot", lots)
	}
	if optionLotsFetched.Load() {
		t.Fatal("fetched lots for an option position")
	}
}
