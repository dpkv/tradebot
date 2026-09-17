// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvkgo/kv"
)

// OrderReconcileInterval controls how often orders live at the broker are
// compared against every limiter's locally tracked state.
//
// This exists because of a real incident (2026-09-14): a cold IBKR gateway
// order cache right after a reconnect caused a limiter's fetchOrderMap to
// conclude a still-live order was cancelled, when it was merely missing
// from that one snapshot. The limiter abandoned it and placed a
// replacement, leaving the original resting at the broker, untracked,
// indefinitely. Three TSLA buy orders sat like this for up to three weeks
// before being noticed manually. This check catches that class of bug
// within one interval instead of relying on a human to notice a stray
// fill.
const OrderReconcileInterval = 15 * time.Minute

// orderReconcileAlertFreeze is how long we wait before re-alerting on the
// same untracked order, so a still-unresolved order doesn't re-send every
// interval.
const orderReconcileAlertFreeze = time.Hour

// watchForUntrackedOrders periodically diffs the broker's live orders
// against every limiter's tracked state and alerts on any broker order
// that no limiter considers active.
func (s *Server) watchForUntrackedOrders(ctx context.Context, ex exchange.Exchange) error {
	// Reconciliation currently only supports IBKR: it's the only exchange
	// where an order can go untracked this way, since its live-orders
	// endpoint is a bulk snapshot that can come back incomplete right after a
	// gateway reconnect, unlike Coinbase/CoinEx's per-order lookups.
	ibkrEx, ok := ex.(*ibkr.Exchange)
	if !ok {
		return nil
	}

	ticker := time.NewTicker(OrderReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-ticker.C:
			if err := s.checkUntrackedOrders(ctx, ibkrEx); err != nil {
				slog.Error("could not check for untracked broker orders (will retry)", "exchange", ex.ExchangeName(), "err", err)
			}
		}
	}
}

func (s *Server) checkUntrackedOrders(ctx context.Context, ex *ibkr.Exchange) error {
	brokerOrders, err := ex.GetOrders(ctx)
	if err != nil {
		return err
	}

	tracked := make(map[string]bool)
	load := func(ctx context.Context, r kv.Reader) error {
		limiters, err := limiter.LoadAll(ctx, r)
		if err != nil {
			return err
		}
		for _, l := range limiters {
			if l.ExchangeName() != ex.ExchangeName() {
				continue
			}
			for _, id := range l.LiveOrderIDs() {
				tracked[id] = true
			}
		}
		return nil
	}
	if err := kv.WithReader(ctx, s.db, load); err != nil {
		return fmt.Errorf("could not load limiters to check tracked orders: %w", err)
	}

	now := time.Now()
	var lines []string
	for _, o := range brokerOrders {
		if o.IsDone() {
			continue
		}
		id := o.ServerID()
		if tracked[id] {
			continue
		}

		key := "alerts/untracked-order/" + ex.ExchangeName() + "/" + id
		if deadline, ok := s.alertFreezeDeadlineMap[key]; ok && now.Before(deadline) {
			continue
		}
		s.alertFreezeDeadlineMap[key] = now.Add(orderReconcileAlertFreeze)

		var created string
		if o.LastExecutionTimeMilli != 0 {
			created = time.UnixMilli(o.LastExecutionTimeMilli).Format(time.RFC3339)
		}
		lines = append(lines, fmt.Sprintf("order-id=%s %s %s %s qty=%s price=%s created=%s",
			id, o.Symbol, o.Side, o.Status, o.OrderedQty.String(), o.LimitPrice.String(), created))
	}

	if len(lines) == 0 {
		return nil
	}

	s.SendMessage(ctx, now,
		"Found %d order(s) live on %s but not tracked by any trading job (possible duplicate/orphan — check and cancel manually if unwanted):\n%s",
		len(lines), ex.ExchangeName(), strings.Join(lines, "\n"))
	return nil
}
