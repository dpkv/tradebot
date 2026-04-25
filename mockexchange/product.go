package mockexchange

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bvk/tradebot/datafeed"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// limitOrder holds an open limit order, its target price, and the amount
// reserved from available balance when the order was placed.
type limitOrder struct {
	order      *exchange.SimpleOrder
	limitPrice decimal.Decimal
	size       decimal.Decimal
	reserved   decimal.Decimal // amount deducted from available at placement time
}

// Product is a simulated exchange product. Balance state lives entirely in the
// Exchange; Product calls reserve/release/settle for all fund movements.
type Product struct {
	def      *gobs.Product
	feeRate  decimal.Decimal
	ex       *Exchange

	mu       sync.Mutex
	orders   map[string]*limitOrder
	lastTick datafeed.Tick

	priceTopic *topic.Topic[exchange.PriceUpdate]
	orderTopic *topic.Topic[exchange.OrderUpdate]
}

var _ exchange.Product = (*Product)(nil)

var serverIDCounter atomic.Uint64

func newProduct(def *gobs.Product, feeRate decimal.Decimal, ex *Exchange) *Product {
	return &Product{
		def:        def,
		feeRate:    feeRate,
		ex:         ex,
		orders:     make(map[string]*limitOrder),
		priceTopic: topic.New[exchange.PriceUpdate](),
		orderTopic: topic.New[exchange.OrderUpdate](),
	}
}

func (p *Product) ProductID() string            { return p.def.ProductID }
func (p *Product) ExchangeName() string         { return "mock" }
func (p *Product) BaseMinSize() decimal.Decimal { return p.def.BaseMinSize }

func (p *Product) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.orders = nil
	return nil
}

func (p *Product) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	r, err := topic.Subscribe(p.priceTopic, 1, true /* includeLast */)
	if err != nil {
		return nil, fmt.Errorf("could not subscribe to price updates: %w", err)
	}
	return r, nil
}

func (p *Product) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	r, err := topic.Subscribe(p.orderTopic, 1, true /* includeLast */)
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

	return order, nil
}

func (p *Product) Get(_ context.Context, serverID string) (exchange.OrderDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	lo, ok := p.orders[serverID]
	if !ok {
		return nil, fmt.Errorf("order %q not found", serverID)
	}
	return lo.order, nil
}

func (p *Product) Cancel(_ context.Context, serverID string) error {
	p.mu.Lock()
	lo, ok := p.orders[serverID]
	if ok {
		lo.order.Done = true
		lo.order.Status = "CANCELLED"
		lo.order.DoneReason = "CANCELLED"
		delete(p.orders, serverID)
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
	}
	return nil
}

// ProcessTick is called by the Engine on each price tick. It publishes a price
// update and checks all open orders for fills.
func (p *Product) ProcessTick(tick datafeed.Tick) {
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
