// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/bvk/tradebot/syncmap"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// Exchange implements exchange.Exchange for Interactive Brokers via the Client
// Portal Gateway.
type Exchange struct {
	client *Client

	// conidCache maps stock symbol → conid. Populated lazily on first
	// OpenSpotProduct or GetSpotProduct call for each symbol.
	conidMu    sync.Mutex
	conidCache map[string]int

	productMap syncmap.Map[string, *Product]
}

var _ exchange.Exchange = &Exchange{}

// NewExchange creates a new Exchange, starts the background client goroutines,
// and returns it. The caller must call Close() when done.
func NewExchange(ctx context.Context, creds *Credentials, opts *Options) (_ *Exchange, status error) {
	client, err := New(ctx, creds, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if status != nil {
			client.Close()
		}
	}()

	v := &Exchange{
		client:     client,
		conidCache: make(map[string]int),
	}
	return v, nil
}

// Close stops the background client goroutines.
func (v *Exchange) Close() error {
	if err := v.client.Close(); err != nil {
		slog.Error("ibkr: could not close client (ignored)", "err", err)
	}
	return nil
}

func (v *Exchange) ExchangeName() string {
	return "ibkr"
}

// CanDedupOnClientUUID returns true. IBKR's cOID field is maintained as
// unique by the gateway — placing an order with a duplicate cOID returns the
// existing order instead of creating a new one.
func (v *Exchange) CanDedupOnClientUUID() bool {
	return true
}

// GetBalanceUpdates returns a receiver that delivers balance updates whenever
// the account's available funds change.
func (v *Exchange) GetBalanceUpdates() (*topic.Receiver[exchange.BalanceUpdate], error) {
	fn := func(b *internal.Balance) exchange.BalanceUpdate { return b }
	return topic.SubscribeFunc(v.client.balanceUpdatesTopic, fn, 0, true)
}

// OpenSpotProduct resolves the conid for productID (a stock ticker), starts
// price polling for it, and returns a cached Product instance.
func (v *Exchange) OpenSpotProduct(ctx context.Context, productID string) (exchange.Product, error) {
	if p, ok := v.productMap.Load(productID); ok {
		return p, nil
	}

	conid, err := v.resolveConid(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("ibkr: could not resolve conid for %q: %w", productID, err)
	}

	// Start price polling for this symbol if not already running.
	v.client.WatchSymbol(productID, conid)

	p, err := NewProduct(ctx, v.client, productID, conid)
	if err != nil {
		return nil, err
	}
	if existing, loaded := v.productMap.LoadOrStore(productID, p); loaded {
		p.Close()
		p = existing
	}
	return p, nil
}

// GetSpotProduct resolves product metadata for base/quote. Only "USD" is
// supported as the quote currency. Returns a *gobs.Product with the current
// mid-price from a snapshot.
func (v *Exchange) GetSpotProduct(ctx context.Context, base, quote string) (*gobs.Product, error) {
	if quote != "USD" {
		return nil, fmt.Errorf("ibkr: only USD is supported as quote currency")
	}

	conid, err := v.resolveConid(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("ibkr: could not resolve conid for %q: %w", base, err)
	}

	price, err := v.fetchMidPrice(ctx, conid)
	if err != nil {
		// Non-fatal — return the product without a price.
		slog.Warn("ibkr: could not fetch price for GetSpotProduct (returning zero)", "symbol", base, "err", err)
		price = decimal.Zero
	}

	p := &gobs.Product{
		ProductID:       base,
		Status:          "online",
		Price:           price,
		BaseMinSize:     decimal.NewFromInt(1), // IBKR minimum: 1 share
		BaseCurrencyID:  base,
		QuoteCurrencyID: quote,
	}
	return p, nil
}

// GetOrder fetches the current state of an order by server ID. It polls the
// full orders list and finds the matching entry. Returns os.ErrNotExist if
// the order is not found (it may have been purged from the gateway's live
// order cache after completion).
func (v *Exchange) GetOrder(ctx context.Context, productID string, serverID string) (exchange.OrderDetail, error) {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("ibkr: invalid serverID %q: %w", serverID, err)
	}

	orders, err := v.client.GetOrders(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			return o, nil
		}
	}
	return nil, fmt.Errorf("ibkr: order %q not found in live orders: %w", serverID, os.ErrNotExist)
}

// resolveConid returns the conid for a symbol, using the cache to avoid
// repeated API calls.
func (v *Exchange) resolveConid(ctx context.Context, symbol string) (int, error) {
	v.conidMu.Lock()
	if conid, ok := v.conidCache[symbol]; ok {
		v.conidMu.Unlock()
		return conid, nil
	}
	v.conidMu.Unlock()

	conid, err := v.client.ResolveConid(ctx, symbol)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return 0, fmt.Errorf("ibkr: symbol %q not found: %w", symbol, os.ErrNotExist)
		}
		return 0, err
	}

	v.conidMu.Lock()
	v.conidCache[symbol] = conid
	v.conidMu.Unlock()
	return conid, nil
}

func (v *Exchange) SupportsOptions() bool { return true }

func (v *Exchange) GetOptionsChain(_ context.Context, _ string) ([]*gobs.OptionContract, error) {
	return nil, errors.New("not yet implemented")
}

func (v *Exchange) GetOptionsProduct(_ context.Context, _ string) (*gobs.OptionContract, error) {
	return nil, errors.New("not yet implemented")
}

func (v *Exchange) OpenOptionsProduct(_ context.Context, _ string) (exchange.OptionsProduct, error) {
	return nil, errors.New("not yet implemented")
}

// fetchMidPrice makes a one-off snapshot request for the given conid and
// returns the mid-price. Used by GetSpotProduct.
func (v *Exchange) fetchMidPrice(ctx context.Context, conid int) (decimal.Decimal, error) {
	path := fmt.Sprintf("/v1/api/iserver/marketdata/snapshot?conids=%d&fields=31,84,86", conid)
	var snapshots []*internal.APISnapshot
	if err := v.client.doRequest(ctx, "GET", path, nil, &snapshots); err != nil {
		return decimal.Zero, err
	}
	for _, snap := range snapshots {
		q := internal.NewQuoteFromAPI(snap)
		if q != nil {
			price, _ := q.PricePoint()
			return price, nil
		}
	}
	return decimal.Zero, fmt.Errorf("ibkr: snapshot returned no price data for conid %d", conid)
}
