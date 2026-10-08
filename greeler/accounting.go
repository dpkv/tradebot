// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

// The accounting model (greel/accounting-story.md): each level's fills form
// a FIFO of lots, an assignment is a fill at the strike split over the
// levels by the allocation rule, and premium is its own Summary line. All
// of it is a replay of Epochs; nothing is stored.

// refreshFacts snapshots every position that hadn't ended at the last
// snapshot. Run calls it after each step, from the goroutine that drives
// the positions.
func (v *Greeler) refreshFacts() {
	v.mu.Lock()
	var stale []*epoch
	for _, e := range v.epochs {
		if e.position != nil && (e.facts == nil || e.facts.Outcome == "") {
			stale = append(stale, e)
		}
	}
	v.mu.Unlock()

	for _, e := range stale {
		facts := e.position.Facts()
		v.mu.Lock()
		e.facts = facts
		v.mu.Unlock()
	}
}

// fill is one entry in a level's replay: a limiter's fills, or a level's
// share of an assignment.
type fill struct {
	buy     bool
	sum     *gobs.Summary // the whole fill; Bought* for a buy, Sold* for a sell
	inRange bool

	action *gobs.Action // nil for a limiter, whose Actions are its own
}

// fills replays the epochs, oldest first, into per-level fills and the
// premiums of every wheel epoch with its position's outcome.
func (v *Greeler) fills(r *timerange.Range) ([][]*fill, []*optpos.Facts, error) {
	// Copy what the replay needs; Run may append epochs meanwhile.
	type snap struct {
		mode     string
		limiters [][]*limiter.Limiter
		uid      string
		facts    *optpos.Facts
	}
	v.mu.Lock()
	snaps := make([]snap, 0, len(v.epochs))
	for _, e := range v.epochs {
		sn := snap{mode: e.Mode, uid: e.PositionID, facts: e.facts}
		for _, ls := range e.limiters {
			sn.limiters = append(sn.limiters, append([]*limiter.Limiter(nil), ls...))
		}
		snaps = append(snaps, sn)
	}
	v.mu.Unlock()

	inRange := func(t time.Time) bool { return r == nil || r.InRange(t) }
	levels := make([][]*fill, len(v.levels))
	var facts []*optpos.Facts
	for _, e := range snaps {
		switch {
		case e.mode == "grid":
			for i, ls := range e.limiters {
				for _, l := range ls {
					sum := l.GetSummary(nil)
					if !sum.BoughtSize.IsPositive() && !sum.SoldSize.IsPositive() {
						continue
					}
					levels[i] = append(levels[i], &fill{buy: l.IsBuy(), sum: sum, inRange: inRange(sum.EndAt)})
				}
			}
		case e.facts != nil:
			facts = append(facts, e.facts)
			fact := e.facts.Assignment
			if e.facts.Outcome != "assigned" || fact == nil {
				continue
			}
			total := fact.Shares.Abs()
			parts, err := v.attribute(total)
			if err != nil {
				return nil, nil, fmt.Errorf("greeler %s position %s: %w", v.uid, e.uid, err)
			}
			for i, part := range parts {
				if part.IsPositive() {
					levels[i] = append(levels[i], v.assignedFill(e.uid, fact, e.facts.OutcomeAt, part, total, inRange(e.facts.OutcomeAt), i))
				}
			}
		}
	}
	return levels, facts, nil
}

// assignedFill is level i's part of an assignment of total shares: a buy
// at the strike for a put, a sell for a call, with the fee split pro rata.
func (v *Greeler) assignedFill(posUID string, fact *gobs.AssignmentFact, at time.Time, part, total decimal.Decimal, inRange bool, i int) *fill {
	buy := fact.Shares.IsPositive()
	value := part.Mul(fact.Price)
	fee := fact.Fee.Mul(part).Div(total)
	units := part.Div(v.levels[i].Buy.Size)
	sum := &gobs.Summary{BeginAt: at, EndAt: at}
	side := "SELL"
	if buy {
		side = "BUY"
		sum.BoughtSize, sum.BoughtValue, sum.BoughtFees, sum.NumBuys = part, value, fee, units
	} else {
		sum.SoldSize, sum.SoldValue, sum.SoldFees, sum.NumSells = part, value, fee, units
	}
	order := &gobs.Order{
		ServerOrderID: fact.Key,
		CreateTime:    gobs.RemoteTime{Time: at},
		FinishTime:    gobs.RemoteTime{Time: at},
		Side:          side,
		Status:        "ASSIGNED",
		FilledFee:     fee,
		FilledSize:    part,
		FilledPrice:   fact.Price,
		Done:          true,
		DoneReason:    "assigned",
	}
	action := &gobs.Action{
		UID:    posUID,
		Point:  gobs.Point{Size: part, Price: fact.Price},
		Orders: []*gobs.Order{order},
	}
	return &fill{buy: buy, sum: sum, inRange: inRange, action: action}
}

// scaled is the num/den share of a fill's sizes, values and fees.
func scaled(s *gobs.Summary, num, den decimal.Decimal) *gobs.Summary {
	if num.Equal(den) {
		return s
	}
	f := func(x decimal.Decimal) decimal.Decimal { return x.Mul(num).Div(den) }
	return &gobs.Summary{
		BeginAt:     s.BeginAt,
		EndAt:       s.EndAt,
		NumBuys:     f(s.NumBuys),
		NumSells:    f(s.NumSells),
		BoughtFees:  f(s.BoughtFees),
		BoughtSize:  f(s.BoughtSize),
		BoughtValue: f(s.BoughtValue),
		SoldFees:    f(s.SoldFees),
		SoldSize:    f(s.SoldSize),
		SoldValue:   f(s.SoldValue),
	}
}

// lot is what's left of a buy.
type lot struct {
	*fill
	left decimal.Decimal
}

// addLevel adds one level's fills to s. Each sell closes the level's oldest
// lots first. Within a range, a sell counts its lots in full even if they
// were bought before the range (as Looper.GetSummary does); a lot bought
// in the range and not sold in it counts as unsold, at its own cost.
func addLevel(s *gobs.Summary, fills []*fill) {
	unsold := func(part *gobs.Summary) {
		s.UnsoldFees = s.UnsoldFees.Add(part.BoughtFees)
		s.UnsoldSize = s.UnsoldSize.Add(part.BoughtSize)
		s.UnsoldValue = s.UnsoldValue.Add(part.BoughtValue)
	}
	var lots []*lot
	for _, f := range fills {
		if f.buy {
			lots = append(lots, &lot{fill: f, left: f.sum.BoughtSize})
			continue
		}
		if f.inRange {
			s.Add(f.sum)
		}
		left := f.sum.SoldSize
		for left.IsPositive() && len(lots) > 0 {
			l := lots[0]
			take := decimal.Min(left, l.left)
			if f.inRange || l.inRange {
				part := scaled(l.sum, take, l.sum.BoughtSize)
				s.Add(part)
				if !f.inRange {
					unsold(part)
				}
			}
			l.left, left = l.left.Sub(take), left.Sub(take)
			if !l.left.IsPositive() {
				lots = lots[1:]
			}
		}
		if left.IsPositive() && f.inRange {
			// Can't happen while fold keeps holdings non-negative.
			over := scaled(f.sum, left, f.sum.SoldSize)
			s.OversoldFees = s.OversoldFees.Add(over.SoldFees)
			s.OversoldSize = s.OversoldSize.Add(over.SoldSize)
			s.OversoldValue = s.OversoldValue.Add(over.SoldValue)
		}
	}
	for _, l := range lots {
		if l.inRange {
			part := scaled(l.sum, l.left, l.sum.BoughtSize)
			s.Add(part)
			unsold(part)
		}
	}
}

// addPremiums adds every premium filled in the range, holding out of
// profit the ones whose position hasn't ended.
func addPremiums(s *gobs.Summary, facts []*optpos.Facts, r *timerange.Range) {
	for _, f := range facts {
		for _, p := range f.Premiums {
			if r != nil && !r.InRange(p.At) {
				continue
			}
			ps := &gobs.Summary{BeginAt: p.At, EndAt: p.At, PremiumValue: p.Value, PremiumFees: p.Fee}
			if f.Outcome == "" {
				ps.OpenPremiumValue, ps.OpenPremiumFees = p.Value, p.Fee
			}
			s.Add(ps)
		}
	}
}

// GetSummary is the greel's P&L: grid round trips, assignments as fills at
// the strike, and option premium.
func (v *Greeler) GetSummary(r *timerange.Range) *gobs.Summary {
	s := &gobs.Summary{
		Exchange:  v.cfg.ExchangeName,
		ProductID: v.cfg.ProductID,
		Budget:    v.BudgetAt(decimal.Zero),
	}
	levels, facts, err := v.fills(r)
	if err != nil {
		slog.Error("could not replay greeler fills for its summary", "greeler", v, "err", err)
		return s
	}
	for _, fills := range levels {
		addLevel(s, fills)
	}
	addPremiums(s, facts, r)
	return s
}

// Actions returns the stock limiters' filled orders and the assignments,
// paired per level. Premium isn't an action; the summary reports it.
func (v *Greeler) Actions() []*gobs.Action {
	levels, _, err := v.fills(nil)
	if err != nil {
		slog.Error("could not replay greeler fills for its actions", "greeler", v, "err", err)
		return nil
	}
	var actions []*gobs.Action
	add := func(i int, a *gobs.Action) {
		a.PairingKey = fmt.Sprintf("%s/level-%03d", v.uid, i)
		actions = append(actions, a)
	}
	v.mu.Lock()
	limiters := make([][]*limiter.Limiter, len(v.levels))
	for _, e := range v.epochs {
		for i, ls := range e.limiters {
			limiters[i] = append(limiters[i], ls...)
		}
	}
	v.mu.Unlock()
	for i, ls := range limiters {
		for _, l := range ls {
			if as := l.Actions(); len(as) > 0 {
				add(i, as[0])
			}
		}
		for _, f := range levels[i] {
			if f.action != nil {
				add(i, f.action)
			}
		}
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i].Orders[0].CreateTime.Time.Before(actions[j].Orders[0].CreateTime.Time)
	})
	if len(actions) == 0 {
		return nil
	}
	return actions
}

// Summary loads the greeler at uid and its children from saved records
// and returns its summary for period (nil for its whole life). Nothing is
// cached: it is folded on every call.
func Summary(ctx context.Context, r kv.Reader, uid string, period *timerange.Range) (*gobs.Summary, error) {
	v, err := Load(ctx, uid, r)
	if err != nil {
		return nil, err
	}
	return v.GetSummary(period), nil
}
