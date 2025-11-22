// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"

	alpacaclient "github.com/alpacahq/alpaca-trade-api-go/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/common"
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

	// Set the base URL for the alpaca client
	alpacaclient.SetBaseUrl(opts.TradeURL)

	// Create credentials
	credentials := &common.APIKey{
		ID:     key,
		Secret: secret,
	}

	alpacaClient := alpacaclient.NewClient(credentials)

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
