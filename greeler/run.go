// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/job"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvk/tradebot/point"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// errStopped is the cause a limiter is canceled with when the greeler
// stops it.
var errStopped = errors.New("greeler stopped the limiter")

// retryDelay is how long a level waits after its limiter failed before the
// limiter is run again.
const retryDelay = 30 * time.Second

var d100 = decimal.NewFromInt(100)

// running is one level's limiter in flight.
type running struct {
	limiter *limiter.Limiter
	job     *job.Job
	ctx     context.Context
	cancel  context.CancelCauseFunc
	done    chan struct{}
	err     error // the limiter's Run result, valid once done is closed
}

// runner is the state of one Run call.
type runner struct {
	v     *Greeler
	rt    *trader.Runtime
	optEx exchange.OptionsExchange

	// fctx bounds every child: canceled when Run returns.
	fctx context.Context

	spot decimal.Decimal

	running map[int]*running
	retryAt map[int]time.Time

	// settled holds the limiters whose last Run in this Run call returned
	// nil or the stop cause, so their orders are confirmed done. A flip to
	// wheel waits until every unfinished limiter is settled.
	settled map[*limiter.Limiter]bool

	// doneCh wakes the loop when a limiter returns.
	doneCh chan struct{}
}

func (v *Greeler) newRunner(fctx context.Context, rt *trader.Runtime, optEx exchange.OptionsExchange) *runner {
	return &runner{
		v:       v,
		rt:      rt,
		optEx:   optEx,
		fctx:    fctx,
		running: make(map[int]*running),
		retryAt: make(map[int]time.Time),
		settled: make(map[*limiter.Limiter]bool),
		doneCh:  make(chan struct{}, 1),
	}
}

// Run trades the greel until ctx is canceled. It needs an options exchange
// in rt.Exchange and the underlying's stock product in rt.Product. It
// returns nil once a retired greeler has nothing left to do.
func (v *Greeler) Run(ctx context.Context, rt *trader.Runtime) (status error) {
	v.runtimeLock.Lock()
	defer v.runtimeLock.Unlock()

	optEx, ok := rt.Exchange.(exchange.OptionsExchange)
	if !ok {
		return fmt.Errorf("greeler %s needs an options exchange: %w", v.uid, os.ErrInvalid)
	}
	if rt.Product == nil || rt.Product.ProductID() != v.cfg.ProductID {
		return fmt.Errorf("greeler %s needs product %s: %w", v.uid, v.cfg.ProductID, os.ErrInvalid)
	}
	if err := v.loadChildren(ctx, rt.Database, optEx); err != nil {
		return err
	}
	// The record must exist before any child names it.
	if err := kv.WithReadWriter(ctx, rt.Database, v.Save); err != nil {
		return err
	}

	fctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errStopped)

	r := v.newRunner(fctx, rt, optEx)
	defer r.stopAll()
	defer func() {
		if err := r.stopPosition(ctx); err != nil {
			status = errors.Join(status, err)
		}
	}()

	prices, err := rt.Product.GetPriceUpdates()
	if err != nil {
		return err
	}
	defer prices.Close()
	priceCh, err := topic.ReceiveCh(prices)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(v.interval)
	defer ticker.Stop()

	slog.Info("started greeler", "greeler", v, "mode", v.Mode())
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case p, ok := <-priceCh:
			if !ok {
				return fmt.Errorf("greeler %s price updates have stopped", v.uid)
			}
			price, _ := p.PricePoint()
			first := r.spot.IsZero()
			r.spot = price
			if !first {
				continue
			}
		case <-ticker.C:
		case <-r.doneCh:
		}
		if r.spot.IsZero() {
			continue
		}
		done, err := r.step(ctx)
		if err != nil {
			return err
		}
		if done {
			slog.Info("retired greeler has nothing left to do", "greeler", v)
			return nil
		}
	}
}

// loadChildren loads every wheel epoch's position, once. Load has already
// loaded the grid epochs' limiters; positions need the options exchange.
func (v *Greeler) loadChildren(ctx context.Context, db kv.Database, optEx exchange.OptionsExchange) error {
	if v.loaded {
		return nil
	}
	return kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		for _, e := range v.epochs {
			switch e.Mode {
			case "wheel":
				pos, err := v.loadPosition(ctx, e.PositionID, r, optEx, db)
				if err != nil {
					return fmt.Errorf("could not load greeler %s position %s: %w", v.uid, e.PositionID, err)
				}
				e.position = pos
			}
		}
		v.loaded = true
		var pos position
		if last := v.current(); last.Mode == "wheel" {
			pos = last.position
		}
		v.updateHeld(pos)
		return nil
	})
}

func (v *Greeler) current() *epoch {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.epochs[len(v.epochs)-1]
}

// fold replays every epoch in order: grid epochs add their limiters' fills,
// and each assigned wheel epoch moves its shares into (put) or out of
// (call) the levels, lowest level first. It returns each level's holding.
func (v *Greeler) fold() ([]decimal.Decimal, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	holdings := make([]decimal.Decimal, len(v.levels))
	for ei, e := range v.epochs {
		switch e.Mode {
		case "grid":
			for i, ls := range e.limiters {
				for _, l := range ls {
					if l.IsBuy() {
						holdings[i] = holdings[i].Add(l.FilledSize())
					} else {
						holdings[i] = holdings[i].Sub(l.FilledSize())
					}
				}
			}
		case "wheel":
			if e.position == nil {
				return nil, fmt.Errorf("greeler %s epoch %d position is not loaded", v.uid, ei)
			}
			if e.position.Outcome() != "assigned" {
				continue
			}
			fact := e.position.Assignment()
			if fact == nil {
				return nil, fmt.Errorf("greeler %s epoch %d position is assigned without a fact", v.uid, ei)
			}
			parts, err := v.attribute(fact.Shares.Abs())
			if err != nil {
				return nil, fmt.Errorf("greeler %s epoch %d: %w", v.uid, ei, err)
			}
			for i, part := range parts {
				if fact.Shares.IsPositive() {
					holdings[i] = holdings[i].Add(part)
				} else {
					holdings[i] = holdings[i].Sub(part)
				}
			}
		}
	}
	for i, h := range holdings {
		if h.IsNegative() || h.GreaterThan(v.levels[i].Buy.Size) {
			return nil, fmt.Errorf("greeler %s level %d holds %s shares, outside [0, %s] (fix manually)", v.uid, i, h, v.levels[i].Buy.Size)
		}
	}
	return holdings, nil
}

// attribute splits an assignment's shares over the levels, lowest level
// first, filling each level's size before the next. It depends only on the
// levels, never on spot, so the replay is deterministic. One rule serves
// puts (shares arrive) and calls (shares leave).
func (v *Greeler) attribute(shares decimal.Decimal) ([]decimal.Decimal, error) {
	parts := make([]decimal.Decimal, len(v.levels))
	left := shares
	for i, p := range v.levels {
		if !left.IsPositive() {
			break
		}
		part := decimal.Min(left, p.Buy.Size)
		parts[i] = part
		left = left.Sub(part)
	}
	if left.IsPositive() {
		return nil, fmt.Errorf("assignment of %s shares exceeds the levels by %s", shares, left)
	}
	return parts, nil
}

// qualifies is the all-or-nothing rule: a put needs every level flat, a
// call needs every level full.
func (v *Greeler) qualifies(flip string, holdings []decimal.Decimal) bool {
	for i, h := range holdings {
		switch flip {
		case "wheel-put":
			if !h.IsZero() {
				return false
			}
		case "wheel-call":
			if !h.Equal(v.levels[i].Buy.Size) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (v *Greeler) top() decimal.Decimal {
	top := v.levels[0].Sell.Price
	for _, p := range v.levels[1:] {
		top = decimal.Max(top, p.Sell.Price)
	}
	return top
}

func (v *Greeler) bottom() decimal.Decimal {
	return v.levels[0].Buy.Price
}

// wheelSide reports the flip spot is past the far threshold for: every
// level below spot·(1−far) for a put, above spot·(1+far) for a call.
func (v *Greeler) wheelSide(spot decimal.Decimal) string {
	far := v.cfg.FarPct.Div(d100)
	if v.top().LessThanOrEqual(spot.Mul(decimal.NewFromInt(1).Sub(far))) {
		return "wheel-put"
	}
	if v.bottom().GreaterThanOrEqual(spot.Mul(decimal.NewFromInt(1).Add(far))) {
		return "wheel-call"
	}
	return ""
}

// backToGrid reports spot has come back within far−hysteresis of the
// levels on the side the position was written for.
func (v *Greeler) backToGrid(optionType string, spot decimal.Decimal) bool {
	near := v.cfg.FarPct.Sub(v.cfg.HysteresisPct).Div(d100)
	if optionType == "PUT" {
		return v.top().GreaterThan(spot.Mul(decimal.NewFromInt(1).Sub(near)))
	}
	return v.bottom().LessThan(spot.Mul(decimal.NewFromInt(1).Add(near)))
}

// inGridBand reports price is within spot·(1±grid), where new limiters
// may start.
func (v *Greeler) inGridBand(price, spot decimal.Decimal) bool {
	half := spot.Mul(v.cfg.GridPct).Div(d100)
	return price.Sub(spot).Abs().LessThanOrEqual(half)
}

// constraint bounds the contract to the levels: a put strikes at or below
// every level's buy price, a call at or above every level's sell price.
func (v *Greeler) constraint(optionType string) *optpos.Constraint {
	c := &optpos.Constraint{
		Underlying: v.cfg.ProductID,
		OptionType: optionType,
		Exclude:    v.exclude,

		ContractSize: contractShares,
	}
	if optionType == "PUT" {
		c.MaxStrike = v.bottom()
	} else {
		c.MinStrike = v.top()
	}
	return c
}

// dwellDone reports whether the open epoch's dwell clock has run long
// enough for want. A clock left running for another side doesn't count.
func (v *Greeler) dwellDone(e *epoch, want string, now time.Time) bool {
	return want != "" && e.PendingFlip == want && now.Sub(e.PendingFlipAt) >= v.cfg.DwellTime
}

// step runs one iteration. It returns true when a retired greeler is done.
func (r *runner) step(ctx context.Context) (bool, error) {
	v := r.v
	defer v.refreshFacts()
	r.reap()
	holdings, err := v.fold()
	if err != nil {
		return false, err
	}
	v.mu.Lock()
	v.holdings = holdings
	v.mu.Unlock()

	now := v.now()
	if v.current().Mode == "wheel" {
		return false, r.stepWheel(ctx, now, holdings)
	}
	return r.stepGrid(ctx, now, holdings)
}

func (r *runner) stepGrid(ctx context.Context, now time.Time, holdings []decimal.Decimal) (bool, error) {
	v := r.v
	e := v.current()

	want := v.wheelSide(r.spot)
	if err := r.setPending(ctx, e, want, now); err != nil {
		slog.Warn("could not save the dwell clock (will retry)", "greeler", v, "err", err)
	}
	if v.dwellDone(e, want, now) && !v.freezeWheelOpt && !v.retireOpt {
		if v.qualifies(want, holdings) {
			if err := r.flipToWheel(ctx, now, want); err != nil {
				if errors.Is(err, errHalt) {
					return false, err
				}
				slog.Warn("could not flip to wheel mode (will retry)", "greeler", v, "flip", want, "err", err)
			}
			return false, nil
		}
		slog.Debug("wheel flip is due but the levels don't qualify", "greeler", v, "flip", want, "holdings", holdings)
	}

	if v.retireOpt && len(r.running) == 0 && allZero(holdings) && !v.hasLiveOrders(e) {
		return true, nil
	}
	r.armLevels(ctx, now, e, holdings)
	return false, nil
}

// hasLiveOrders reports whether any of e's limiters knows of an order that
// isn't done.
func (v *Greeler) hasLiveOrders(e *epoch) bool {
	v.mu.Lock()
	var all []*limiter.Limiter
	for _, ls := range e.limiters {
		all = append(all, ls...)
	}
	v.mu.Unlock()
	for _, l := range all {
		if l.HasLiveOrders() {
			return true
		}
	}
	return false
}

func allZero(holdings []decimal.Decimal) bool {
	for _, h := range holdings {
		if !h.IsZero() {
			return false
		}
	}
	return true
}

// errHalt marks an error Run must stop on.
var errHalt = errors.New("greeler halted")

// flipToWheel stops every limiter, waits until each unfinished one has
// confirmed its order done, re-checks qualification, and opens a position
// under a new wheel epoch saved ahead of it. A limiter that hasn't
// confirmed (its cancel failed, it is waiting out a retry, or it hasn't run
// since a restart) is started so it recovers and cancels its order, and the
// flip waits: the dwell clock keeps running, and the next step's attempt
// stops the limiter and checks again. A fill that raced the cancel blocks
// the flip and resets the dwell clock.
func (r *runner) flipToWheel(ctx context.Context, now time.Time, flip string) error {
	v := r.v
	e := v.current()
	r.stopAll()

	waiting := false
	for i := range v.levels {
		v.mu.Lock()
		ls := e.limiters[i]
		v.mu.Unlock()
		if len(ls) == 0 {
			continue
		}
		last := ls[len(ls)-1]
		if !last.PendingSize().IsPositive() || r.settled[last] {
			continue
		}
		waiting = true
		if at, ok := r.retryAt[i]; ok && now.Before(at) {
			slog.Info("wheel flip waits for a level's limiter to retry and confirm its order", "greeler", v, "level", i, "limiter", last, "retry-at", at)
			continue
		}
		slog.Info("wheel flip waits for a level's limiter to confirm its order", "greeler", v, "level", i, "limiter", last)
		r.start(i, last)
	}
	if waiting {
		return nil
	}

	holdings, err := v.fold()
	if err != nil {
		return fmt.Errorf("%w: %w", errHalt, err)
	}
	if !v.qualifies(flip, holdings) {
		slog.Info("wheel flip is blocked by a fill during cancel; staying in grid mode", "greeler", v, "flip", flip, "holdings", holdings)
		return r.setPending(ctx, e, "", now)
	}

	optionType := "PUT"
	if flip == "wheel-call" {
		optionType = "CALL"
	}
	v.mu.Lock()
	uid := path.Join(v.uid, fmt.Sprintf("pos-%06d", len(v.epochs)))
	v.mu.Unlock()
	pos := v.newPosition(uid, r.optEx, r.rt.Database)
	ne := &epoch{
		GreelEpoch: gobs.GreelEpoch{Mode: "wheel", StartAt: now, PositionID: uid},
		position:   pos,
	}
	v.appendEpoch(ne)
	if err := kv.WithReadWriter(ctx, r.rt.Database, func(ctx context.Context, rw kv.ReadWriter) error {
		if err := pos.Save(ctx, rw); err != nil {
			return err
		}
		return v.Save(ctx, rw)
	}); err != nil {
		v.dropLastEpoch(ne)
		return fmt.Errorf("could not save wheel epoch: %w", err)
	}
	v.opened = false
	slog.Info("flipped to wheel mode", "greeler", v, "position", uid, "option-type", optionType, "spot", r.spot)
	r.notify(ctx, now, "Greeler %s flipped to wheel mode; writing a %s on %s (%s).", v.uid, optionType, v.cfg.ProductID, v.cfg.ExchangeName)

	// Check opens it later if this fails.
	err = pos.Open(ctx, r.fctx, v.constraint(optionType))
	v.updateHeld(pos)
	if err != nil {
		slog.Warn("could not open option position (will retry)", "greeler", v, "position", uid, "err", err)
	}
	return nil
}

func (r *runner) stepWheel(ctx context.Context, now time.Time, holdings []decimal.Decimal) error {
	v := r.v
	e := v.current()
	pos := e.position

	if pos.Outcome() == "" {
		// The levels haven't moved since the flip: all flat for a put, all
		// full for a call.
		optionType := "CALL"
		if allZero(holdings) {
			optionType = "PUT"
		}

		if !v.opened {
			want := ""
			if v.backToGrid(optionType, r.spot) {
				want = "grid"
			}
			if err := r.setPending(ctx, e, want, now); err != nil {
				slog.Warn("could not save the dwell clock (will retry)", "greeler", v, "err", err)
			}
			if v.dwellDone(e, want, now) {
				switch err := pos.Abandon(ctx); {
				case err == nil:
					slog.Info("abandoned option position that never opened", "greeler", v, "position", pos.UID())
				case errors.Is(err, optpos.ErrOpened):
					v.opened = true
					if err := r.setPending(ctx, e, "", now); err != nil {
						slog.Warn("could not save the dwell clock (will retry)", "greeler", v, "err", err)
					}
				default:
					slog.Warn("could not abandon option position (will retry)", "greeler", v, "position", pos.UID(), "err", err)
				}
			}
		}

		if pos.Outcome() == "" {
			if err := pos.Check(ctx, r.fctx, v.constraint(optionType)); err != nil {
				slog.Warn("could not check option position (will retry)", "greeler", v, "position", pos.UID(), "err", err)
			}
		}
		v.updateHeld(pos)
		if pos.Outcome() == "" {
			return nil
		}
	}

	v.updateHeld(pos)
	ne := v.newGridEpoch(now)
	v.appendEpoch(ne)
	if err := kv.WithReadWriter(ctx, r.rt.Database, v.Save); err != nil {
		v.dropLastEpoch(ne)
		slog.Warn("could not save grid epoch (will retry)", "greeler", v, "err", err)
		return nil
	}
	if fact := pos.Assignment(); fact != nil {
		slog.Info("option position was assigned; back to grid mode", "greeler", v, "position", pos.UID(), "shares", fact.Shares, "strike", fact.Price)
		r.notify(ctx, now, "Greeler %s was assigned %s shares of %s at %s (%s).", v.uid, fact.Shares, v.cfg.ProductID, fact.Price.StringFixed(2), v.cfg.ExchangeName)
	} else {
		slog.Info("option position ended; back to grid mode", "greeler", v, "position", pos.UID(), "outcome", pos.Outcome())
		r.notify(ctx, now, "Greeler %s option position on %s ended %s (%s).", v.uid, v.cfg.ProductID, pos.Outcome(), v.cfg.ExchangeName)
	}
	return nil
}

// armLevels resumes each level's unfinished limiter or starts its next one:
// a buy of the level's size when flat, else a sell of what it holds. New
// limiters start only within the grid band, and are saved before they run.
func (r *runner) armLevels(ctx context.Context, now time.Time, e *epoch, holdings []decimal.Decimal) {
	v := r.v
	for i, p := range v.levels {
		if _, ok := r.running[i]; ok {
			continue
		}
		if at, ok := r.retryAt[i]; ok && now.Before(at) {
			continue
		}
		v.mu.Lock()
		ls := e.limiters[i]
		v.mu.Unlock()
		if n := len(ls); n > 0 && ls[n-1].PendingSize().IsPositive() {
			// A retired greeler doesn't resume a buy that hasn't started,
			// unless its order may still be live (say, its cancel failed at
			// the last stop), which only the limiter can manage.
			if last := ls[n-1]; !(v.retireOpt && last.IsBuy() && last.FilledSize().IsZero() && !last.HasLiveOrders()) {
				r.start(i, last)
			}
			continue
		}
		if v.freezeGridOpt {
			continue
		}

		pt := p.Buy
		if h := holdings[i]; h.IsPositive() {
			pt = p.Sell
			pt.Size = h
		} else if v.retireOpt {
			continue
		}
		if !v.inGridBand(pt.Price, r.spot) {
			continue
		}

		l, err := r.addLimiter(ctx, e, i, &pt)
		if err != nil {
			slog.Warn("could not add a limiter (will retry)", "greeler", v, "level", i, "err", err)
			r.retryAt[i] = now.Add(retryDelay)
			continue
		}
		r.start(i, l)
	}
}

// addLimiter names a new limiter in the current epoch and saves both
// records before the limiter can place an order.
func (r *runner) addLimiter(ctx context.Context, e *epoch, level int, pt *point.Point) (*limiter.Limiter, error) {
	v := r.v
	v.mu.Lock()
	uid := path.Join(v.uid, fmt.Sprintf("epoch-%06d/level-%03d/%s-%06d", len(v.epochs)-1, level, strings.ToLower(pt.Side()), len(e.LevelLimiterIDs[level])))
	v.mu.Unlock()

	l, err := limiter.New(uid, v.cfg.ExchangeName, v.cfg.ProductID, pt)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	e.LevelLimiterIDs[level] = append(e.LevelLimiterIDs[level], uid)
	e.limiters[level] = append(e.limiters[level], l)
	v.mu.Unlock()

	if err := kv.WithReadWriter(ctx, r.rt.Database, func(ctx context.Context, rw kv.ReadWriter) error {
		if err := l.Save(ctx, rw); err != nil {
			return err
		}
		return v.Save(ctx, rw)
	}); err != nil {
		v.mu.Lock()
		e.LevelLimiterIDs[level] = e.LevelLimiterIDs[level][:len(e.LevelLimiterIDs[level])-1]
		e.limiters[level] = e.limiters[level][:len(e.limiters[level])-1]
		v.mu.Unlock()
		return nil, err
	}
	slog.Info("added limiter", "greeler", v, "level", level, "limiter", uid, "point", pt)
	return l, nil
}

// start runs a level's limiter in its own goroutine until it fills or the
// greeler stops it.
func (r *runner) start(level int, l *limiter.Limiter) {
	delete(r.settled, l)
	ctx, cancel := context.WithCancelCause(r.fctx)
	rl := &running{limiter: l, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	rt := r.rt
	rl.job = job.Run(func(ctx context.Context) error {
		defer func() {
			select {
			case r.doneCh <- struct{}{}:
			default:
			}
		}()
		defer close(rl.done)
		rl.err = l.Run(ctx, rt)
		return rl.err
	}, ctx)
	r.running[level] = rl
}

// reap clears limiters that returned on their own: filled, or failed (they
// run again after retryDelay).
func (r *runner) reap() {
	for i, rl := range r.running {
		select {
		case <-rl.done:
			r.finished(i, rl)
		default:
		}
	}
}

// finished clears a level's limiter that has returned. One that returned
// nil or the stop cause with every order it knows of done is settled. The
// error alone can't tell: a broker call interrupted by the stop returns an
// error wrapping the stop cause before the limiter got to cancel. Otherwise
// its order may be live: the limiter stays unsettled, runs again after
// retryDelay, and is saved so the live order is on record.
func (r *runner) finished(level int, rl *running) {
	delete(r.running, level)
	rl.cancel(errStopped)
	clean := rl.err == nil || errors.Is(rl.err, context.Cause(rl.ctx))
	if clean && !rl.limiter.HasLiveOrders() {
		r.settled[rl.limiter] = true
		return
	}
	slog.Warn("limiter returned without confirming its order is done (will retry)", "greeler", r.v, "level", level, "limiter", rl.limiter, "err", rl.err)
	r.retryAt[level] = r.v.now().Add(retryDelay)
	delete(r.settled, rl.limiter)
	if err := kv.WithReadWriter(context.WithoutCancel(r.fctx), r.rt.Database, rl.limiter.Save); err != nil {
		slog.Warn("could not save limiter after it failed (ignored)", "greeler", r.v, "level", level, "limiter", rl.limiter, "err", err)
	}
}

// stopPosition stops the current position's attempt, if any, and waits
// until its order is canceled and confirmed, so Run never returns with an
// order working at the broker.
func (r *runner) stopPosition(ctx context.Context) error {
	e := r.v.current()
	if e.Mode != "wheel" || e.position == nil {
		return nil
	}
	if err := e.position.Stop(context.WithoutCancel(ctx)); err != nil {
		slog.Error("could not stop option position before quitting", "greeler", r.v, "position", e.position.UID(), "err", err)
		return fmt.Errorf("could not stop greeler %s position %s: %w", r.v.uid, e.position.UID(), err)
	}
	return nil
}

// stopAll stops every limiter and waits until each has returned. Only the
// ones that confirmed their orders done end up settled.
func (r *runner) stopAll() {
	for _, rl := range r.running {
		rl.cancel(errStopped)
	}
	for i, rl := range r.running {
		<-rl.done
		r.finished(i, rl)
	}
}

// setPending moves the open epoch's dwell clock to want, starting it at now,
// and saves it when it changes.
func (r *runner) setPending(ctx context.Context, e *epoch, want string, now time.Time) error {
	v := r.v
	if e.PendingFlip == want {
		return nil
	}
	prev, prevAt := e.PendingFlip, e.PendingFlipAt
	at := now
	if want == "" {
		at = time.Time{}
	}
	v.mu.Lock()
	e.PendingFlip, e.PendingFlipAt = want, at
	v.mu.Unlock()
	if err := kv.WithReadWriter(ctx, r.rt.Database, v.Save); err != nil {
		v.mu.Lock()
		e.PendingFlip, e.PendingFlipAt = prev, prevAt
		v.mu.Unlock()
		return err
	}
	slog.Info("dwell clock changed", "greeler", v, "pending-flip", want, "spot", r.spot)
	return nil
}

func (r *runner) notify(ctx context.Context, at time.Time, format string, args ...any) {
	if r.rt.Messenger != nil {
		r.rt.Messenger.SendMessage(ctx, at, format, args...)
	}
}

func (v *Greeler) appendEpoch(e *epoch) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.epochs = append(v.epochs, e)
}

func (v *Greeler) dropLastEpoch(e *epoch) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if n := len(v.epochs); n > 0 && v.epochs[n-1] == e {
		v.epochs = v.epochs[:n-1]
	}
}
