// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"
)

type Client struct {
	opts Options
}

func NewClient(ctx context.Context, key, secret string, opts *Options) (*Client, error) {
	if opts == nil {
		opts = new(Options)
		opts.setDefaults(true) // paper trading is the default for alpaca
	}
	if err := opts.Check(); err != nil {
		return nil, err
	}
	return &Client{
		opts: *opts,
	}, nil
}

func (c *Client) Close() error {
	return nil
}
