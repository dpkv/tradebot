// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// assignmentOrder is shaped like the order E*TRADE creates for a put
// assignment: one detail with an option leg and a stock leg.
const assignmentOrder = `{
  "orderId": 743,
  "orderType": "OPTN",
  "OrderDetail": [{
    "placedTime": 1795410000000,
    "executedTime": 1795410000000,
    "status": "EXECUTED",
    "priceType": "MARKET",
    "Instrument": [
      {"Product": {"symbol": "NVDA", "securityType": "OPTN", "callPut": "PUT",
                   "expiryYear": 2026, "expiryMonth": 11, "expiryDay": 20, "strikePrice": 167.5},
       "orderAction": "BUY_CLOSE", "orderedQuantity": 1, "filledQuantity": 1},
      {"Product": {"symbol": "NVDA", "securityType": "EQ"},
       "orderAction": "BUY", "orderedQuantity": 100, "filledQuantity": 100,
       "averageExecutionPrice": 167.5, "estimatedFees": 0.25}
    ]
  }]
}`

func TestNewOrderFromAPIMultiInstrument(t *testing.T) {
	var a APIOrder
	if err := json.Unmarshal([]byte(assignmentOrder), &a); err != nil {
		t.Fatal(err)
	}

	o := NewOrderFromAPI("acct", &a)
	if !o.HasLegs() {
		t.Fatalf("HasLegs = false for a multi-instrument order: %+v", o)
	}
	if o.OrderID != 743 || o.Status != "EXECUTED" || o.AccountID != "acct" {
		t.Errorf("order = %+v", o)
	}
	for _, leg := range o.Legs {
		if leg.Status != "EXECUTED" || leg.PriceType != "MARKET" {
			t.Errorf("leg detail fields = %+v, want the order's", leg)
		}
	}
	if o.Symbol != "" || !o.FilledQty.IsZero() {
		t.Errorf("per-instrument fields set on a multi-instrument order: %+v", o)
	}
	if !o.Commission.Equal(decimal.RequireFromString("0.25")) {
		t.Errorf("Commission = %s, want 0.25", o.Commission)
	}
	if len(o.Legs) != 2 {
		t.Fatalf("len(Legs) = %d, want 2", len(o.Legs))
	}
	put, stock := o.Legs[0], o.Legs[1]
	if put.SecurityType != "OPTN" || put.CallPut != "PUT" || put.Side != "BUY_CLOSE" || !put.StrikePrice.Equal(decimal.RequireFromString("167.5")) {
		t.Errorf("option leg = %+v", put)
	}
	if stock.SecurityType != "EQ" || stock.Side != "BUY" || !stock.FilledQty.Equal(decimal.NewFromInt(100)) || !stock.AvgFillPrice.Equal(decimal.RequireFromString("167.5")) {
		t.Errorf("stock leg = %+v", stock)
	}
}

func TestNewOrderFromAPISingleInstrument(t *testing.T) {
	var a APIOrder
	if err := json.Unmarshal([]byte(assignmentOrder), &a); err != nil {
		t.Fatal(err)
	}
	a.OrderDetail[0].Instrument = a.OrderDetail[0].Instrument[1:]

	o := NewOrderFromAPI("acct", &a)
	if o.HasLegs() || o.Symbol != "NVDA" || o.Side != "BUY" || !o.FilledQty.Equal(decimal.NewFromInt(100)) || !o.Commission.Equal(decimal.RequireFromString("0.25")) || o.Status != "EXECUTED" {
		t.Fatalf("NewOrderFromAPI = %+v", o)
	}
	js, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "Legs") {
		t.Errorf("single-instrument order JSON has Legs: %s", js)
	}
}

// ocaOrder is a multi-leg OCA order: two details, each with its own status
// and limit price.
const ocaOrder = `{
  "orderId": 800,
  "OrderDetail": [
    {"status": "EXECUTED", "priceType": "LIMIT", "limitPrice": 110, "executedTime": 1795410000000,
     "Instrument": [{"Product": {"symbol": "NVDA", "securityType": "EQ"},
       "orderAction": "SELL", "orderedQuantity": 10, "filledQuantity": 10,
       "averageExecutionPrice": 110, "estimatedCommission": 0.1}]},
    {"status": "CANCELLED", "priceType": "STOP", "limitPrice": 90,
     "Instrument": [{"Product": {"symbol": "NVDA", "securityType": "EQ"},
       "orderAction": "SELL", "orderedQuantity": 10}]}
  ]
}`

func TestNewOrderFromAPIOCA(t *testing.T) {
	var a APIOrder
	if err := json.Unmarshal([]byte(ocaOrder), &a); err != nil {
		t.Fatal(err)
	}

	o := NewOrderFromAPI("acct", &a)
	if !o.HasLegs() {
		t.Fatalf("HasLegs = false for an OCA order: %+v", o)
	}
	if o.OrderID != 800 || o.Status != "" || !o.LimitPrice.IsZero() {
		t.Errorf("order-level detail fields set on an OCA order: %+v", o)
	}
	if !o.Commission.Equal(decimal.RequireFromString("0.1")) {
		t.Errorf("Commission = %s, want 0.1", o.Commission)
	}
	if len(o.Legs) != 2 {
		t.Fatalf("len(Legs) = %d, want 2", len(o.Legs))
	}
	filled, cancelled := o.Legs[0], o.Legs[1]
	if filled.Status != "EXECUTED" || !filled.LimitPrice.Equal(decimal.NewFromInt(110)) || filled.ExecutedTimeMilli == 0 || !filled.FilledQty.Equal(decimal.NewFromInt(10)) {
		t.Errorf("first leg = %+v", filled)
	}
	if cancelled.Status != "CANCELLED" || cancelled.PriceType != "STOP" || !cancelled.LimitPrice.Equal(decimal.NewFromInt(90)) || !cancelled.FilledQty.IsZero() {
		t.Errorf("second leg = %+v", cancelled)
	}
}
