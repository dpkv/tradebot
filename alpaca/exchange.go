// Copyright (c) 2025 Deepak Vankadaru

package alpaca

import (
	"context"
	"errors"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
)

type Exchange struct {
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
	return &Exchange{}, nil
}

func (e *Exchange) Close() error {
	// TODO: Implement this
	return nil
}

func (e *Exchange) ExchangeName() string {
	return "alpaca"
}

func (e *Exchange) CanDedupOnClientUUID() bool {
	// TODO: Implement this
	return false
}

func (e *Exchange) OpenSpotProduct(ctx context.Context, productID string) (exchange.Product, error) {
	return nil, errors.New("not implemented")
}

func (e *Exchange) GetSpotProduct(ctx context.Context, base, quote string) (*gobs.Product, error) {
	return nil, errors.New("not implemented")
}

func (e *Exchange) GetOrder(ctx context.Context, productID string, serverID string) (exchange.OrderDetail, error) {
	return nil, errors.New("not implemented")
}
