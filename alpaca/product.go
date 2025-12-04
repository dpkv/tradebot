// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	alpacaclient "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/bvk/tradebot/alpaca/internal"
	"github.com/bvk/tradebot/exchange"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

type Product struct {
	client     *Client
	exchange   *Exchange
	symbol     string
	asset      *alpacaclient.Asset
	orderTopic *topic.Topic[exchange.OrderUpdate]
	priceTopic *topic.Topic[exchange.PriceUpdate]
}

var _ exchange.Product = &Product{}

func (p *Product) Close() error {
	// TODO: Cleanup if needed
	return nil
}

func (p *Product) ProductID() string {
	return p.symbol
}

func (p *Product) ExchangeName() string {
	return "alpaca"
}

func (p *Product) BaseMinSize() decimal.Decimal {
	// Alpaca doesn't provide min size in Asset directly
	// For stocks, typically 1 share is the minimum
	// For fractional shares, it might be different
	// Using 0.0001 as a reasonable default for fractional shares
	// This should be configurable or fetched from account/product info
	// TODO: Implement this
	return decimal.NewFromFloat(0.0001)
}

func (p *Product) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	slog.Info("GetPriceUpdates called", "symbol", p.symbol, "hasPriceTopic", p.priceTopic != nil)
	// Subscribe to trades for this symbol if not already subscribed
	if p.priceTopic == nil {
		slog.Info("subscribing to trades for price updates", "symbol", p.symbol)
		priceTopic, err := p.client.SubscribeToTrades(context.Background(), p.symbol)
		if err != nil {
			slog.Error("failed to subscribe to trades for price updates", "symbol", p.symbol, "err", err)
			return nil, fmt.Errorf("could not subscribe to trades for %s: %w", p.symbol, err)
		}
		p.priceTopic = priceTopic
		slog.Info("successfully subscribed to trades for price updates", "symbol", p.symbol)
	}
	return topic.Subscribe(p.priceTopic, 1, true /* includeLast */)
}

func (p *Product) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	return topic.Subscribe(p.orderTopic, 1, true /* includeLast */)
}

func (p *Product) LimitBuy(ctx context.Context, clientOrderID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	if size.LessThan(p.BaseMinSize()) {
		return nil, fmt.Errorf("min size is %s: %w", p.BaseMinSize(), os.ErrInvalid)
	}

	// Check if this is a retry request for the clientOrderID
	if order, ok := p.exchange.recreateOldOrder(clientOrderID); ok {
		// internal.Order implements both exchange.Order and exchange.OrderUpdate
		if alpacaOrder, ok := order.(*internal.Order); ok {
			p.orderTopic.Send(alpacaOrder)
		}
		return order, nil
	}

	req := alpacaclient.PlaceOrderRequest{
		Symbol:        p.symbol,
		Qty:           &size,
		Side:          alpacaclient.Buy,
		Type:          alpacaclient.Limit,
		TimeInForce:   alpacaclient.Day,
		LimitPrice:    &price,
		ClientOrderID: clientOrderID.String(),
		ExtendedHours: false,
	}

	order, err := p.client.PlaceOrder(ctx, req)
	if err != nil {
		return nil, err
	}

	// Store the order in the exchange's clientOrderIDMap
	p.exchange.clientOrderIDMap.Store(clientOrderID, order)

	// Send order update
	p.orderTopic.Send(order)

	return order, nil
}

func (p *Product) LimitSell(ctx context.Context, clientOrderID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
	if size.LessThan(p.BaseMinSize()) {
		return nil, fmt.Errorf("min size is %s: %w", p.BaseMinSize(), os.ErrInvalid)
	}

	// Check if this is a retry request for the clientOrderID
	if order, ok := p.exchange.recreateOldOrder(clientOrderID); ok {
		// internal.Order implements both exchange.Order and exchange.OrderUpdate
		if alpacaOrder, ok := order.(*internal.Order); ok {
			p.orderTopic.Send(alpacaOrder)
		}
		return order, nil
	}

	req := alpacaclient.PlaceOrderRequest{
		Symbol:        p.symbol,
		Qty:           &size,
		Side:          alpacaclient.Sell,
		Type:          alpacaclient.Limit,
		TimeInForce:   alpacaclient.Day,
		LimitPrice:    &price,
		ClientOrderID: clientOrderID.String(),
		ExtendedHours: false,
	}

	order, err := p.client.PlaceOrder(ctx, req)
	if err != nil {
		return nil, err
	}

	// Store the order in the exchange's clientOrderIDMap
	p.exchange.clientOrderIDMap.Store(clientOrderID, order)

	// Send order update
	p.orderTopic.Send(order)

	return order, nil
}

func (p *Product) Get(ctx context.Context, serverOrderID string) (exchange.OrderDetail, error) {
	return p.exchange.GetOrder(ctx, p.symbol, serverOrderID)
}

func (p *Product) Cancel(ctx context.Context, serverOrderID string) error {
	return p.client.CancelOrder(ctx, serverOrderID)
}
