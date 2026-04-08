// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// Options configures the behavior of the IBKR exchange client.
type Options struct {
	// HttpClientTimeout is the timeout for individual HTTP requests to the
	// CP Gateway. The gateway is local but can be slow when it is busy
	// proxying requests to IBKR's servers.
	// Default: 10s.
	HttpClientTimeout time.Duration

	// TickleInterval is how often the background goroutine sends a POST
	// /v1/api/tickle to keep the gateway session alive. The gateway logs out
	// an idle session after 60 seconds, so this must be well under 60s.
	// Default: 55s.
	TickleInterval time.Duration

	// PollOrdersInterval is how often open orders are polled from the gateway.
	// Default: 10s.
	PollOrdersInterval time.Duration

	// PollPricesInterval is how often market data snapshots are polled for
	// each subscribed product.
	// Default: 5s.
	PollPricesInterval time.Duration

	// PollBalancesInterval is how often the account portfolio summary is
	// polled to refresh the balance.
	// Default: 30s.
	PollBalancesInterval time.Duration

	// OnBuyFill, if non-nil, is called once when a BUY order reaches Filled
	// status. productType is "STK", "CALL", or "PUT". Called from a
	// background goroutine; must not block.
	OnBuyFill func(ctx context.Context, productType, symbol string, filledQty, avgPrice decimal.Decimal)
}

func (o *Options) setDefaults() {
	if o.HttpClientTimeout == 0 {
		o.HttpClientTimeout = 10 * time.Second
	}
	if o.TickleInterval == 0 {
		o.TickleInterval = 55 * time.Second
	}
	if o.PollOrdersInterval == 0 {
		o.PollOrdersInterval = 10 * time.Second
	}
	if o.PollPricesInterval == 0 {
		o.PollPricesInterval = 5 * time.Second
	}
	if o.PollBalancesInterval == 0 {
		o.PollBalancesInterval = 30 * time.Second
	}
}

// Check validates the options. All fields have defaults so nothing is
// mandatory; mandatory values live in Credentials.
func (o *Options) Check() error {
	return nil
}
