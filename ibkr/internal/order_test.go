// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"encoding/json"
	"testing"
)

// IBKR lists a rejected order with status "Cancelled"; only
// order_ccp_status tells it apart from a real cancellation.
func TestNewOrderFromAPIRejected(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		rejected  bool
		ccpStatus string
	}{
		{
			name:      "rejected",
			json:      `{"orderId":1052773302,"order_ref":"ce9a0ba5-8afa-7694-4b22-d6ebfc330a7d","ticker":"SOXL","side":"SELL","status":"Cancelled","order_ccp_status":"Rejected"}`,
			rejected:  true,
			ccpStatus: "Rejected",
		},
		{
			name:      "cancelled",
			json:      `{"orderId":1052773528,"order_ref":"d3561642-e34f-22b5-0b76-793ad7490093","ticker":"SOXL","side":"SELL","status":"Cancelled","order_ccp_status":"Cancelled"}`,
			ccpStatus: "Cancelled",
		},
		{
			name: "no ccp status",
			json: `{"orderId":1,"order_ref":"x","ticker":"SOXL","side":"SELL","status":"Submitted"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a APIOrder
			if err := json.Unmarshal([]byte(tt.json), &a); err != nil {
				t.Fatal(err)
			}
			o := NewOrderFromAPI(&a)
			if o.CCPStatus != tt.ccpStatus {
				t.Errorf("CCPStatus = %q, want %q", o.CCPStatus, tt.ccpStatus)
			}
			if got := o.IsRejected(); got != tt.rejected {
				t.Errorf("IsRejected() = %v, want %v", got, tt.rejected)
			}
		})
	}
}

// Orders are saved as JSON, so the status must survive a save and load, and
// orders saved before the field existed must load as not rejected.
func TestOrderCCPStatusPersists(t *testing.T) {
	js, err := json.Marshal(&Order{OrderID: 1, Status: "Cancelled", CCPStatus: "Rejected"})
	if err != nil {
		t.Fatal(err)
	}
	var loaded Order
	if err := json.Unmarshal(js, &loaded); err != nil {
		t.Fatal(err)
	}
	if !loaded.IsRejected() {
		t.Errorf("loaded order IsRejected() = false, want true (json %s)", js)
	}

	var old Order
	if err := json.Unmarshal([]byte(`{"OrderID":2,"Status":"Cancelled"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.IsRejected() {
		t.Error("order saved without CCPStatus IsRejected() = true, want false")
	}
}
