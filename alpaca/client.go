// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"

	alpacaclient "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
)

type Client struct {
	opts         Options
	alpacaClient *alpacaclient.Client
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

	return &Client{
		opts:         *opts,
		alpacaClient: alpacaClient,
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
