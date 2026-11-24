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
	"github.com/bvk/tradebot/optpos"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

// Tax lots on stock sells (greel/tax-lots-story.md): a level's sell names
// the broker lots of the buys the level took in since it was last flat.

// sources is what one level's sell closes: the buys behind the level's
// holding, its parts of put assignments, which sell from the assignment's
// lot (greel/assignment-lots-story.md scenario 4), and whether a call
// assignment left the level partly held, in which case the broker chose
// which of the buys' lots went and the sell names only what is left of
// them (scenario 5).
type sources struct {
	buys     []*limiter.Limiter
	assigned []*assignedShare
	called   bool
}

// assignedShare is a level's part of a put assignment's shares.
type assignedShare struct {
	epoch *epoch
	part  decimal.Decimal
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
				s.assigned = append(s.assigned, &assignedShare{epoch: e, part: part})
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
	resolver, ok := r.rt.Product.(exchange.TaxLotResolver)
	if !ok {
		if src.called || len(src.assigned) != 0 {
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
	if len(src.assigned) != 0 {
		assigned, ok, found := r.assignedLots(ctx, level, src.assigned)
		if !ok {
			return nil, false
		}
		if !found {
			return nil, true
		}
		lots = append(lots, assigned...)
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

// assignedLotDeadline is when a put assignment at at stops waiting for its
// lot to show up in the portfolio and its levels sell by the account's
// default lot method: one trading day later, skipping a weekend.
func assignedLotDeadline(at time.Time) time.Time {
	d := at.Add(24 * time.Hour)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.Add(24 * time.Hour)
	}
	return d
}

// assignedLots is the tax lots level's parts of put assignments sell from:
// each assignment's lot, capped to the level's part. It finds and saves an
// assignment's lot the first time it is needed. ok is false when the level
// should wait and try again; found is false when the sell should name no
// lots, because the product can't find them or the wait ran out.
func (r *runner) assignedLots(ctx context.Context, level int, shares []*assignedShare) (lots []gobs.TaxLotID, ok, found bool) {
	v := r.v
	finder, isFinder := r.rt.Product.(exchange.AssignedLotFinder)
	for _, a := range shares {
		v.mu.Lock()
		lot := a.epoch.AssignedLot
		v.mu.Unlock()
		if lot == "" {
			if !isFinder {
				slog.Info("level holds assigned shares the product can't find lots for; selling by the account's default lot method", "greeler", v, "level", level)
				return nil, true, false
			}
			var err error
			if lot, err = r.findAssignedLot(ctx, finder, a.epoch); err != nil {
				facts := v.epochFacts(a.epoch)
				if deadline := assignedLotDeadline(facts.OutcomeAt); v.now().Before(deadline) {
					slog.Warn("could not find an assignment's tax lot yet (will retry)", "greeler", v, "level", level, "position", a.epoch.PositionID, "until", deadline, "err", err)
					return nil, false, false
				}
				slog.Warn("could not find an assignment's tax lot; selling by the account's default lot method", "greeler", v, "level", level, "position", a.epoch.PositionID, "err", err)
				r.notify(ctx, v.now(), "Greeler %s could not find the tax lot of its assignment (%v); level %d sells %s by the account's default lot method (%s).", v.uid, err, level, v.cfg.ProductID, v.cfg.AccountName)
				return nil, true, false
			}
		}
		lots = append(lots, lot.WithQty(a.part))
	}
	return lots, true, true
}

// findAssignedLot looks up wheel epoch e's assignment lot and saves it on
// the epoch.
func (r *runner) findAssignedLot(ctx context.Context, finder exchange.AssignedLotFinder, e *epoch) (gobs.TaxLotID, error) {
	v := r.v
	facts := v.epochFacts(e)
	if facts == nil || facts.Assignment == nil {
		return "", fmt.Errorf("position %s has no assignment", e.PositionID)
	}
	shares := facts.Assignment.Shares
	var kept decimal.Decimal
	for _, p := range facts.Premiums {
		kept = kept.Add(p.Value).Sub(p.Fee)
	}
	basis := facts.Assignment.Price.Sub(kept.Div(shares))
	lot, err := finder.FindAssignedLot(ctx, shares, facts.OutcomeAt, basis)
	if err != nil {
		return "", err
	}
	v.mu.Lock()
	e.AssignedLot = lot
	v.mu.Unlock()
	if err := kv.WithReadWriter(ctx, r.rt.Database, v.Save); err != nil {
		v.mu.Lock()
		e.AssignedLot = ""
		v.mu.Unlock()
		return "", err
	}
	slog.Info("found an assignment's tax lot", "greeler", v, "position", e.PositionID, "lot", lot, "basis", basis)
	return lot, nil
}

// epochFacts is wheel epoch e's position facts: from the position once Run
// has loaded it, else the saved ones.
func (v *Greeler) epochFacts(e *epoch) *optpos.Facts {
	if e.position != nil {
		return e.position.Facts()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return e.facts
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
