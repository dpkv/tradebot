// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestParseContractID(t *testing.T) {
	tests := []struct {
		id      string
		root    string
		callPut string
		date    string
		strike  string
		quote   string
	}{
		{"AAPL261218P00200000", "AAPL", "PUT", "2026-12-18", "200", "AAPL:2026:12:18:PUT:200"},
		{"SPY270115C00512500", "SPY", "CALL", "2027-01-15", "512.5", "SPY:2027:01:15:CALL:512.5"},
		{"F261120P00012000", "F", "PUT", "2026-11-20", "12", "F:2026:11:20:PUT:12"},
		{"AAPL1261218C00000500", "AAPL1", "CALL", "2026-12-18", "0.5", "AAPL1:2026:12:18:CALL:0.5"},
		{"BRK.B261218C00450000", "BRK.B", "CALL", "2026-12-18", "450", "BRK.B:2026:12:18:CALL:450"},
	}
	for _, test := range tests {
		c, err := parseContractID(test.id)
		if err != nil {
			t.Errorf("parseContractID(%q): %v", test.id, err)
			continue
		}
		date := time.Date(c.Year, time.Month(c.Month), c.Day, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
		if c.Root != test.root || c.CallPut != test.callPut || date != test.date || !c.Strike.Equal(decimal.RequireFromString(test.strike)) {
			t.Errorf("parseContractID(%q) = %+v, want %s %s %s %s", test.id, c, test.root, test.callPut, test.date, test.strike)
		}
		if got := c.ID(); got != test.id {
			t.Errorf("parseContractID(%q).ID() = %q", test.id, got)
		}
		if got := c.QuoteSymbol(); got != test.quote {
			t.Errorf("parseContractID(%q).QuoteSymbol() = %q, want %q", test.id, got, test.quote)
		}
	}
}

func TestParseContractIDInvalid(t *testing.T) {
	for _, id := range []string{
		"",
		"261218P00200000",         // no root
		"AAPL  261218P00200000",   // OSI space padding
		"aapl261218P00200000",     // lower-case root
		"AAPL_20261218_P00200000", // old underscore form
		"AAPL261218X00200000",     // neither call nor put
		"AAPL261318P00200000",     // month 13
		"AAPL260230P00200000",     // February 30
		"AAPL261218P00000000",     // zero strike
		"AAPL261218P0020000A",     // non-digit strike
	} {
		if _, err := parseContractID(id); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("parseContractID(%q) = %v, want os.ErrInvalid", id, err)
		}
	}
}

func TestNewContract(t *testing.T) {
	c, err := newContract("aapl", "put", 2026, 12, 18, decimal.RequireFromString("200.000000"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ID(); got != "AAPL261218P00200000" {
		t.Errorf("ID() = %q", got)
	}
	for _, strike := range []string{"0", "-5", "200.0005", "100000"} {
		if _, err := newContract("AAPL", "PUT", 2026, 12, 18, decimal.RequireFromString(strike)); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("newContract with strike %s = %v, want os.ErrInvalid", strike, err)
		}
	}
	if _, err := newContract("AAPL", "STRADDLE", 2026, 12, 18, decimal.NewFromInt(200)); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("newContract with option type STRADDLE = %v, want os.ErrInvalid", err)
	}
}

func TestContractExpiry(t *testing.T) {
	c, err := parseContractID("AAPL261218P00200000")
	if err != nil {
		t.Fatal(err)
	}
	got := c.Expiry()
	want := time.Date(2026, 12, 19, 4, 59, 0, 0, time.UTC) // 23:59 EST is 04:59 UTC the next day
	if !got.Equal(want) {
		t.Errorf("Expiry() = %v, want %v", got, want)
	}
	// A 0DTE contract after the close is still live.
	afterClose := time.Date(2026, 12, 18, 17, 0, 0, 0, newYork)
	if !got.After(afterClose) {
		t.Errorf("Expiry() %v is not after the expiry-day close %v", got, afterClose)
	}
}
