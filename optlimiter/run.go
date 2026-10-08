// Copyright (c) 2026 Deepak Vankadaru

package optlimiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/idgen"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// runState is the in-memory state of one Run call. Prices and the step
// count are per session and are not persisted: after a restart the live
// order (if any) is canceled and pricing restarts from the mid.
type runState struct {
	optEx   exchange.OptionsExchange
	product exchange.OptionsProduct
	db      kv.Database

	active      string          // server ID of the live order, if any
	activePrice decimal.Decimal // its limit price
	lastPrice   decimal.Decimal // price of the previous order this session
	step        int             // orders placed this session
	nextReprice time.Time       // zero means now
}

// Run places and re-prices until filled or ctx is cancelled; on cancel it
// cancels the live order and waits for confirmation. Saves itself to db
// ahead of each placement. It returns ctx's cause only once its orders are
// confirmed done; any other error means an order may still be live.
//
// At most one broker order is live at any time: a re-price cancels the
// live order and waits until the broker confirms it is done before placing
// the next one, sized to what is still unfilled.
func (v *OptLimiter) Run(ctx context.Context, optEx exchange.OptionsExchange, product exchange.OptionsProduct, db kv.Database) error {
	v.runMu.Lock()
	defer v.runMu.Unlock()

	if product.ContractID() != v.contractID {
		return fmt.Errorf("optlimiter %s: product is for contract %q, want %q: %w", v.uid, product.ContractID(), v.contractID, os.ErrInvalid)
	}
	if optEx.ExchangeName() != v.exchangeName {
		return fmt.Errorf("optlimiter %s: exchange is %q, want %q: %w", v.uid, optEx.ExchangeName(), v.exchangeName, os.ErrInvalid)
	}

	// Subscribe before recovery so no update between the refresh and the loop
	// is missed.
	orderUpdates, err := product.GetOrderUpdates()
	if err != nil {
		return err
	}
	defer orderUpdates.Close()

	updatesCh, err := topic.ReceiveCh(orderUpdates)
	if err != nil {
		return err
	}

	// Recovery runs to completion even if ctx is already canceled, so a
	// canceled Run still cancels any live order it finds and confirms it.
	bg := context.WithoutCancel(ctx)
	if err := v.recover(bg, optEx, product); err != nil {
		return err
	}
	if err := v.save(bg, db); err != nil {
		return err
	}

	rs := &runState{optEx: optEx, product: product, db: db}

	// The price of an order found live after a restart isn't known, so cancel
	// it and start again from the mid.
	for _, id := range v.liveOrders() {
		slog.Warn("canceling live option order found on resume", "optlimiter", v, "order-id", id)
		if err := v.cancel(bg, product, id); err != nil {
			return err
		}
		if err := v.save(bg, db); err != nil {
			return err
		}
	}

	slog.Info("started optlimiter", "optlimiter", v, "contract", v.contractID, "contracts", v.numContracts, "min-premium", v.minPremium, "filled", v.FilledSize())

	for !v.IsDone() {
		if ctx.Err() != nil {
			return v.shutdown(ctx, rs)
		}
		now := v.now()
		open, change := v.session(now)
		if !open {
			// Day orders die at the close; cancel and confirm anyway so the next
			// session can't overlap a still-live order.
			if err := v.cancelActive(context.WithoutCancel(ctx), rs); err != nil {
				return err
			}
			if v.IsDone() {
				break
			}
			rs.lastPrice, rs.step, rs.nextReprice = decimal.Zero, 0, time.Time{}
			slog.Info("optlimiter waiting for the regular session", "optlimiter", v, "until", change)
		} else if !rs.nextReprice.After(now) {
			if err := v.reprice(ctx, rs, now); err != nil {
				// Not a clean stop even if ctx was canceled meanwhile: a failed
				// placement may have left an order that isn't in the list yet,
				// or a failed cancel a live one, so the caller must run
				// recovery again.
				return err
			}
			continue
		}

		wake := change
		if open && rs.nextReprice.Before(wake) {
			wake = rs.nextReprice
		}
		timer := time.NewTimer(wake.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return v.shutdown(ctx, rs)

		case update, ok := <-updatesCh:
			timer.Stop()
			if !ok {
				err := fmt.Errorf("optlimiter %s: order updates have stopped", v.uid)
				if cerr := v.cancelActive(bg, rs); cerr != nil {
					return errors.Join(err, cerr)
				}
				return err
			}
			v.handleUpdate(ctx, rs, update)

		case <-timer.C:
		}
	}

	if err := v.refresh(bg, product); err != nil {
		return err
	}
	if err := v.save(bg, db); err != nil {
		return err
	}
	slog.Info("optlimiter is complete", "optlimiter", v, "filled", v.FilledSize(), "value", v.FilledValue())
	return nil
}

// reprice places the first order of a session, or replaces the live order
// with one at the next price.
func (v *OptLimiter) reprice(ctx context.Context, rs *runState, now time.Time) error {
	rs.nextReprice = now.Add(v.repriceInterval)

	quote, err := rs.optEx.GetOptionsProduct(ctx, v.contractID)
	if err != nil {
		slog.Warn("could not fetch option quote (will retry)", "optlimiter", v, "err", err)
		return nil
	}
	price, err := v.nextPrice(quote, rs.step, rs.lastPrice)
	if err != nil {
		slog.Warn("could not price option order (will retry)", "optlimiter", v, "err", err)
		return nil
	}
	if rs.active != "" && price.GreaterThanOrEqual(rs.activePrice) {
		// Already at the bid or the floor; leave it resting.
		return nil
	}

	bg := context.WithoutCancel(ctx)
	if err := v.cancelActive(bg, rs); err != nil {
		return err
	}
	pending := v.PendingSize()
	if pending.IsZero() {
		return nil
	}
	id, err := v.place(bg, rs, pending, price)
	if err != nil {
		return err
	}
	rs.active, rs.activePrice, rs.lastPrice = id, price, price
	rs.step++
	return nil
}

// place advances and saves the client ID offset, then places the order. A
// crash between the two leaves an ID that recovery looks up at the broker.
func (v *OptLimiter) place(ctx context.Context, rs *runState, size, price decimal.Decimal) (string, error) {
	clientID := v.idgen.NextID()
	if err := v.save(ctx, rs.db); err != nil {
		v.idgen.RevertID()
		return "", err
	}

	order, err := rs.product.LimitSellToOpen(ctx, clientID, size, price)
	if err != nil {
		// The order may or may not be at the broker; the next Run finds it by
		// client ID. Placing another one now could make two live orders.
		slog.Error("could not place sell-to-open order", "optlimiter", v, "client-order-id", clientID, "size", size, "price", price, "err", err)
		return "", fmt.Errorf("could not place sell-to-open order: %w", err)
	}

	sorder, err := exchange.NewSimpleOrder(order.ServerID(), clientID, "SELL")
	if err != nil {
		return "", err
	}
	v.mu.Lock()
	v.orders[sorder.ServerOrderID] = sorder
	v.mu.Unlock()

	if err := v.save(ctx, rs.db); err != nil {
		// Recovery finds the order by client ID if this is never saved.
		slog.Warn("could not save optlimiter after placing an order (ignored)", "optlimiter", v, "err", err)
	}
	slog.Info("placed sell-to-open order", "optlimiter", v, "order-id", sorder.ServerOrderID, "client-order-id", clientID, "size", size, "price", price, "step", rs.step)
	return sorder.ServerOrderID, nil
}

// cancelActive cancels the live order, if any, and waits until the broker
// confirms it is done. A fill that raced the cancel is recorded.
func (v *OptLimiter) cancelActive(ctx context.Context, rs *runState) error {
	if rs.active == "" {
		return nil
	}
	if err := v.cancel(ctx, rs.product, rs.active); err != nil {
		return err
	}
	rs.active, rs.activePrice = "", decimal.Zero
	if err := v.save(ctx, rs.db); err != nil {
		slog.Warn("could not save optlimiter after canceling an order (ignored)", "optlimiter", v, "err", err)
	}
	return nil
}

// cancel cancels an order and polls until the broker reports it done, for
// at most cancelTimeout. The broker's final detail replaces what the
// updates recorded, so the fill is right even if an update couldn't be
// merged.
func (v *OptLimiter) cancel(ctx context.Context, product exchange.OptionsProduct, id string) error {
	ctx, stop := context.WithTimeout(ctx, v.cancelTimeout)
	defer stop()

	cancelErr := product.Cancel(ctx, id)
	if cancelErr != nil {
		// The order may have finished already; Get below decides.
		slog.Warn("cancel option order failed", "optlimiter", v, "order-id", id, "err", cancelErr)
	}
	for {
		detail, err := product.Get(ctx, id)
		if err == nil && detail.IsDone() {
			return v.setOrder(id, detail)
		}
		if errors.Is(err, os.ErrNotExist) {
			v.markNotFound(id)
			return nil
		}
		if err == nil && cancelErr != nil {
			return fmt.Errorf("could not cancel option order %s: %w", id, cancelErr)
		}
		if err != nil {
			slog.Warn("could not fetch canceled option order (will retry)", "optlimiter", v, "order-id", id, "err", err)
		}
		if err := sleep(ctx, v.pollInterval); err != nil {
			return fmt.Errorf("could not confirm option order %s is done: %w", id, err)
		}
	}
}

// shutdown cancels the live order, waits for confirmation and saves.
func (v *OptLimiter) shutdown(ctx context.Context, rs *runState) error {
	bg := context.WithoutCancel(ctx)
	if rs.active != "" {
		slog.Info("canceling live option order before quitting", "optlimiter", v, "order-id", rs.active, "quit-reason", context.Cause(ctx))
	}
	if err := v.cancelActive(bg, rs); err != nil {
		return fmt.Errorf("could not cancel option order before quitting: %w", err)
	}
	if err := v.save(bg, rs.db); err != nil {
		slog.Error("could not save optlimiter before quitting (ignored)", "optlimiter", v, "err", err)
	}
	return context.Cause(ctx)
}

func (v *OptLimiter) handleUpdate(ctx context.Context, rs *runState, update exchange.OrderUpdate) {
	order := v.applyUpdate(update)
	if order == nil {
		return
	}
	if order.Done && order.ServerOrderID == rs.active {
		slog.Info("option order is done", "optlimiter", v, "order-id", rs.active, "status", order.Status, "filled", order.FilledSize)
		rs.active, rs.activePrice = "", decimal.Zero
		// Place the rest right away if a partial fill ended early. An order
		// that ended unfilled (rejected, expired) waits for the next re-price,
		// so repeated rejections can't place a burst of orders.
		if order.FilledSize.IsPositive() {
			rs.nextReprice = time.Time{}
		}
	}
	if err := v.save(ctx, rs.db); err != nil {
		slog.Warn("could not save optlimiter after an order update (will retry)", "optlimiter", v, "err", err)
	}
}

// applyUpdate merges an update into a known order and returns it, or
// returns nil if the order isn't this optlimiter's.
func (v *OptLimiter) applyUpdate(update exchange.OrderUpdate) *exchange.SimpleOrder {
	v.mu.Lock()
	defer v.mu.Unlock()

	order, ok := v.orders[update.ServerID()]
	if !ok {
		return nil
	}
	if _, err := order.AddUpdate(update); err != nil {
		slog.Warn("could not apply option order update (ignored)", "optlimiter", v, "order-id", update.ServerID(), "err", err)
	}
	return order
}

// setOrder replaces a known order with the broker's detail of it.
func (v *OptLimiter) setOrder(id string, detail exchange.OrderDetail) error {
	order, err := exchange.NewSimpleOrderFromOrderDetail(detail)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.orders[id] = order
	v.mu.Unlock()
	return nil
}

func (v *OptLimiter) markNotFound(id string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if order, ok := v.orders[id]; ok && !order.Done {
		// Brokers may drop canceled orders that never filled.
		order.Done = true
		order.DoneReason = "NOTFOUND/CANCELED"
	}
}

func (v *OptLimiter) liveOrders() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	var ids []string
	for id, order := range v.orders {
		if !order.Done {
			ids = append(ids, id)
		}
	}
	return ids
}

// recover adopts orders placed with a saved client ID that never made it
// into the order list (a crash between placing and saving), then refreshes
// every order not yet done.
func (v *OptLimiter) recover(ctx context.Context, optEx exchange.OptionsExchange, product exchange.OptionsProduct) error {
	known := make(map[uuid.UUID]bool)
	v.mu.Lock()
	for _, order := range v.orders {
		known[order.ClientUUID] = true
	}
	v.mu.Unlock()

	gen := idgen.New(v.idgen.Seed(), 0)
	for i, n := uint64(0), v.idgen.Offset(); i < n; i++ {
		clientID := gen.NextID()
		if known[clientID] {
			continue
		}
		detail, err := optEx.GetOptionsOrderByClientID(ctx, clientID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("could not look up option order by client id %s: %w", clientID, err)
		}
		order, err := exchange.NewSimpleOrderFromOrderDetail(detail)
		if err != nil {
			return err
		}
		slog.Warn("adopted option order found by client id", "optlimiter", v, "order-id", order.ServerOrderID, "client-order-id", clientID, "done", order.Done, "filled", order.FilledSize)
		v.mu.Lock()
		v.orders[order.ServerOrderID] = order
		v.mu.Unlock()
	}
	return v.refresh(ctx, product)
}

// refresh re-fetches every order not yet done.
func (v *OptLimiter) refresh(ctx context.Context, product exchange.OptionsProduct) error {
	for _, id := range v.liveOrders() {
		detail, err := product.Get(ctx, id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				v.markNotFound(id)
				continue
			}
			return fmt.Errorf("could not fetch option order %s: %w", id, err)
		}
		if err := v.setOrder(id, detail); err != nil {
			return err
		}
	}
	return nil
}

func (v *OptLimiter) save(ctx context.Context, db kv.Database) error {
	if err := kv.WithReadWriter(ctx, db, v.Save); err != nil {
		return fmt.Errorf("could not save optlimiter %s: %w", v.uid, err)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}
