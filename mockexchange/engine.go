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

// Run loops until the feed is exhausted or ctx is cancelled.
// Returns nil when the feed reaches EOF (normal end of backtest).
func (e *Engine) Run(ctx context.Context) error {
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
		e.product.ProcessTick(tick)
	}
}
