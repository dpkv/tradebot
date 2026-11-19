// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"strings"
	"testing"
)

func TestPlaceOrderCheck(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantErr string // substring; empty means no error
	}{
		// Equity orders, as before.
		{[]string{"--symbol", "AAPL", "--side", "BUY", "--qty", "10", "--limit-price", "180"}, ""},
		{[]string{"--symbol", "AAPL", "--side", "sell", "--qty", "10", "--limit-price", "180", "--order-term", "GOOD_FOR_DAY", "--market-session", "EXTENDED"}, ""},
		{[]string{"--symbol", "AAPL", "--side", "SELL_OPEN", "--qty", "10", "--limit-price", "180"}, "is for option orders"},

		// Option orders.
		{[]string{"--contract", "AAPL261218P00200000", "--side", "SELL_OPEN", "--qty", "1", "--limit-price", "1.25"}, ""},
		{[]string{"--contract", "AAPL261218P00200000", "--side", "buy_close", "--qty", "1", "--limit-price", "1.25", "--preview"}, ""},
		{[]string{"--contract", "AAPL261218P00200000", "--side", "SELL", "--qty", "1", "--limit-price", "1.25"}, "must be one of"},
		{[]string{"--contract", "AAPL261218P0020000", "--side", "SELL_OPEN", "--qty", "1", "--limit-price", "1.25"}, "invalid --contract"},
		{[]string{"--contract", "AAPL261218P00200000", "--side", "SELL_OPEN", "--qty", "1", "--limit-price", "1.25", "--order-term", "GOOD_FOR_DAY"}, "--order-term cannot be used"},
		{[]string{"--contract", "AAPL261218P00200000", "--side", "SELL_OPEN", "--qty", "1", "--limit-price", "1.25", "--market-session", "REGULAR"}, "--market-session cannot be used"},

		// Picking the kind of order, and required flags.
		{[]string{"--side", "BUY", "--qty", "1", "--limit-price", "1"}, "is required"},
		{[]string{"--symbol", "AAPL", "--contract", "AAPL261218P00200000", "--side", "BUY", "--qty", "1", "--limit-price", "1"}, "cannot be used together"},
		{[]string{"--symbol", "AAPL", "--qty", "1", "--limit-price", "1"}, "--side flag is required"},
		{[]string{"--symbol", "AAPL", "--side", "BUY", "--limit-price", "1"}, "--qty flag is required"},
		{[]string{"--symbol", "AAPL", "--side", "BUY", "--qty", "1"}, "--limit-price flag is required"},
	} {
		c := new(PlaceOrder)
		_, fset, _ := c.Command()
		if err := fset.Parse(tc.args); err != nil {
			t.Fatalf("%v: parse: %v", tc.args, err)
		}
		err := c.check()
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%v: err = %v, want nil", tc.args, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.wantErr)
		}
	}
}
