// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"

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

	client *Client

	symbol string
	conid  int

	// clientIDStatusMap maps the caller's UUID to the status of the order
	// placed with that UUID as cOID. Populated by LimitBuy/LimitSell and
	// updated by goWatchOrderUpdates.
	clientIDStatusMap syncmap.Map[uuid.UUID, *clientIDStatus]
}

var _ exchange.Product = &Product{}

// NewProduct creates a Product for the given symbol and starts its background
// goroutine. WatchSymbol must have been called on the client before NewProduct
// so that the price and order topics exist.
func NewProduct(ctx context.Context, client *Client, symbol string, conid int) (*Product, error) {
	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	p := &Product{
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		client:     client,
		symbol:     symbol,
		conid:      conid,
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

	serverOrderID, err := p.client.PlaceOrder(ctx, p.symbol, p.conid, side, size, price, clientID.String())
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

// Get returns the current state of an order by server ID. It first checks the
// local clientIDStatusMap, then falls back to polling the exchange.
func (p *Product) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return nil, err
	}

	// Fast path: find in clientIDStatusMap.
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

	// Slow path: poll the exchange.
	orders, err := p.client.GetOrders(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			return o, nil
		}
	}
	return nil, os.ErrNotExist
}

// Cancel cancels an open order by its server ID.
func (p *Product) Cancel(ctx context.Context, serverID string) error {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return err
	}
	return p.client.CancelOrder(ctx, orderID)
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
	}
}
