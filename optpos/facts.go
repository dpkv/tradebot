// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/optlimiter"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

// Facts is what accounting needs from a position: how it ended and the
// premium its legs collected. A snapshot; nothing in it changes once made.
type Facts struct {
	Outcome    string // "" while open or settling
	OutcomeAt  time.Time
	Assignment *gobs.AssignmentFact

	Premiums []*Premium
}

// Premium is one sell-to-open order's fill.
type Premium struct {
	At    time.Time
	Value decimal.Decimal // dollars: contracts × per-share price × ContractSize
	Fee   decimal.Decimal
}

// Facts snapshots the position. Like the other methods, call it from the
// goroutine that drives the position.
func (v *Position) Facts() *Facts {
	f := &Facts{Outcome: v.outcome, OutcomeAt: v.outcomeAt, Assignment: v.assignment}
	// Only the last leg can have filled (gobs-story scenario 5a).
	if v.leg != nil {
		f.Premiums = premiums(v.leg)
	}
	return f
}

// ReadFacts reads the position at uid and every one of its legs from saved
// records alone, so a greeler that isn't running can still account for it.
func ReadFacts(ctx context.Context, uid string, r kv.Reader) (*Facts, error) {
	key := path.Join(DefaultKeyspace, uid)
	gv, err := kvutil.Get[gobs.OptPositionState](ctx, r, key)
	if err != nil {
		return nil, fmt.Errorf("could not load optpos state: %w", err)
	}
	if gv.V1 == nil || gv.V1.Progress == nil {
		return nil, fmt.Errorf("optpos state at %q is incomplete", key)
	}
	progress := gv.V1.Progress
	f := &Facts{Outcome: progress.Outcome, OutcomeAt: progress.OutcomeAt, Assignment: progress.Assignment}
	for _, id := range progress.Legs {
		leg, err := optlimiter.Load(ctx, id, r)
		if err != nil {
			return nil, fmt.Errorf("could not load optpos %s attempt %s: %w", uid, id, err)
		}
		f.Premiums = append(f.Premiums, premiums(leg)...)
	}
	return f, nil
}

func premiums(leg *optlimiter.OptLimiter) []*Premium {
	var ps []*Premium
	for _, order := range leg.Orders() {
		if !order.FilledSize.IsPositive() {
			continue
		}
		at := order.FinishTime.Time
		if at.IsZero() {
			at = order.CreateTime.Time
		}
		ps = append(ps, &Premium{
			At:    at,
			Value: order.FilledPrice.Mul(order.FilledSize).Mul(leg.ContractSize()),
			Fee:   order.FilledFee,
		})
	}
	return ps
}
