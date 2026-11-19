// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"
)

// previewTestMux answers order previews with a fixed preview response and
// fails the test on any place request. It reports the last previewed
// request's orderType and first order detail.
func previewTestMux(t *testing.T, previews *atomic.Int32, gotType *string, gotOrder *placeOrderDetail) *http.ServeMux {
	mux := http.NewServeMux()
	base := "/v1/accounts/" + testAccountIDKey + "/orders"
	mux.HandleFunc(base+"/preview", func(w http.ResponseWriter, r *http.Request) {
		var req previewOrderRequestWrapper
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		if len(req.PreviewOrderRequest.Order) == 1 {
			*gotType = req.PreviewOrderRequest.OrderType
			*gotOrder = req.PreviewOrderRequest.Order[0]
		}
		previews.Add(1)
		writeJSON(w, map[string]any{"PreviewOrderResponse": map[string]any{
			"PreviewIds":      []any{map[string]any{"previewId": 77}},
			"totalOrderValue": 245.5,
			"totalCommission": "1.5",
			"Order": []any{map[string]any{
				"estimatedCommission":  1.5,
				"estimatedTotalAmount": 245.5,
				"messages": map[string]any{"Message": []any{
					map[string]any{"description": "Not enough buying power", "code": 1042, "type": "WARNING"},
					map[string]any{"description": "Text code", "code": "X1", "type": "INFO"},
				}},
			}},
		}})
	})
	mux.HandleFunc(base+"/place", func(w http.ResponseWriter, r *http.Request) {
		t.Error("preview must not place an order")
		http.Error(w, "unexpected", http.StatusBadRequest)
	})
	return mux
}

func checkPreview(t *testing.T, p *OrderPreview) {
	t.Helper()
	want := []OrderMessage{
		{Type: "WARNING", Code: "1042", Description: "Not enough buying power"},
		{Type: "INFO", Code: "X1", Description: "Text code"},
	}
	if p.PreviewID != 77 || !p.TotalOrderValue.Equal(decimal.RequireFromString("245.5")) ||
		!p.TotalCommission.Equal(decimal.RequireFromString("1.5")) || len(p.Messages) != len(want) {
		t.Fatalf("preview = %+v", p)
	}
	for i := range want {
		if p.Messages[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, p.Messages[i], want[i])
		}
	}
}

func TestPreviewOptionOrder(t *testing.T) {
	var previews atomic.Int32
	var gotType string
	var got placeOrderDetail
	c := newTestClient(t, previewTestMux(t, &previews, &gotType, &got))
	ctx := context.Background()

	p, err := c.PreviewOptionOrder(ctx, testAccountID, "AAPL261218P00200000", OptionSellOpen, decimal.NewFromInt(2), decimal.RequireFromString("1.23"), "123")
	if err != nil {
		t.Fatal(err)
	}
	checkPreview(t, p)
	if gotType != "OPTN" || got.OrderTerm != "GOOD_FOR_DAY" || got.MarketSession != "REGULAR" || len(got.Instrument) != 1 {
		t.Fatalf("previewed %s order = %+v", gotType, got)
	}
	in := got.Instrument[0]
	if in.OrderAction != OptionSellOpen || in.Product.Symbol != "AAPL" || in.Product.CallPut != "PUT" ||
		in.Product.ExpiryYear != 2026 || in.Product.ExpiryMonth != 12 || in.Product.ExpiryDay != 18 ||
		in.Product.StrikePrice == nil || !in.Product.StrikePrice.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("previewed instrument = %+v", in)
	}

	// Bad contract IDs and order fields fail before any request.
	previews.Store(0)
	for _, bad := range []struct {
		id, action, qty string
	}{
		{"AAPL", OptionSellOpen, "1"},
		{"AAPL261218P0020000", OptionSellOpen, "1"},
		{"AAPL261218P00200000", "SELL", "1"},
		{"AAPL261218P00200000", OptionSellOpen, "1.5"},
	} {
		if _, err := c.PreviewOptionOrder(ctx, testAccountID, bad.id, bad.action, decimal.RequireFromString(bad.qty), decimal.NewFromInt(1), "124"); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("%+v: err = %v, want os.ErrInvalid", bad, err)
		}
	}
	if n := previews.Load(); n != 0 {
		t.Errorf("%d previews sent for invalid orders", n)
	}
}

func TestPlaceOptionOrderBadContract(t *testing.T) {
	var requests atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	for _, id := range []string{"", "AAPL", "AAPL 261218P00200000", "aapl261218P00200000"} {
		if _, err := c.PlaceOptionOrder(context.Background(), testAccountID, id, OptionBuyOpen, decimal.NewFromInt(1), decimal.NewFromInt(1), "1"); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("%q: err = %v, want os.ErrInvalid", id, err)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests sent for invalid contract ids", n)
	}
}

func TestPlaceOptionOrder(t *testing.T) {
	var got placeOrderDetail
	mux := http.NewServeMux()
	base := "/v1/accounts/" + testAccountIDKey + "/orders"
	mux.HandleFunc(base+"/preview", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"PreviewOrderResponse": map[string]any{"PreviewIds": []any{map[string]any{"previewId": 77}}}})
	})
	mux.HandleFunc(base+"/place", func(w http.ResponseWriter, r *http.Request) {
		var req placeOrderRequestWrapper
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		if len(req.PlaceOrderRequest.Order) == 1 {
			got = req.PlaceOrderRequest.Order[0]
		}
		writeJSON(w, map[string]any{"PlaceOrderResponse": map[string]any{"OrderIds": []any{map[string]any{"orderId": 9001}}}})
	})
	c := newTestClient(t, mux)

	id, err := c.PlaceOptionOrder(context.Background(), testAccountID, "AAPL261218C00200000", OptionBuyClose, decimal.NewFromInt(1), decimal.RequireFromString("0.05"), "123")
	if err != nil {
		t.Fatal(err)
	}
	if id != 9001 || len(got.Instrument) != 1 || got.Instrument[0].OrderAction != OptionBuyClose || got.Instrument[0].Product.CallPut != "CALL" {
		t.Fatalf("order id = %d, placed = %+v", id, got)
	}
}

func TestPreviewLimitOrder(t *testing.T) {
	var previews atomic.Int32
	var gotType string
	var got placeOrderDetail
	c := newTestClient(t, previewTestMux(t, &previews, &gotType, &got))

	p, err := c.PreviewLimitOrder(context.Background(), testAccountID, "AAPL", "buy", decimal.NewFromInt(10), decimal.RequireFromString("180"), "123", "GOOD_UNTIL_CANCEL", "REGULAR")
	if err != nil {
		t.Fatal(err)
	}
	checkPreview(t, p)
	if gotType != "EQ" || got.OrderTerm != "GOOD_UNTIL_CANCEL" || got.MarketSession != "REGULAR" ||
		len(got.Instrument) != 1 || got.Instrument[0].OrderAction != "BUY" || got.Instrument[0].Product.Symbol != "AAPL" ||
		got.Instrument[0].Product.SecurityType != "EQ" {
		t.Fatalf("previewed %s order = %+v", gotType, got)
	}
}
