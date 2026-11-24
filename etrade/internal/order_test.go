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

func TestNewOrderLegsFromAPI(t *testing.T) {
	var a APIOrder
	if err := json.Unmarshal([]byte(assignmentOrder), &a); err != nil {
		t.Fatal(err)
	}

	if o := NewOrderFromAPI("acct", &a); o != nil {
		t.Fatalf("NewOrderFromAPI = %+v, want nil for a multi-instrument order", o)
	}

	o := NewOrderLegsFromAPI("acct", &a)
	if o == nil {
		t.Fatal("NewOrderLegsFromAPI = nil")
	}
	if o.OrderID != 743 || o.Status != "EXECUTED" || o.AccountID != "acct" {
		t.Errorf("order = %+v", o)
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

func TestNewOrderLegsFromAPISingleInstrument(t *testing.T) {
	var a APIOrder
	if err := json.Unmarshal([]byte(assignmentOrder), &a); err != nil {
		t.Fatal(err)
	}
	a.OrderDetail[0].Instrument = a.OrderDetail[0].Instrument[1:]

	if o := NewOrderLegsFromAPI("acct", &a); o != nil {
		t.Errorf("NewOrderLegsFromAPI = %+v, want nil for a single-instrument order", o)
	}
	o := NewOrderFromAPI("acct", &a)
	if o == nil || o.Symbol != "NVDA" || o.Side != "BUY" || len(o.Legs) != 0 {
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
