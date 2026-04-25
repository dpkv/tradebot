package mockexchange

import (
	"context"
	"errors"
	"io"

	"github.com/bvk/tradebot/datafeed"
)

// Engine drives a DataFeed into a Product by calling ProcessTick for each tick.
// It is the only component that knows about time progression during backtesting.
type Engine struct {
	feed    datafeed.DataFeed
	product *Product
}

// NewEngine creates an Engine that feeds ticks from feed into product.
func NewEngine(feed datafeed.DataFeed, product *Product) *Engine {
	return &Engine{feed: feed, product: product}
}

// Run waits for the strategy to subscribe, then loops until the feed is
// exhausted or ctx is cancelled. After each tick with fills, it waits until
// the strategy has placed reactive orders before processing the next tick.
// Returns nil when the feed reaches EOF (normal end of backtest).
func (e *Engine) Run(ctx context.Context) error {
	if err := e.product.WaitForSubscriber(ctx); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tick, err := e.feed.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if fills := e.product.ProcessTick(tick); fills > 0 {
			if err := e.product.waitForStableOrders(ctx); err != nil {
				return err
			}
		}
	}
}
