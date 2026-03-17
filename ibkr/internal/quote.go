// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// APISnapshot is the per-instrument JSON structure returned by
// GET /v1/api/iserver/marketdata/snapshot?conids={conid}&fields=31,84,86.
//
// IBKR encodes market data fields as string-keyed JSON properties using
// numeric field IDs. The relevant fields are:
//
//	31  - last traded price
//	84  - bid price
//	86  - ask price
//	7295 - open price (unused here, available if needed)
//
// All price fields are strings in the response and may be absent if the
// market data subscription is not yet ready (IBKR streams data lazily after
// the first snapshot request).
type APISnapshot struct {
	ConID    int    `json:"conid"`
	Updated  int64  `json:"_updated"` // milliseconds since epoch
	LastStr  string `json:"31"`
	BidStr   string `json:"84"`
	AskStr   string `json:"86"`
}

// Quote is a flat price update derived from an IBKR market data snapshot. It
// implements exchange.PriceUpdate.
//
// PricePoint returns the mid-price (bid+ask)/2 when both sides are available,
// falling back to the last traded price if bid/ask are absent.
type Quote struct {
	Bid     decimal.Decimal
	Ask     decimal.Decimal
	Last    decimal.Decimal
	Updated int64 // milliseconds since epoch
}

var _ exchange.PriceUpdate = &Quote{}

var d2 = decimal.NewFromInt(2)

func (q *Quote) PricePoint() (decimal.Decimal, gobs.RemoteTime) {
	t := gobs.RemoteTime{Time: time.UnixMilli(q.Updated)}
	if !q.Bid.IsZero() && !q.Ask.IsZero() {
		return q.Bid.Add(q.Ask).Div(d2), t
	}
	return q.Last, t
}

// NewQuoteFromAPI converts an APISnapshot into a Quote. Returns nil if no
// usable price data is present (e.g. snapshot not yet populated by gateway).
func NewQuoteFromAPI(a *APISnapshot) *Quote {
	bid, _ := decimal.NewFromString(a.BidStr)
	ask, _ := decimal.NewFromString(a.AskStr)
	last, _ := decimal.NewFromString(a.LastStr)

	if bid.IsZero() && ask.IsZero() && last.IsZero() {
		return nil
	}
	return &Quote{
		Bid:     bid,
		Ask:     ask,
		Last:    last,
		Updated: a.Updated,
	}
}
