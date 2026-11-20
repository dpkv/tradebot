// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"fmt"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/limiter"
	"github.com/shopspring/decimal"
)

// Tax lots on stock sells (greel/tax-lots-story.md): a level's sell names
// the broker lots of the buys the level took in since it was last flat.

// sources is what one level's sell closes: the buys behind the level's
// holding, and whether part of that holding has no lots we can name (it
// came from a put assignment, or a call assignment left the level partly
// held), in which case the sell names none.
type sources struct {
	buys     []*limiter.Limiter
	unlotted bool
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
					s.unlotted = true
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
