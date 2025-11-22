// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"

	alpacaclient "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
)

type Client struct {
	opts             Options
	alpacaClient     *alpacaclient.Client
	marketdataClient *marketdata.Client
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
		alpacaClient:     alpacaClient,
		marketdataClient: marketdataClient,
	}, nil
}

func (c *Client) Close() error {
	return nil
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
