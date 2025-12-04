// Copyright (c) 2025 Deepak Vankadaru

package internal

import (
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// TradeUpdate wraps the stream.Trade to implement exchange.PriceUpdate
type TradeUpdate struct {
	Trade stream.Trade
}

var _ exchange.PriceUpdate = &TradeUpdate{}

func (t *TradeUpdate) PricePoint() (decimal.Decimal, gobs.RemoteTime) {
	price := decimal.NewFromFloat(t.Trade.Price)
	return price, gobs.RemoteTime{Time: t.Trade.Timestamp}
}

// QuoteUpdate wraps the stream.Quote to implement exchange.PriceUpdate
// Uses the midpoint of bid and ask as the price
type QuoteUpdate struct {
	Quote stream.Quote
}

var _ exchange.PriceUpdate = &QuoteUpdate{}

func (q *QuoteUpdate) PricePoint() (decimal.Decimal, gobs.RemoteTime) {
	// Use midpoint of bid and ask
	bidPrice := decimal.NewFromFloat(q.Quote.BidPrice)
	askPrice := decimal.NewFromFloat(q.Quote.AskPrice)
	midPrice := bidPrice.Add(askPrice).Div(decimal.NewFromInt(2))
	return midPrice, gobs.RemoteTime{Time: q.Quote.Timestamp}
}

// BarUpdate wraps the stream.Bar to implement exchange.PriceUpdate
// Uses the close price as the current price
type BarUpdate struct {
	Bar       stream.Bar
	Timestamp time.Time
}

var _ exchange.PriceUpdate = &BarUpdate{}

func (b *BarUpdate) PricePoint() (decimal.Decimal, gobs.RemoteTime) {
	price := decimal.NewFromFloat(b.Bar.Close)
	return price, gobs.RemoteTime{Time: b.Timestamp}
}
