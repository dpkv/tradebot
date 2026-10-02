// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

// StockHoldings is implemented by exchanges that report how many shares of
// a product the account holds, settled or not. The ladder's stock
// reconciliation needs it; balance updates don't suffice, since they report
// what's free for orders, which moves whenever a sell rests.
type StockHoldings interface {
	GetStockHolding(ctx context.Context, productID string) (decimal.Decimal, error)
}

// Run drives the ladder's greelers, as Waller.Run drives its loopers, and
// reconciles stock every reconcileInterval. It returns once every greeler
// has returned: nil if each retired, else their errors.
func (v *GreelLadder) Run(ctx context.Context, rt *trader.Runtime) error {
	v.runtimeLock.Lock()
	defer v.runtimeLock.Unlock()

	slog.Info("started greel ladder", "greelladder", v, "greelers", len(v.greelers))

	// Saved before any greeler runs: afterwards each greeler saves itself.
	if err := kv.WithReadWriter(ctx, rt.Database, v.Save); err != nil {
		return err
	}

	type result struct {
		g   *greeler.Greeler
		err error
	}
	doneCh := make(chan result, len(v.greelers))
	for _, g := range v.greelers {
		g := g
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("CAUGHT PANIC", "panic", r)
					slog.Error(string(debug.Stack()))
					panic(r)
				}
			}()
			doneCh <- result{g: g, err: g.Run(ctx, rt)}
		}()
	}

	holdings, _ := rt.Exchange.(StockHoldings)
	if holdings == nil {
		slog.Warn("exchange doesn't report stock holdings; stock reconciliation is off", "greelladder", v, "exchange", v.cfg.ExchangeName)
	}
	ticker := time.NewTicker(v.reconcileInterval)
	defer ticker.Stop()

	var rec reconciler
	var errs []error
	for left := len(v.greelers); left > 0; {
		select {
		case res := <-doneCh:
			left--
			if res.err != nil && ctx.Err() == nil {
				slog.Error("greeler has failed (fix manually)", "greelladder", v, "greeler", res.g, "err", res.err)
				v.notify(ctx, rt, "Greeler %s of ladder %s has failed and needs a manual fix: %v", res.g.UID(), v.uid, res.err)
				errs = append(errs, fmt.Errorf("greeler %s: %w", res.g.UID(), res.err))
			}
		case <-ticker.C:
			if holdings != nil && ctx.Err() == nil {
				v.reconcile(ctx, rt, holdings, &rec)
			}
		}
	}

	slog.Info("stopped greel ladder", "greelladder", v, "cause", context.Cause(ctx))
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// reconcile compares the change in the account's shares against the change
// in the greelers' derived stock and alerts on unexplained movement. It
// never writes.
func (v *GreelLadder) reconcile(ctx context.Context, rt *trader.Runtime, holdings StockHoldings, rec *reconciler) {
	var derived decimal.Decimal
	for _, g := range v.greelers {
		stock, ok := g.DerivedStock()
		if !ok {
			slog.Debug("greeler hasn't derived its stock yet; skipping reconciliation", "greelladder", v, "greeler", g)
			return
		}
		derived = derived.Add(stock)
	}
	account, err := holdings.GetStockHolding(ctx, v.cfg.ProductID)
	if err != nil {
		slog.Warn("could not fetch stock holding (will retry)", "greelladder", v, "product", v.cfg.ProductID, "err", err)
		return
	}
	if m := rec.observe(account, derived); m != nil {
		slog.Warn("account stock moved differently than the greelers", "greelladder", v, "product", v.cfg.ProductID,
			"account-change", m.account, "greelers-change", m.derived, "unexplained", m.unexplained())
		v.notify(ctx, rt, "Greel ladder %s: %s shares of %s moved without the greelers (account %s, greelers %s since the last match).",
			v.uid, m.unexplained(), v.cfg.ProductID, m.account, m.derived)
	}
}

func (v *GreelLadder) notify(ctx context.Context, rt *trader.Runtime, format string, args ...any) {
	if rt.Messenger != nil {
		rt.Messenger.SendMessage(ctx, v.now(), format, args...)
	}
}

// reconciler tracks stock deltas since the last time the account and the
// greelers agreed. Deltas, not totals: the account may hold the same stock
// for other reasons.
type reconciler struct {
	based                    bool
	baseAccount, baseDerived decimal.Decimal

	// mismatched is true when the previous check didn't match. A mismatch
	// is reported only if it's still there a check later, since a greeler
	// derives a fill or an assignment some time after the account shows it.
	mismatched bool
}

// mismatch is unexplained movement since the base.
type mismatch struct {
	account, derived decimal.Decimal
}

func (m *mismatch) unexplained() decimal.Decimal {
	return m.account.Sub(m.derived)
}

// observe records one check and returns the mismatch to alert on, if any.
// After an alert the current values become the new base, so one movement
// alerts once.
func (r *reconciler) observe(account, derived decimal.Decimal) *mismatch {
	if !r.based {
		r.rebase(account, derived)
		return nil
	}
	m := &mismatch{account: account.Sub(r.baseAccount), derived: derived.Sub(r.baseDerived)}
	if m.unexplained().IsZero() {
		r.rebase(account, derived)
		return nil
	}
	if !r.mismatched {
		r.mismatched = true
		return nil
	}
	r.rebase(account, derived)
	return m
}

func (r *reconciler) rebase(account, derived decimal.Decimal) {
	r.based = true
	r.baseAccount, r.baseDerived = account, derived
	r.mismatched = false
}
