package datafeed

import (
	"context"
	"io"
	"time"

	"github.com/shopspring/decimal"
)

// Tick represents a single price observation at a point in time.
type Tick struct {
	Time  time.Time
	Price decimal.Decimal
}

// DataFeed emits sequential price ticks for a single trading product.
// Implementations may source data from a database, file, or live stream.
// Next returns io.EOF when the feed is exhausted.
type DataFeed interface {
	ProductID() string
	Next(ctx context.Context) (Tick, error)
	io.Closer
}

// CandleExpander converts a single OHLC candle into an ordered sequence of ticks.
// Time progresses evenly across the candle duration for each tick.
// Candle-based feed implementations use this to control intra-candle price path assumptions.
type CandleExpander func(t time.Time, d time.Duration, open, low, high, close decimal.Decimal) []Tick

// OpenLowHighClose expands a candle as open → low → high → close (bullish path assumption).
func OpenLowHighClose(t time.Time, d time.Duration, open, low, high, close decimal.Decimal) []Tick {
	step := d / 3
	return []Tick{
		{Time: t, Price: open},
		{Time: t.Add(step), Price: low},
		{Time: t.Add(2 * step), Price: high},
		{Time: t.Add(d), Price: close},
	}
}

// OpenHighLowClose expands a candle as open → high → low → close (bearish path assumption).
func OpenHighLowClose(t time.Time, d time.Duration, open, low, high, close decimal.Decimal) []Tick {
	step := d / 3
	return []Tick{
		{Time: t, Price: open},
		{Time: t.Add(step), Price: high},
		{Time: t.Add(2 * step), Price: low},
		{Time: t.Add(d), Price: close},
	}
}
