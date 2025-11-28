// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/syncmap"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

type Exchange struct {
	client *Client

	productMap syncmap.Map[string, *Product]

	// clientOrderIDMap holds client-order-id to exchange.Order mapping for all
	// known orders. This is used for deduplication when the same clientOrderID
	// is used again.
	clientOrderIDMap syncmap.Map[uuid.UUID, exchange.Order]
}

var _ exchange.Exchange = &Exchange{}

func NewExchange(ctx context.Context, key, secret string, paperTrading bool, opts *Options) (_ *Exchange, status error) {
	if opts == nil {
		opts = new(Options)
	}
	opts.setDefaults(paperTrading)
	if err := opts.Check(); err != nil {
		return nil, err
	}

	client, err := NewClient(ctx, key, secret, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if status != nil {
			client.Close()
		}
	}()

	return &Exchange{
		client: client,
	}, nil
}

func (e *Exchange) Close() error {
	if e.client != nil {
		return e.client.Close()
	}
	return nil
}

func (e *Exchange) ExchangeName() string {
	return "alpaca"
}

func (e *Exchange) CanDedupOnClientUUID() bool {
	// Alpaca supports client_order_id and allows retrieving orders by it,
	// so we can deduplicate on client UUID
	// TODO: Testing pending
	return true
}

func (e *Exchange) GetBalanceUpdates() (*topic.Receiver[exchange.BalanceUpdate], error) {
	return nil, errors.New("not implemented")
}

func (e *Exchange) OpenSpotProduct(ctx context.Context, productID string) (exchange.Product, error) {
	// Check if product is already open
	if p, ok := e.productMap.Load(productID); ok {
		return p, nil
	}

	// Get asset information
	asset, err := e.client.GetAsset(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("could not get asset %q: %w", productID, err)
	}

	// Check if asset is tradable
	if !asset.Tradable {
		return nil, fmt.Errorf("asset %q is not tradable", productID)
	}

	p := &Product{
		client:     e.client,
		exchange:   e,
		symbol:     productID,
		asset:      asset,
		orderTopic: topic.New[exchange.OrderUpdate](),
	}

	e.productMap.Store(productID, p)
	return p, nil
}

func (e *Exchange) GetSpotProduct(ctx context.Context, base, quote string) (*gobs.Product, error) {
	// For Alpaca, productID is just the symbol (e.g., "AAPL" for stocks)
	// We'll use the base symbol as the productID
	productID := base

	asset, err := e.client.GetAsset(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("could not get asset %q: %w", productID, err)
	}

	// Alpaca doesn't provide all the same fields as Coinbase
	// We'll create a minimal Product with available information
	product := &gobs.Product{
		ProductID: productID,
		Status:    string(asset.Status),
		// Alpaca doesn't provide price in asset info, would need to fetch separately
		BaseName:          asset.Name,
		BaseDisplaySymbol: asset.Symbol,
		// Min size defaults to 1 share for stocks, but could be fractional
		BaseMinSize:        decimal.NewFromInt(1),
		BaseIncrement:      decimal.NewFromFloat(0.0001), // Typical fractional share increment
		QuoteName:          quote,
		QuoteDisplaySymbol: quote,
	}

	return product, nil
}

// recreateOldOrder checks if an order with the given clientOrderID already exists
// and returns it if found. This is used for deduplication.
func (e *Exchange) recreateOldOrder(clientOrderID uuid.UUID) (exchange.Order, bool) {
	old, ok := e.clientOrderIDMap.Load(clientOrderID)
	if !ok {
		// Try to fetch from Alpaca by client order ID
		order, err := e.client.GetOrderByClientOrderID(context.Background(), clientOrderID.String())
		if err != nil {
			return nil, false
		}
		e.clientOrderIDMap.Store(clientOrderID, order)
		return order, true
	}
	slog.Debug("recreate order request for already used client-id is short-circuited",
		"clientID", clientOrderID, "serverID", old.ServerID())
	return old, true
}

func (e *Exchange) GetOrder(ctx context.Context, productID string, serverID string) (exchange.OrderDetail, error) {
	order, err := e.client.GetOrder(ctx, serverID)
	if err != nil {
		return nil, err
	}

	// Update clientOrderIDMap if we have a client order ID
	if order.ClientID() != uuid.Nil {
		e.clientOrderIDMap.Store(order.ClientID(), order)
	}

	return order, nil
}
