package datafeed

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bvk/tradebot/coinbase"
	"github.com/bvk/tradebot/gobs"
)

// CoinbaseDBFeed is a DataFeed that reads OHLC candle data from a Coinbase
// BadgerDB datastore and expands each candle into ticks using a CandleExpander.
type CoinbaseDBFeed struct {
	productID string
	expander  CandleExpander
	candles   []*gobs.Candle
	candleIdx int
	ticks     []Tick
	tickIdx   int
}

// NewCoinbaseDBFeed loads candles for productID between begin and end from the
// Coinbase datastore, returning a feed that emits ticks via the given expander.
func NewCoinbaseDBFeed(ctx context.Context, ds *coinbase.Datastore, productID string, begin, end time.Time, expander CandleExpander) (*CoinbaseDBFeed, error) {
	var candles []*gobs.Candle
	err := ds.ScanCandles(ctx, productID, begin, end, func(c *gobs.Candle) error {
		cp := *c
		candles = append(candles, &cp)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not scan candles for %s: %w", productID, err)
	}
	if len(candles) == 0 {
		return nil, fmt.Errorf("no candles found for %s between %s and %s", productID, begin, end)
	}
	return &CoinbaseDBFeed{
		productID: productID,
		expander:  expander,
		candles:   candles,
	}, nil
}

func (f *CoinbaseDBFeed) ProductID() string { return f.productID }

// Next returns the next tick. Returns io.EOF when all candles are exhausted.
func (f *CoinbaseDBFeed) Next(ctx context.Context) (Tick, error) {
	for {
		if f.tickIdx < len(f.ticks) {
			t := f.ticks[f.tickIdx]
			f.tickIdx++
			return t, nil
		}
		if f.candleIdx >= len(f.candles) {
			return Tick{}, io.EOF
		}
		c := f.candles[f.candleIdx]
		f.candleIdx++
		f.ticks = f.expander(c.StartTime.Time, c.Duration, c.Open, c.Low, c.High, c.Close)
		f.tickIdx = 0
	}
}

func (f *CoinbaseDBFeed) Close() error {
	f.candles = nil
	f.ticks = nil
	return nil
}
