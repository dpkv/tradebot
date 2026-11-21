// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

// Tax lots on stock sells (greel/tax-lots-story.md): a level's sell names
// the broker lots of the buys the level took in since it was last flat.

// sources is what one level's sell closes: the buys behind the level's
// holding, whether part of that holding has no lots we can name (it came
// from a put assignment), in which case the sell names none, and whether a
// call assignment left the level partly held, in which case the broker
// chose which of the buys' lots went and the sell names only what is left
// of them (greel/assignment-lots-story.md scenario 5).
type sources struct {
	buys     []*limiter.Limiter
	unlotted bool
	called   bool
}

// lots is every tax lot of the source buys, in order.
func (s *sources) lots() []gobs.TaxLotID {
	var ids []gobs.TaxLotID
	for _, b := range s.buys {
		ids = append(ids, b.TaxLotIDs()...)
	}
	return ids
}

// missing is the source buys that have no tax lots yet.
func (s *sources) missing() []*limiter.Limiter {
	var ms []*limiter.Limiter
	for _, b := range s.buys {
		if len(b.TaxLotIDs()) == 0 {
			ms = append(ms, b)
		}
	}
	return ms
}

// sellSources replays level's entries in the order fold does and returns
// the sources of its holding since the holding was last zero. The replay
// stops at stop, the level's unfinished sell when it is being resumed, so
// that sell isn't counted against its own sources; nil replays everything.
func (v *Greeler) sellSources(level int, stop *limiter.Limiter) (*sources, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	var held decimal.Decimal
	s := new(sources)
	add := func(size decimal.Decimal) {
		held = held.Add(size)
	}
	sub := func(size decimal.Decimal) {
		held = held.Sub(size)
		if !held.IsPositive() {
			held = decimal.Zero
			s = new(sources)
		}
	}
	for ei, e := range v.epochs {
		switch e.Mode {
		case "grid":
			for _, l := range e.limiters[level] {
				if l == stop {
					return s, nil
				}
				size := l.FilledSize()
				if !size.IsPositive() {
					continue
				}
				if l.IsBuy() {
					add(size)
					s.buys = append(s.buys, l)
				} else {
					sub(size)
				}
			}
		case "wheel":
			fact, err := v.assignment(ei, e)
			if err != nil {
				return nil, err
			}
			if fact == nil {
				continue
			}
			parts, err := v.attribute(fact.Shares.Abs())
			if err != nil {
				return nil, fmt.Errorf("greeler %s epoch %d: %w", v.uid, ei, err)
			}
			part := parts[level]
			if !part.IsPositive() {
				continue
			}
			if fact.Shares.IsPositive() {
				add(part)
				s.unlotted = true
			} else {
				sub(part)
				if held.IsPositive() {
					s.called = true
				}
			}
		}
	}
	return s, nil
}

// assignment is wheel epoch e's assignment, nil if it wasn't assigned. It
// reads the position once Run has loaded it, else the saved facts. Called
// with mu held.
func (v *Greeler) assignment(ei int, e *epoch) (*gobs.AssignmentFact, error) {
	switch {
	case e.position != nil:
		if e.position.Outcome() != "assigned" {
			return nil, nil
		}
		if fact := e.position.Assignment(); fact != nil {
			return fact, nil
		}
	case e.facts != nil:
		if e.facts.Outcome != "assigned" {
			return nil, nil
		}
		if e.facts.Assignment != nil {
			return e.facts.Assignment, nil
		}
	default:
		return nil, fmt.Errorf("greeler %s epoch %d position is not loaded", v.uid, ei)
	}
	return nil, fmt.Errorf("greeler %s epoch %d position is assigned without a fact", v.uid, ei)
}

// sellLots is the tax lots level's next sell of size names, stop being
// the sell when it is resumed. On a product that resolves tax lots, it
// first resolves the lots of any source buy that lacks them, and reports
// false if some are still missing: the level skips its sell this time.
// Shares with no lots to name sell without any, and so do lots that no
// longer hold size shares (another sale, such as a call assignment by the
// account's lot method, took some of them).
func (r *runner) sellLots(ctx context.Context, level int, stop *limiter.Limiter, size decimal.Decimal) ([]gobs.TaxLotID, bool) {
	v := r.v
	src, err := v.sellSources(level, stop)
	if err != nil {
		slog.Error("could not find the buys a sell closes (will retry)", "greeler", v, "level", level, "err", err)
		return nil, false
	}
	if src.unlotted {
		slog.Info("level holds shares with no tax lots to name; selling by the account's default lot method", "greeler", v, "level", level)
		return nil, true
	}
	resolver, ok := r.rt.Product.(exchange.TaxLotResolver)
	if !ok {
		if src.called {
			return nil, true
		}
		return src.lots(), true
	}
	missing := src.missing()
	for _, b := range missing {
		lots, size, err := resolver.ResolveTaxLots(ctx, b.ServerOrderIDs())
		if err != nil {
			slog.Error("could not resolve a buy's tax lots (will retry)", "greeler", v, "level", level, "limiter", b, "err", err)
			continue
		}
		if wanted := b.FilledSize(); !size.Equal(wanted) || len(lots) == 0 {
			slog.Error("a buy's tax lots resolved to an unexpected size (will retry)", "greeler", v, "level", level, "limiter", b, "wanted", wanted, "got", size)
			continue
		}
		b.SetTaxLotIDs(lots)
		if err := kv.WithReadWriter(ctx, r.rt.Database, b.Save); err != nil {
			slog.Error("could not save a buy's tax lots (will retry)", "greeler", v, "level", level, "limiter", b, "err", err)
			b.SetTaxLotIDs(nil)
			continue
		}
	}
	if len(src.missing()) != 0 {
		return nil, false
	}
	lots := src.lots()
	if src.called {
		return r.calledLots(ctx, level, lots, size)
	}
	if checker, ok := r.rt.Product.(exchange.TaxLotChecker); ok && len(lots) != 0 {
		held, err := checker.TaxLotsSize(ctx, lots)
		if err != nil {
			slog.Error("could not check a sell's tax lots (will retry)", "greeler", v, "level", level, "err", err)
			return nil, false
		}
		if !held.Equal(size) {
			r.lotsGone(ctx, level, size, &held)
			return nil, true
		}
	}
	return lots, true
}

// calledLots is the lots a level that a call assignment left partly held
// names for its sell of size: its buys' lots, each capped to size, so the
// sell draws what is left of them and the broker trims the rest. If they
// no longer hold size shares between them, or the product can't tell, the
// sell names none.
func (r *runner) calledLots(ctx context.Context, level int, lots []gobs.TaxLotID, size decimal.Decimal) ([]gobs.TaxLotID, bool) {
	v := r.v
	checker, ok := r.rt.Product.(exchange.TaxLotChecker)
	if !ok || len(lots) == 0 {
		slog.Info("level was partly called away and its lots can't be checked; selling by the account's default lot method", "greeler", v, "level", level)
		return nil, true
	}
	capped := make([]gobs.TaxLotID, len(lots))
	for i, l := range lots {
		capped[i] = l.WithQty(size)
	}
	held, err := checker.TaxLotsSize(ctx, capped)
	if err != nil {
		slog.Error("could not check a sell's tax lots (will retry)", "greeler", v, "level", level, "err", err)
		return nil, false
	}
	if held.LessThan(size) {
		r.lotsGone(ctx, level, size, &held)
		return nil, true
	}
	return capped, true
}

// lotsGone reports that level's lots no longer hold its sell's size
// shares (held, when known), so the sell goes by the account's default lot
// method.
func (r *runner) lotsGone(ctx context.Context, level int, size decimal.Decimal, held *decimal.Decimal) {
	v := r.v
	what := "no longer cover its sell"
	if held != nil {
		what = fmt.Sprintf("hold %s shares, not the %s its sell needs", held, size)
	}
	slog.Warn("level's tax lots no longer cover its sell; selling by the account's default lot method", "greeler", v, "level", level, "size", size, "lots-hold", held)
	r.notify(ctx, v.now(), "Greeler %s level %d's tax lots %s; selling %s by the account's default lot method (%s).", v.uid, level, what, v.cfg.ProductID, v.cfg.AccountName)
}

// lotResumedSell gives level's unfinished limiter l the level's tax lots
// if it is a sell that has none and hasn't filled anything, and saves it.
// A sell that has filled keeps going without lots: its fills went by the
// default lot method, so the buys' lots may no longer add up to what is
// left. It reports false, and sets the level's retry, if l should not run
// this time.
func (r *runner) lotResumedSell(ctx context.Context, now time.Time, level int, l *limiter.Limiter) bool {
	if l.IsBuy() || len(l.TaxLotIDs()) != 0 || !l.FilledSize().IsZero() {
		return true
	}
	lots, ok := r.sellLots(ctx, level, l, l.PendingSize())
	if !ok {
		r.retryAt[level] = now.Add(retryDelay)
		return false
	}
	if len(lots) == 0 {
		return true
	}
	l.SetTaxLotIDs(lots)
	if err := kv.WithReadWriter(ctx, r.rt.Database, l.Save); err != nil {
		slog.Warn("could not save a resumed sell's tax lots (will retry)", "greeler", r.v, "level", level, "limiter", l, "err", err)
		l.SetTaxLotIDs(nil)
		r.retryAt[level] = now.Add(retryDelay)
		return false
	}
	slog.Info("gave a resumed sell its level's tax lots", "greeler", r.v, "level", level, "limiter", l, "lots", len(lots))
	return true
}
