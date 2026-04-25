package mockexchange

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bvk/tradebot/datafeed"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// limitOrder holds a limit order, its target price, and the amount reserved
// from available balance when the order was placed.
type limitOrder struct {
	order      *exchange.SimpleOrder
	limitPrice decimal.Decimal
	size       decimal.Decimal
	reserved   decimal.Decimal // amount deducted from available at placement time
}

// Product is a simulated exchange product. Balance state lives entirely in the
// Exchange; Product calls reserve/release/settle for all fund movements.
type Product struct {
	def     *gobs.Product
	feeRate decimal.Decimal
	ex      *Exchange

	mu         sync.Mutex
	orders     map[string]*limitOrder // open orders only
	doneOrders map[string]*limitOrder // filled or cancelled orders
	lastTick   datafeed.Tick

	priceTopic *topic.Topic[exchange.PriceUpdate]
	orderTopic *topic.Topic[exchange.OrderUpdate]

	// subscribedCh is closed when the first price subscriber registers.
	// The engine waits on this before sending any ticks.
	subscribedOnce sync.Once
	subscribedCh   chan struct{}

	// orderChanged receives a signal on every placeOrder or Cancel call so the
	// engine can wait for order-count quiescence after a fill.
	orderChanged chan struct{}
}

var _ exchange.Product = (*Product)(nil)

var serverIDCounter atomic.Uint64

func newProduct(def *gobs.Product, feeRate decimal.Decimal, ex *Exchange) *Product {
	return &Product{
		def:          def,
		feeRate:      feeRate,
		ex:           ex,
		orders:       make(map[string]*limitOrder),
		doneOrders:   make(map[string]*limitOrder),
		priceTopic:   topic.New[exchange.PriceUpdate](),
		orderTopic:   topic.New[exchange.OrderUpdate](),
		subscribedCh: make(chan struct{}),
		orderChanged: make(chan struct{}, 1),
	}
}

func (p *Product) ProductID() string            { return p.def.ProductID }
func (p *Product) ExchangeName() string         { return "mock" }
func (p *Product) BaseMinSize() decimal.Decimal { return p.def.BaseMinSize }

func (p *Product) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.orders = nil
	p.doneOrders = nil
	return nil
}

func (p *Product) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	r, err := topic.Subscribe(p.priceTopic, 0 /* unbounded */, true /* includeLast */)
	if err != nil {
		return nil, fmt.Errorf("could not subscribe to price updates: %w", err)
	}
	p.subscribedOnce.Do(func() { close(p.subscribedCh) })
	return r, nil
}

// WaitForSubscriber blocks until the first price subscriber registers or ctx
// is cancelled. The engine calls this before sending any ticks to ensure the
// strategy has set up its event loop.
func (p *Product) WaitForSubscriber(ctx context.Context) error {
	select {
	case <-p.subscribedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Product) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	r, err := topic.Subscribe(p.orderTopic, 0 /* unbounded */, true /* includeLast */)
	if err != nil {
		return nil, fmt.Errorf("could not subscribe to order updates: %w", err)
	}
	return r, nil
}

func (p *Product) LimitBuy(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	return p.placeOrder(ctx, "BUY", clientID, size, price)
}

func (p *Product) LimitSell(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	return p.placeOrder(ctx, "SELL", clientID, size, price)
}

func (p *Product) placeOrder(_ context.Context, side string, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	// BUY reserves quote (cost + fee); SELL reserves base (the asset being sold).
	var reserveCurrency string
	var reserved decimal.Decimal
	if strings.EqualFold(side, "buy") {
		reserveCurrency = p.def.QuoteCurrencyID
		reserved = size.Mul(price).Mul(decimal.NewFromInt(1).Add(p.feeRate))
	} else {
		reserveCurrency = p.def.BaseCurrencyID
		reserved = size
	}
	if err := p.ex.reserve(reserveCurrency, reserved); err != nil {
		return nil, err
	}

	serverID := fmt.Sprintf("mock-%d", serverIDCounter.Add(1))
	order, err := exchange.NewSimpleOrder(serverID, clientID, side)
	if err != nil {
		p.ex.release(reserveCurrency, reserved)
		return nil, err
	}

	p.mu.Lock()
	order.CreateTime = gobs.RemoteTime{Time: p.lastTick.Time}
	order.Status = "OPEN"
	p.orders[serverID] = &limitOrder{order: order, limitPrice: price, size: size, reserved: reserved}
	p.mu.Unlock()

	select {
	case p.orderChanged <- struct{}{}:
	default:
	}
	return order, nil
}

func (p *Product) Get(_ context.Context, serverID string) (exchange.OrderDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if lo, ok := p.orders[serverID]; ok {
		return lo.order, nil
	}
	if lo, ok := p.doneOrders[serverID]; ok {
		return lo.order, nil
	}
	return nil, fmt.Errorf("order %q not found", serverID)
}

func (p *Product) Cancel(_ context.Context, serverID string) error {
	p.mu.Lock()
	lo, ok := p.orders[serverID]
	if ok {
		lo.order.Done = true
		lo.order.Status = "CANCELLED"
		lo.order.DoneReason = "CANCELLED"
		delete(p.orders, serverID)
		p.doneOrders[serverID] = lo
	}
	p.mu.Unlock()

	if ok {
		// Restore reserved funds before notifying strategy.
		if strings.EqualFold(lo.order.Side, "buy") {
			p.ex.release(p.def.QuoteCurrencyID, lo.reserved)
		} else {
			p.ex.release(p.def.BaseCurrencyID, lo.reserved)
		}
		p.orderTopic.Send(lo.order)
		select {
		case p.orderChanged <- struct{}{}:
		default:
		}
	}
	return nil
}

// ProcessTick is called by the Engine on each price tick. It publishes a price
// update, checks all open orders for fills, and returns the number of fills.
func (p *Product) ProcessTick(tick datafeed.Tick) int {
	p.priceTopic.Send(&exchange.SimpleTicker{
		ServerTime: exchange.RemoteTime{Time: tick.Time},
		Price:      tick.Price,
	})

	p.mu.Lock()
	p.lastTick = tick
	var toFill []*limitOrder
	for _, lo := range p.orders {
		if shouldFill(lo.order.Side, lo.limitPrice, tick.Price) {
			toFill = append(toFill, lo)
		}
	}
	for _, lo := range toFill {
		delete(p.orders, lo.order.ServerOrderID)
		p.doneOrders[lo.order.ServerOrderID] = lo
	}
	p.mu.Unlock()

	for _, lo := range toFill {
		fee := lo.size.Mul(lo.limitPrice).Mul(p.feeRate)
		p.ex.settle(lo.order.Side, p.def.BaseCurrencyID, p.def.QuoteCurrencyID, lo.size, lo.limitPrice, fee)
		lo.order.FilledSize = lo.size
		lo.order.FilledPrice = lo.limitPrice
		lo.order.Fee = fee
		lo.order.Done = true
		lo.order.Status = "FILLED"
		lo.order.DoneReason = "FILLED"
		lo.order.FinishTime = gobs.RemoteTime{Time: tick.Time}
		p.orderTopic.Send(lo.order)
	}
	return len(toFill)
}

// openOrderCount returns the number of open (not yet filled or cancelled) orders.
func (p *Product) openOrderCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.orders)
}

// waitForStableOrders blocks until no order mutations have occurred for one
// millisecond, indicating the strategy has finished placing reactive orders.
func (p *Product) waitForStableOrders(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.orderChanged:
			// order mutated; reset the quiet window
		case <-time.After(100 * time.Millisecond):
			return nil
		}
	}
}

// shouldFill returns true when a limit order at limitPrice should fill at tickPrice.
// BUY fills when market price drops to or below the limit.
// SELL fills when market price rises to or above the limit.
func shouldFill(side string, limitPrice, tickPrice decimal.Decimal) bool {
	if strings.EqualFold(side, "buy") {
		return tickPrice.LessThanOrEqual(limitPrice)
	}
	return tickPrice.GreaterThanOrEqual(limitPrice)
}
