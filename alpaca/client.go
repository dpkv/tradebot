// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"
	"log/slog"
	"sync"

	alpacaclient "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	"github.com/bvk/tradebot/alpaca/internal"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/syncmap"
	"github.com/visvasity/topic"
)

type Client struct {
	opts             Options
	key              string
	secret           string
	alpacaClient     *alpacaclient.Client
	marketdataClient *marketdata.Client

	// Streaming client for real-time data
	streamClient *stream.StocksClient
	streamMu     sync.Mutex
	streamCtx    context.Context
	streamCancel context.CancelFunc

	// Map of symbol to price topic for streaming price updates
	priceTopicMap syncmap.Map[string, *topic.Topic[exchange.PriceUpdate]]
}

func NewClient(ctx context.Context, key, secret string, opts *Options) (*Client, error) {
	if opts == nil {
		opts = new(Options)
		opts.setDefaults(true) // paper trading is the default for alpaca
	}
	if err := opts.Check(); err != nil {
		return nil, err
	}

	// Create client options for v3 API
	clientOpts := alpacaclient.ClientOpts{
		APIKey:    key,
		APISecret: secret,
		BaseURL:   opts.TradeURL,
	}

	alpacaClient := alpacaclient.NewClient(clientOpts)

	// Create marketdata client options
	marketdataOpts := marketdata.ClientOpts{
		APIKey:    key,
		APISecret: secret,
		BaseURL:   opts.DataURL,
	}
	marketdataClient := marketdata.NewClient(marketdataOpts)

	return &Client{
		opts:             *opts,
		key:              key,
		secret:           secret,
		alpacaClient:     alpacaClient,
		marketdataClient: marketdataClient,
	}, nil
}

func (c *Client) Close() error {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()

	if c.streamCancel != nil {
		c.streamCancel()
	}
	return nil
}

// getOrCreatePriceTopic returns the price topic for a symbol, creating it if necessary
func (c *Client) getOrCreatePriceTopic(symbol string) *topic.Topic[exchange.PriceUpdate] {
	if t, ok := c.priceTopicMap.Load(symbol); ok {
		return t
	}
	t := topic.New[exchange.PriceUpdate]()
	c.priceTopicMap.Store(symbol, t)
	return t
}

// ensureStreamConnected ensures the stream client is connected
func (c *Client) ensureStreamConnected(ctx context.Context) error {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()

	if c.streamClient != nil {
		slog.Debug("alpaca stream client already connected")
		return nil
	}

	slog.Info("connecting to alpaca stream", "feed", c.opts.StreamFeed, "url", c.opts.StreamURL)

	// Create stream client with credentials
	c.streamClient = stream.NewStocksClient(
		c.opts.StreamFeed,
		stream.WithCredentials(c.key, c.secret),
		stream.WithBaseURL(c.opts.StreamURL),
		stream.WithConnectCallback(func() {
			slog.Info("alpaca stream connection established")
		}),
		stream.WithDisconnectCallback(func() {
			slog.Warn("alpaca stream connection lost")
		}),
	)

	// Create a context for the stream
	c.streamCtx, c.streamCancel = context.WithCancel(context.Background())

	// Connect to the stream
	slog.Info("attempting to connect to alpaca stream...")
	if err := c.streamClient.Connect(c.streamCtx); err != nil {
		slog.Error("failed to connect to alpaca stream", "err", err)
		c.streamClient = nil
		c.streamCancel()
		return err
	}

	slog.Info("alpaca stream client connected successfully", "feed", c.opts.StreamFeed)
	return nil
}

// SubscribeToTrades subscribes to trade updates for a symbol
func (c *Client) SubscribeToTrades(ctx context.Context, symbol string) (*topic.Topic[exchange.PriceUpdate], error) {
	if err := c.ensureStreamConnected(ctx); err != nil {
		slog.Error("failed to connect to alpaca stream", "symbol", symbol, "err", err)
		return nil, err
	}

	priceTopic := c.getOrCreatePriceTopic(symbol)

	tradeCount := 0
	// Subscribe to trades for this symbol
	err := c.streamClient.SubscribeToTrades(func(trade stream.Trade) {
		tradeCount++
		if tradeCount <= 5 || tradeCount%100 == 0 {
			slog.Info("alpaca trade received",
				"symbol", trade.Symbol,
				"price", trade.Price,
				"size", trade.Size,
				"timestamp", trade.Timestamp,
				"tradeCount", tradeCount)
		}
		if trade.Symbol == symbol {
			update := &internal.TradeUpdate{Trade: trade}
			priceTopic.Send(update)
		}
	}, symbol)
	if err != nil {
		slog.Error("failed to subscribe to alpaca trades", "symbol", symbol, "err", err)
		return nil, err
	}

	slog.Info("subscribed to alpaca trades", "symbol", symbol, "feed", c.opts.StreamFeed)
	return priceTopic, nil
}

// UnsubscribeFromTrades unsubscribes from trade updates for a symbol
func (c *Client) UnsubscribeFromTrades(ctx context.Context, symbol string) error {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()

	if c.streamClient == nil {
		return nil
	}

	return c.streamClient.UnsubscribeFromTrades(symbol)
}

// GetAsset returns an asset for the given symbol.
func (c *Client) GetAsset(ctx context.Context, symbol string) (*alpacaclient.Asset, error) {
	return c.alpacaClient.GetAsset(symbol)
}

// GetAssets returns the list of assets, optionally filtered by status, asset class, or exchange.
func (c *Client) GetAssets(ctx context.Context, status *string, assetClass *string, exchange *string) ([]alpacaclient.Asset, error) {
	req := alpacaclient.GetAssetsRequest{}
	if status != nil {
		req.Status = *status
	}
	if assetClass != nil {
		req.AssetClass = *assetClass
	}
	if exchange != nil {
		req.Exchange = *exchange
	}
	return c.alpacaClient.GetAssets(req)
}

// GetSnapshot returns a snapshot of market data for the given symbol, including
// the latest trade, quote, and bar data.
func (c *Client) GetSnapshot(ctx context.Context, symbol string) (*marketdata.Snapshot, error) {
	req := marketdata.GetSnapshotRequest{}
	return c.marketdataClient.GetSnapshot(symbol, req)
}

// GetAccount returns the account information, including cash balance, buying power, and portfolio value.
func (c *Client) GetAccount(ctx context.Context) (*alpacaclient.Account, error) {
	return c.alpacaClient.GetAccount()
}

// PlaceOrder places a new order with the given request.
func (c *Client) PlaceOrder(ctx context.Context, req alpacaclient.PlaceOrderRequest) (*internal.Order, error) {
	order, err := c.alpacaClient.PlaceOrder(req)
	if err != nil {
		return nil, err
	}
	return &internal.Order{Order: order}, nil
}

// GetOrder retrieves an order by its server order ID.
func (c *Client) GetOrder(ctx context.Context, orderID string) (*internal.Order, error) {
	order, err := c.alpacaClient.GetOrder(orderID)
	if err != nil {
		return nil, err
	}
	return &internal.Order{Order: order}, nil
}

// GetOrderByClientOrderID retrieves an order by its client order ID.
func (c *Client) GetOrderByClientOrderID(ctx context.Context, clientOrderID string) (*internal.Order, error) {
	order, err := c.alpacaClient.GetOrderByClientOrderID(clientOrderID)
	if err != nil {
		return nil, err
	}
	return &internal.Order{Order: order}, nil
}

// CancelOrder cancels an order by its server order ID.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	return c.alpacaClient.CancelOrder(orderID)
}
