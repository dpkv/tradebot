package mockexchange

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/bvk/tradebot/datafeed"
	"github.com/shopspring/decimal"
)

// Engine drives a DataFeed into a Product by calling ProcessTick for each tick.
// It is the only component that knows about time progression during backtesting.
type Engine struct {
	feed    datafeed.DataFeed
	product *Product

	// MaxTickProgress caps the price change per tick. When a feed tick exceeds
	// this, the engine emits intermediate sub-ticks stepping by MaxTickProgress
	// so the strategy can react incrementally. Zero means unlimited.
	MaxTickProgress decimal.Decimal
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

	var prevTick datafeed.Tick
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
		ticks := e.expand(prevTick, tick)
		for _, t := range ticks {
			if err := e.deliverTick(ctx, t, prevTick); err != nil {
				return err
			}
			prevTick = t
		}
	}
}

// expand returns the sub-ticks to deliver for a feed tick. If MaxTickProgress
// is zero or the price change is within limit, it returns just the tick itself.
// Otherwise it returns intermediate ticks stepping by MaxTickProgress followed
// by the original tick.
func (e *Engine) expand(prev, tick datafeed.Tick) []datafeed.Tick {
	if e.MaxTickProgress.IsZero() || prev.Price.IsZero() {
		return []datafeed.Tick{tick}
	}
	diff := tick.Price.Sub(prev.Price)
	if diff.Abs().LessThanOrEqual(e.MaxTickProgress) {
		return []datafeed.Tick{tick}
	}
	step := e.MaxTickProgress
	if diff.IsNegative() {
		step = step.Neg()
	}
	var ticks []datafeed.Tick
	for price := prev.Price.Add(step); diff.IsPositive() && price.LessThan(tick.Price) ||
		diff.IsNegative() && price.GreaterThan(tick.Price); price = price.Add(step) {
		ticks = append(ticks, datafeed.Tick{Time: tick.Time, Price: price})
	}
	ticks = append(ticks, tick)
	slog.Debug("engine: expand", "prev_price", prev.Price, "curr_price", tick.Price, "diff", diff, "sub_ticks", len(ticks))
	return ticks
}

func (e *Engine) deliverTick(ctx context.Context, tick, prevTick datafeed.Tick) error {
	fills := e.product.ProcessTick(tick)
	slog.Debug("engine: tick", "prev_price", prevTick.Price, "curr_price", tick.Price, "time", tick.Time, "fills", fills)
	if fills == 0 {
		return nil
	}
	return e.product.waitForStableOrders(ctx)
}
