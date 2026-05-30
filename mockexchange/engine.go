package mockexchange

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/bvk/tradebot/datafeed"
	"github.com/bvk/tradebot/trader"
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

	// Syncer is notified on every tick so the strategy can synchronize with
	// tick delivery. Nil means no synchronization (production mode).
	Syncer *trader.Syncer

	// Budget is the strategy's committed capital. Set before calling Run.
	// The engine uses it to isolate strategy NLV = total NLV - idle cash,
	// where idle cash = initial total NLV - Budget.
	Budget decimal.Decimal

	initialNLV  decimal.Decimal // captured on first tick
	peakNLV     decimal.Decimal // global peak total NLV (for display)
	minNLV      decimal.Decimal // global min total NLV (for display)

	peakStratNLV     decimal.Decimal // rolling peak of strategy NLV
	peakStratNLVTime time.Time

	maxDrawdownPct        decimal.Decimal
	maxDrawdownPctEpisode DrawdownEpisode

	maxDrawdownAbs        decimal.Decimal
	maxDrawdownAbsEpisode DrawdownEpisode
}

// DrawdownEpisode records the peak and trough that defined a drawdown event.
type DrawdownEpisode struct {
	PeakTime   time.Time
	PeakNLV    decimal.Decimal
	TroughTime time.Time
	TroughNLV  decimal.Decimal
}

// MaxDrawdownPct returns the maximum peak-to-trough decline as a percentage of
// the peak NLV, and the episode that produced it.
func (e *Engine) MaxDrawdownPct() (decimal.Decimal, DrawdownEpisode) {
	return e.maxDrawdownPct, e.maxDrawdownPctEpisode
}

// MaxDrawdownAbs returns the largest peak-to-trough dollar drop and the episode
// that produced it, tracked independently from MaxDrawdownPct.
func (e *Engine) MaxDrawdownAbs() (decimal.Decimal, DrawdownEpisode) {
	return e.maxDrawdownAbs, e.maxDrawdownAbsEpisode
}

// MinNLV returns the lowest NLV observed across all delivered ticks.
func (e *Engine) MinNLV() decimal.Decimal {
	return e.minNLV
}

// PeakNLV returns the highest NLV observed across all delivered ticks.
func (e *Engine) PeakNLV() decimal.Decimal {
	return e.peakNLV
}

// NewEngine creates an Engine that feeds ticks from feed into product.
func NewEngine(feed datafeed.DataFeed, product *Product) *Engine {
	return &Engine{feed: feed, product: product}
}

// Run waits for the strategy to subscribe, then loops until the feed is
// exhausted or ctx is cancelled. After each tick, notifies the Syncer and
// waits for the full strategy hierarchy to acknowledge before advancing.
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
	// Collect intermediate prices first so we know the total count for time interpolation.
	var prices []decimal.Decimal
	for price := prev.Price.Add(step); diff.IsPositive() && price.LessThan(tick.Price) ||
		diff.IsNegative() && price.GreaterThan(tick.Price); price = price.Add(step) {
		prices = append(prices, price)
	}
	n := len(prices) + 1 // +1 for the final tick
	duration := tick.Time.Sub(prev.Time)
	var ticks []datafeed.Tick
	for i, price := range prices {
		t := prev.Time.Add(duration * time.Duration(i+1) / time.Duration(n))
		ticks = append(ticks, datafeed.Tick{Time: t, Price: price})
	}
	ticks = append(ticks, tick)
	slog.Debug("engine: expand", "prev_price", prev.Price, "curr_price", tick.Price, "diff", diff, "sub_ticks", len(ticks))
	return ticks
}

func (e *Engine) deliverTick(ctx context.Context, tick, prevTick datafeed.Tick) error {
	slog.Debug("engine: tick begin", "time", tick.Time, "price", tick.Price, "prev_price", prevTick.Price)
	fills := e.product.ProcessTick(tick)
	slog.Debug("engine: tick fills", "time", tick.Time, "price", tick.Price, "prev_price", prevTick.Price, "fills", len(fills))
	if e.Syncer == nil {
		slog.Debug("engine: tick end", "time", tick.Time, "price", tick.Price, "prev_price", prevTick.Price)
		return nil
	}
	slog.Debug("engine: notifying syncer", "time", tick.Time, "price", tick.Price, "prev_price", prevTick.Price, "fills", len(fills))
	e.Syncer.Notify(tick.Time, fills)
	slog.Debug("engine: waiting for syncer", "time", tick.Time, "price", tick.Price, "prev_price", prevTick.Price)
	if err := trader.WaitSyncers(ctx, []*trader.Syncer{e.Syncer}); err != nil {
		return err
	}
	slog.Debug("engine: tick end", "time", tick.Time, "price", tick.Price, "prev_price", prevTick.Price)
	e.updateDrawdown(tick.Time, tick.Price)
	return nil
}

func (e *Engine) updateDrawdown(t time.Time, price decimal.Decimal) {
	def := e.product.def
	nlv := e.product.ex.NetLiquidationValue(def.BaseCurrencyID, def.QuoteCurrencyID, price)

	// Track global min/max total NLV for display.
	if nlv.GreaterThan(e.peakNLV) {
		e.peakNLV = nlv
	}
	if e.minNLV.IsZero() || nlv.LessThan(e.minNLV) {
		e.minNLV = nlv
	}

	// Compute strategy NLV = budget + profit so far.
	// idle cash never moves with the strategy, so we strip it out once.
	if e.initialNLV.IsZero() {
		e.initialNLV = nlv
	}
	idleCash := e.initialNLV.Sub(e.Budget)
	stratNLV := nlv.Sub(idleCash)

	if stratNLV.GreaterThan(e.peakStratNLV) {
		e.peakStratNLV = stratNLV
		e.peakStratNLVTime = t
	}
	if e.peakStratNLV.IsZero() {
		return
	}
	drop := e.peakStratNLV.Sub(stratNLV)
	dd := drop.Div(e.peakStratNLV).Mul(decimal.NewFromInt(100))
	if dd.GreaterThan(e.maxDrawdownPct) {
		e.maxDrawdownPct = dd
		e.maxDrawdownPctEpisode = DrawdownEpisode{
			PeakTime:   e.peakStratNLVTime,
			PeakNLV:    e.peakStratNLV,
			TroughTime: t,
			TroughNLV:  stratNLV,
		}
	}
	if drop.GreaterThan(e.maxDrawdownAbs) {
		e.maxDrawdownAbs = drop
		e.maxDrawdownAbsEpisode = DrawdownEpisode{
			PeakTime:   e.peakStratNLVTime,
			PeakNLV:    e.peakStratNLV,
			TroughTime: t,
			TroughNLV:  stratNLV,
		}
	}
}
