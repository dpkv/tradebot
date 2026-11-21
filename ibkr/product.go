// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/bvk/tradebot/syncmap"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// clientIDStatus tracks the lifecycle of a single order placed by this
// product instance, keyed by the caller's UUID (which is stored as cOID).
type clientIDStatus struct {
	mu sync.Mutex

	// err is set if PlaceOrder failed. Non-nil err means the order was not
	// placed (or placement status is unknown).
	err error

	// order is the most recent order snapshot from the exchange. Nil until
	// goWatchOrderUpdates publishes the first matching update or PlaceOrder
	// returns.
	order *internal.Order
}

// cancelEntry tracks a pending cancel request for an order, counting
// consecutive observations where the order was not yet done.
type cancelEntry struct {
	mu        sync.Mutex
	at        time.Time
	missCount int
}

func newClientIDStatus() *clientIDStatus {
	return &clientIDStatus{}
}

func (s *clientIDStatus) isValidLocked() bool {
	return s.err == nil && s.order != nil
}

// Product implements exchange.Product for a single IBKR stock symbol.
//
// Because IBKR deduplicates orders by cOID (CanDedupOnClientUUID = true),
// there is no need for the sequential counter, DB-backed counterID mapping, or
// goCancelFailedCreates goroutine that E*TRADE requires.
type Product struct {
	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	wg sync.WaitGroup

	exchange *Exchange
	client   *Client

	symbol string
	conid  int

	// outsideRTH controls whether orders placed by this product can execute
	// outside regular trading hours (pre-market and after-hours sessions).
	outsideRTH bool

	// onBuyFill, if non-nil, is called once when a BUY order reaches Filled.
	onBuyFill func(ctx context.Context, productType, symbol string, filledQty, avgPrice decimal.Decimal)

	// notifiedFills tracks order IDs for which onBuyFill has already fired,
	// preventing duplicate notifications when the exchange returns the same
	// filled order across multiple poll cycles.
	notifiedFills syncmap.Map[int64, struct{}]

	// clientIDStatusMap maps the caller's UUID to the status of the order
	// placed with that UUID as cOID. Populated by LimitBuy/LimitSell and
	// updated by goWatchOrderUpdates.
	// TODO: We should cleanup the oldest orders.
	clientIDStatusMap syncmap.Map[uuid.UUID, *clientIDStatus]

	// pendingCancels maps orderID to a cancelEntry tracking when the cancel
	// was sent and how many consecutive not-done observations have occurred.
	// Used to synthesize a "Cancelled" update when the order drops off the
	// live orders list before goWatchOrders can observe its cancelled status.
	pendingCancels syncmap.Map[int64, *cancelEntry]
}

var _ exchange.Product = &Product{}

// NewProduct creates a Product for the given symbol and starts its background
// goroutine. WatchSymbol must have been called on the client before NewProduct
// so that the price and order topics exist. outsideRTH controls whether orders
// can execute outside regular trading hours.
func NewProduct(ctx context.Context, exch *Exchange, symbol string, conid int, outsideRTH bool, onBuyFill func(context.Context, string, string, decimal.Decimal, decimal.Decimal)) (*Product, error) {
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	p := &Product{
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		exchange:   exch,
		client:     exch.client,
		symbol:     symbol,
		conid:      conid,
		outsideRTH: outsideRTH,
		onBuyFill:  onBuyFill,
	}

	p.wg.Add(1)
	go p.goWatchOrderUpdates(p.lifeCtx)

	return p, nil
}

// Close stops the background goroutine and waits for it to exit.
func (p *Product) Close() error {
	p.lifeCancel(os.ErrClosed)
	p.wg.Wait()
	return nil
}

func (p *Product) ProductID() string {
	return p.symbol
}

func (p *Product) ExchangeName() string {
	return "ibkr"
}

// BaseMinSize returns 1 share — IBKR's minimum order size for equities.
func (p *Product) BaseMinSize() decimal.Decimal {
	return decimal.NewFromInt(1)
}

// GetOrderUpdates returns a receiver that delivers order updates for this
// symbol as they are published by goWatchOrders in the client.
func (p *Product) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	t := p.client.GetSymbolOrdersTopic(p.symbol)
	fn := func(o *internal.Order) exchange.OrderUpdate { return o }
	return topic.SubscribeFunc(t, fn, 0, true)
}

// GetPriceUpdates returns a receiver that delivers price updates for this
// symbol as they are published by goWatchPrices in the client.
func (p *Product) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	t := p.client.GetSymbolPricesTopic(p.symbol)
	if t == nil {
		return nil, os.ErrNotExist
	}
	fn := func(q *internal.Quote) exchange.PriceUpdate { return q }
	return topic.SubscribeFunc(t, fn, 1, true)
}

// LimitBuy places a GTC limit buy order. Idempotent on clientID — if an order
// with the same UUID was already placed this session, the cached result is
// returned without a new API call. IBKR also deduplicates by cOID server-side.
func (p *Product) LimitBuy(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (_ exchange.Order, status error) {
	return p.placeOrder(ctx, clientID, "BUY", size, price)
}

// LimitSell places a GTC limit sell order. Same idempotency guarantee as
// LimitBuy.
func (p *Product) LimitSell(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (_ exchange.Order, status error) {
	return p.placeOrder(ctx, clientID, "SELL", size, price)
}

// errIfRejected returns an exchange.ErrRejected error when order is an order
// IBKR refused. Callers use it before handing back an order they found by
// client ID: a rejected order still shows up in the gateway's orders list
// (as "Cancelled") under the client ID of the failed attempt, and returning
// it as if it were placed made the limiter treat the rejection as a
// completed order and immediately place another one.
func errIfRejected(order *internal.Order, clientID uuid.UUID) error {
	if order == nil || !order.IsRejected() {
		return nil
	}
	return fmt.Errorf("ibkr: order %d with client id %s was rejected by ibkr: %w", order.OrderID, clientID, exchange.ErrRejected)
}

func (p *Product) placeOrder(ctx context.Context, clientID uuid.UUID, side string, size, price decimal.Decimal) (_ exchange.Order, status error) {
	// Check persisted orders first — crash recovery path.
	if order, ok := p.exchange.findOrder(clientID); ok {
		if err := errIfRejected(order, clientID); err != nil {
			return nil, err
		}
		cstatus, _ := p.clientIDStatusMap.LoadOrStore(clientID, newClientIDStatus())
		cstatus.mu.Lock()
		cstatus.order = order
		cstatus.mu.Unlock()
		return order, nil
	}

	cstatus, loaded := p.clientIDStatusMap.LoadOrStore(clientID, newClientIDStatus())
	cstatus.mu.Lock()
	defer cstatus.mu.Unlock()

	// Local dedup: return the cached result for a UUID we've seen before.
	if loaded {
		if cstatus.err != nil {
			return nil, cstatus.err
		}
		if cstatus.order != nil {
			// goWatchOrderUpdates replaces a cached PlaceOrder error with the
			// order once it shows up in the orders list, which includes
			// rejected orders.
			if err := errIfRejected(cstatus.order, clientID); err != nil {
				return nil, err
			}
			return cstatus.order, nil
		}
		// PlaceOrder is still in flight from another goroutine — fall through
		// and let the exchange's cOID dedup handle it.
	}
	defer func() {
		cstatus.err = status
	}()

	serverOrderID, err := p.client.PlaceOrder(ctx, p.symbol, p.conid, side, size, price, clientID.String(), p.outsideRTH)
	if err != nil {
		return nil, err
	}

	// Construct a minimal Order from what we know. goWatchOrderUpdates will
	// replace it with a full snapshot on the next poll cycle.
	order := &internal.Order{
		OrderID:       serverOrderID,
		ClientOrderID: clientID.String(),
		Symbol:        p.symbol,
		Side:          side,
		Status:        "Submitted",
		LimitPrice:    price,
		OrderedQty:    size,
	}
	cstatus.order = order
	return order, nil
}

// findOrder looks up an order by IBKR numeric orderID across all three
// lookup paths. When found via the slow path (live gateway), it caches the
// result in clientIDStatusMap so subsequent calls can use the fast path —
// this is important for the restart case where clientIDStatusMap is initially
// empty and the limiter reuses an active order from a previous run.
//
// The slow path retries a few times before concluding an order is missing.
// IBKR's live-orders endpoint can return 200 OK with an incomplete list for a
// short window right after the gateway session reconnects, while its
// internal order cache is still warming up — not just the 503 that
// client.GetOrders already retries on. A caller here has already excluded
// orders it considers done, so every miss represents an order the caller
// still believes is live; treating a single miss as proof of cancellation
// caused fetchOrderMap to abandon still-open orders and place duplicates
// for them (see 2026-09-14 incident).
func (p *Product) findOrder(ctx context.Context, orderID int64) (*internal.Order, error) {
	// Fast path: clientIDStatusMap.
	for cid, cstatus := range p.clientIDStatusMap.Range {
		_ = cid
		cstatus.mu.Lock()
		if cstatus.order != nil && cstatus.order.OrderID == orderID {
			o := cstatus.order
			cstatus.mu.Unlock()
			return o, nil
		}
		cstatus.mu.Unlock()
	}

	// Slow path: poll the live gateway.
	const maxMisses = 3
	for attempt := 0; ; attempt++ {
		orders, err := p.client.GetOrders(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range orders {
			if o.OrderID == orderID {
				if attempt > 0 {
					slog.Warn("ibkr: order found in live orders list after retry — earlier miss(es) were a stale/incomplete snapshot, not a real cancellation", "orderID", orderID, "attempt", attempt+1)
				}
				p.cacheOrder(o)
				return o, nil
			}
		}

		// Persisted orders are done orders this bot already observed
		// (filled orders drop off the gateway cache).
		if o, ok := p.exchange.findOrderByServerID(orderID); ok {
			return o, nil
		}

		// The cancelled list can overflow the gateway's 1000-order limit, so
		// ask for the order itself before concluding it is gone.
		o, err := p.lookupMissingOrder(ctx, orderID)
		if err == nil {
			return o, nil
		}
		if errors.Is(err, errDoneWithoutFill) {
			return nil, os.ErrNotExist
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}

		if attempt+1 >= maxMisses {
			slog.Warn("ibkr: order not found in live orders list or by order status after all retries — treating as not-found", "orderID", orderID, "attempts", attempt+1)
			return nil, os.ErrNotExist
		}
		slog.Warn("ibkr: order not found in live orders list or by order status (will retry)", "orderID", orderID, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(missRetryDelay):
		}
	}
}

// missRetryDelay is the wait between findOrder's attempts to find an order
// that is missing from the orders list. It is a variable so tests can
// shorten it.
var missRetryDelay = 2 * time.Second

// errDoneWithoutFill means the gateway reports an order as done with no
// fill, but its client order id can't be recovered (there are no
// executions to take it from). Callers treat the order as not found, which
// records it as cancelled without a fill — what it is.
var errDoneWithoutFill = errors.New("ibkr: order is done without a fill and its client order id is unknown")

// lookupMissingOrder gets an order that is missing from the orders list
// from the gateway's order status endpoint. It never reports an order the
// gateway says is working as missing: if such an order's client order id
// can't be recovered, it returns an error instead of os.ErrNotExist, so the
// caller retries rather than placing a duplicate. Returns an error wrapping
// os.ErrNotExist if the gateway doesn't know the order, or
// errDoneWithoutFill (see above).
func (p *Product) lookupMissingOrder(ctx context.Context, orderID int64) (*internal.Order, error) {
	o, err := p.exchange.lookupOrderStatus(ctx, orderID, nil)
	if err != nil {
		return nil, err
	}
	if o.ClientOrderID == "" {
		if o.IsDone() && o.FilledQty.IsZero() {
			slog.Warn("ibkr: order missing from orders list is done without a fill", "orderID", orderID, "status", o.Status)
			return nil, errDoneWithoutFill
		}
		return nil, fmt.Errorf("ibkr: order %d is %s at ibkr (filled %s) but missing from the orders list, and its client order id is unknown", orderID, o.Status, o.FilledQty)
	}
	slog.Warn("ibkr: order missing from orders list was found by order status", "orderID", orderID, "status", o.Status, "filled", o.FilledQty)
	p.cacheOrder(o)
	return o, nil
}

// cacheOrder makes order the current snapshot for its client id, so later
// lookups take findOrder's fast path.
func (p *Product) cacheOrder(order *internal.Order) {
	cid := order.ClientID()
	if cid == uuid.Nil {
		return
	}
	cstatus, _ := p.clientIDStatusMap.LoadOrStore(cid, newClientIDStatus())
	cstatus.mu.Lock()
	cstatus.order = order
	cstatus.mu.Unlock()
}

// Get returns the current state of an order by server ID. When a cancel has
// been requested via Cancel and the order remains not-done (or disappears from
// the exchange) for at least 5 observations and 1 minute, Get synthesizes a
// "Cancelled" update, publishes it to the orders topic, and returns it so the
// caller's polling loop can exit without special-casing os.ErrNotExist.
func (p *Product) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return nil, err
	}

	order, err := p.findOrder(ctx, orderID)
	if err != nil && !isNotExist(err) {
		return nil, err // real error (e.g. network), propagate
	}

	if order != nil && order.IsDone() {
		p.exchange.markFillObserved(ctx, order)
		p.pendingCancels.Delete(orderID)
		return order, nil
	}

	// Order is not done (stale status) or not found on the exchange.
	// If no cancel is pending, return as-is.
	entry, pending := p.pendingCancels.Load(orderID)
	if !pending {
		if order != nil {
			return order, nil
		}
		return nil, os.ErrNotExist
	}

	entry.mu.Lock()
	entry.missCount++
	count := entry.missCount
	cancelAt := entry.at
	entry.mu.Unlock()

	if count < 5 || time.Since(cancelAt) < time.Minute {
		if order != nil {
			slog.Warn("ibkr: order not done after cancel (will retry)", "orderID", orderID, "retries", count, "elapsed", time.Since(cancelAt).Round(time.Second))
			return order, nil
		}
		slog.Warn("ibkr: canceled order not found on exchange (will retry)", "orderID", orderID, "retries", count, "elapsed", time.Since(cancelAt).Round(time.Second))
		return nil, os.ErrNotExist
	}

	// The orders list still doesn't show the order as done. Ask the gateway
	// for the order itself instead of assuming the cancel worked: the
	// cancelled list can overflow its 1000-order limit, and a cancel can
	// fail to take effect.
	status, err := p.exchange.lookupOrderStatus(ctx, orderID, order)
	if errors.Is(err, os.ErrNotExist) {
		// The gateway doesn't know the order at all, so there is nothing
		// left to cancel.
		return p.synthesizeCancelled(orderID, count, cancelAt)
	}
	if err != nil {
		return nil, err
	}
	if !status.IsDone() {
		// Still working a minute after the cancel request: send it again.
		slog.Warn("ibkr: order still working after cancel; cancelling again", "orderID", orderID, "status", status.Status, "retries", count, "elapsed", time.Since(cancelAt).Round(time.Second))
		if err := p.client.CancelOrder(ctx, orderID); err != nil {
			return nil, err
		}
		entry.mu.Lock()
		entry.at, entry.missCount = time.Now(), 0
		entry.mu.Unlock()
		return status, nil
	}
	if status.ClientOrderID == "" {
		// Done, but neither the bot nor the order's executions know its
		// client order id, so it can't be matched to a limiter's order.
		return p.synthesizeCancelled(orderID, count, cancelAt)
	}
	slog.Warn("ibkr: cancelled order missing from orders list was found by order status", "orderID", orderID, "status", status.Status, "filled", status.FilledQty)
	p.exchange.markFillObserved(ctx, status)
	p.publishOrder(status)
	p.pendingCancels.Delete(orderID)
	return status, nil
}

// publishOrder makes order the current snapshot for its client id and
// publishes it to the symbol's orders topic, so goWatchOrderUpdates and any
// limiter waiting on order updates see it.
func (p *Product) publishOrder(order *internal.Order) {
	p.cacheOrder(order)
	if err := p.client.GetSymbolOrdersTopic(p.symbol).Send(order); err != nil {
		slog.Warn("ibkr: could not publish order update", "orderID", order.OrderID, "err", err)
	}
}

// synthesizeCancelled builds a fake "Cancelled" order snapshot, updates the
// local caches, and publishes it to the orders topic so goWatchOrderUpdates
// processes it (persisting it and unblocking anything waiting on the order).
func (p *Product) synthesizeCancelled(orderID int64, missCount int, cancelAt time.Time) (*internal.Order, error) {
	// Recover the client order ID from the cached status map so that
	// goWatchOrderUpdates and updateOrderMap can match the update correctly.
	var clientOrderID string
	for cid, cstatus := range p.clientIDStatusMap.Range {
		cstatus.mu.Lock()
		if cstatus.order != nil && cstatus.order.OrderID == orderID {
			clientOrderID = cid.String()
			cstatus.mu.Unlock()
			break
		}
		cstatus.mu.Unlock()
	}

	synthetic := &internal.Order{
		OrderID:       orderID,
		ClientOrderID: clientOrderID,
		Symbol:        p.symbol,
		Status:        "Cancelled",
	}

	// Update clientIDStatusMap so the fast path returns the synthetic order
	// on any subsequent Get calls before goWatchOrderUpdates processes the topic.
	if clientOrderID != "" {
		if cid, err := uuid.Parse(clientOrderID); err == nil {
			if cstatus, ok := p.clientIDStatusMap.Load(cid); ok {
				cstatus.mu.Lock()
				cstatus.order = synthetic
				cstatus.mu.Unlock()
			}
		}
	}

	// Publish to the symbol orders topic so goWatchOrderUpdates picks it up
	// and calls persistOrder, populating serverIDMap for future lookups.
	t := p.client.GetSymbolOrdersTopic(p.symbol)
	if err := t.Send(synthetic); err != nil {
		slog.Warn("ibkr: could not publish synthetic cancelled order update", "orderID", orderID, "err", err)
	}

	// Also store directly in serverIDMap so the last-resort path in findOrder
	// finds it immediately, before the topic message is consumed.
	p.exchange.serverIDMap.Store(orderID, synthetic)
	p.pendingCancels.Delete(orderID)

	slog.Warn("ibkr: synthesized cancelled status for order missing from exchange",
		"orderID", orderID, "symbol", p.symbol,
		"retries", missCount, "elapsed", time.Since(cancelAt).Round(time.Second))
	return synthetic, nil
}

func isNotExist(err error) bool {
	return err == os.ErrNotExist
}

// Cancel cancels an open order by its server ID and records the cancel time
// so Get can synthesize a "Cancelled" update if the order drops off the
// live orders list before goWatchOrders observes its cancelled status.
func (p *Product) Cancel(ctx context.Context, serverID string) error {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return err
	}
	if err := p.client.CancelOrder(ctx, orderID); err != nil {
		return err
	}
	p.pendingCancels.Store(orderID, &cancelEntry{at: time.Now()})
	return nil
}

// goWatchOrderUpdates subscribes to the per-symbol orders topic published by
// goWatchOrders in the client. For each incoming order whose cOID matches a
// known clientID, it updates the cached order snapshot in clientIDStatusMap.
func (p *Product) goWatchOrderUpdates(ctx context.Context) {
	defer p.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("ibkr: CAUGHT PANIC in goWatchOrderUpdates", "panic", r, "symbol", p.symbol)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	sub, err := topic.Subscribe(p.client.GetSymbolOrdersTopic(p.symbol), 0, true)
	if err != nil {
		slog.Error("ibkr: could not subscribe to order updates topic", "symbol", p.symbol, "err", err)
		return
	}
	defer sub.Close()

	stopf := context.AfterFunc(ctx, sub.Close)
	defer stopf()

	for ctx.Err() == nil {
		order, err := sub.Receive()
		if err != nil {
			return
		}
		cid := order.ClientID()
		if cid == uuid.Nil {
			// Order was not placed by this bot (no valid cOID); skip it.
			continue
		}
		cstatus, ok := p.clientIDStatusMap.Load(cid)
		if !ok {
			continue
		}
		cstatus.mu.Lock()
		cstatus.order = order
		cstatus.err = nil // Clear any cached PlaceOrder error — the order was confirmed by the gateway.
		cstatus.mu.Unlock()

		if order.IsDone() {
			p.exchange.markFillObserved(ctx, order)

			if p.onBuyFill != nil && order.OrderSide() == "BUY" && order.FilledQty.IsPositive() {
				if _, alreadyNotified := p.notifiedFills.LoadOrStore(order.OrderID, struct{}{}); !alreadyNotified {
					p.onBuyFill(ctx, "STK", p.symbol, order.FilledQty, order.AvgFillPrice)
				}
			}
		}
	}
}
