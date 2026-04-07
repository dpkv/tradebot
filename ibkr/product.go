// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
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
func NewProduct(ctx context.Context, exch *Exchange, symbol string, conid int, outsideRTH bool) (*Product, error) {
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	p := &Product{
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		exchange:   exch,
		client:     exch.client,
		symbol:     symbol,
		conid:      conid,
		outsideRTH: outsideRTH,
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

func (p *Product) placeOrder(ctx context.Context, clientID uuid.UUID, side string, size, price decimal.Decimal) (_ exchange.Order, status error) {
	// Check persisted orders first — crash recovery path.
	if order, ok := p.exchange.findOrder(clientID); ok {
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
	orders, err := p.client.GetOrders(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			if cid := o.ClientID(); cid != uuid.Nil {
				cstatus, _ := p.clientIDStatusMap.LoadOrStore(cid, newClientIDStatus())
				cstatus.mu.Lock()
				cstatus.order = o
				cstatus.mu.Unlock()
			}
			return o, nil
		}
	}

	// Last resort: check persisted orders (filled orders drop off the gateway cache).
	if o, ok := p.exchange.findOrderByServerID(orderID); ok {
		return o, nil
	}
	return nil, os.ErrNotExist
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

	return p.synthesizeCancelled(orderID, count, cancelAt)
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
		cstatus.mu.Unlock()

		if order.IsDone() {
			p.exchange.persistOrder(ctx, order)
		}
	}
}
