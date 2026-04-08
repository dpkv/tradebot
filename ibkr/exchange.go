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
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// Exchange implements exchange.Exchange for Interactive Brokers via the Client
// Portal Gateway.
type Exchange struct {
	client *Client

	datastore *Datastore

	// orderMap holds all known orders keyed by client order UUID. Populated
	// from the datastore on startup and updated as orders complete.
	orderMap syncmap.Map[uuid.UUID, *internal.Order]

	// serverIDMap is a secondary index keyed by IBKR numeric order ID, used
	// by Product.Get to find old filled orders after a restart.
	serverIDMap syncmap.Map[int64, *internal.Order]

	// conidCache maps stock symbol → conid. Populated lazily on first
	// OpenSpotProduct or GetSpotProduct call for each symbol.
	conidMu    sync.Mutex
	conidCache map[string]int

	productMap syncmap.Map[string, *Product]

	onBuyFill func(ctx context.Context, productType, symbol string, filledQty, avgPrice decimal.Decimal)
}

var _ exchange.Exchange = &Exchange{}

// NewExchange creates a new Exchange, starts the background client goroutines,
// and returns it. The caller must call Close() when done.
func NewExchange(ctx context.Context, db kv.Database, creds *Credentials, opts *Options) (_ *Exchange, status error) {
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
		onBuyFill:  opts.OnBuyFill,
	}

	if db != nil {
		v.datastore = NewDatastore(db)

		// Repopulate orderMap from persisted orders for crash recovery.
		orders, err := v.datastore.LoadOrders(ctx)
		if err != nil {
			return nil, fmt.Errorf("ibkr: could not load persisted orders: %w", err)
		}
		for _, o := range orders {
			if id, err := uuid.Parse(o.ClientOrderID); err == nil {
				v.orderMap.Store(id, o)
			}
			v.serverIDMap.Store(o.OrderID, o)
		}
		slog.Info("ibkr: loaded persisted orders", "count", len(orders))
	}

	return v, nil
}

// findOrder returns a previously persisted order for the given client UUID, if any.
func (v *Exchange) findOrder(clientID uuid.UUID) (*internal.Order, bool) {
	return v.orderMap.Load(clientID)
}

// findOrderByServerID returns a previously persisted order by IBKR numeric order ID.
func (v *Exchange) findOrderByServerID(orderID int64) (*internal.Order, bool) {
	return v.serverIDMap.Load(orderID)
}

// persistOrder saves a completed order to the datastore and updates orderMap.
// No-op if no database was provided at construction.
func (v *Exchange) persistOrder(ctx context.Context, order *internal.Order) {
	if v.datastore == nil {
		return
	}
	if err := v.datastore.SaveOrder(ctx, order); err != nil {
		slog.Warn("ibkr: could not persist order", "clientOrderID", order.ClientOrderID, "err", err)
		return
	}
	if id, err := uuid.Parse(order.ClientOrderID); err == nil {
		v.orderMap.Store(id, order)
	}
	v.serverIDMap.Store(order.OrderID, order)
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

	p, err := NewProduct(ctx, v, productID, conid, true, v.onBuyFill)
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

	price, err := v.client.FetchMidPrice(ctx, conid)
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

func (v *Exchange) GetOptionChain(ctx context.Context, underlying string) ([]*gobs.OptionContract, error) {
	return v.client.GetOptionChain(ctx, underlying)
}

// GetOptionsProduct returns a metadata snapshot for the option contract
// identified by its OCC symbol. Delegates to Client.GetOptionsProduct.
func (v *Exchange) GetOptionsProduct(ctx context.Context, occSymbol string) (*gobs.OptionContract, error) {
	return v.client.GetOptionsProduct(ctx, occSymbol)
}

func (v *Exchange) OpenOptionsProduct(ctx context.Context, occSymbol string) (exchange.OptionsProduct, error) {
	contract, err := v.GetOptionsProduct(ctx, occSymbol)
	if err != nil {
		return nil, err
	}
	conid, err := strconv.Atoi(contract.ContractID)
	if err != nil {
		return nil, fmt.Errorf("ibkr: invalid conid %q for %s: %w", contract.ContractID, occSymbol, err)
	}
	v.client.WatchOptionConid(occSymbol, conid)
	return NewOptionsProduct(ctx, v, contract)
}

func (v *Exchange) GetOrders(ctx context.Context) ([]*internal.Order, error) {
	return v.client.GetOrders(ctx)
}

func (v *Exchange) GetAccountSummary(ctx context.Context) (*AccountSummary, error) {
	return v.client.GetAccountSummary(ctx)
}

func (v *Exchange) GetPositions(ctx context.Context) ([]*Position, error) {
	return v.client.GetPositions(ctx)
}

func (v *Exchange) GetOptionContractInfo(ctx context.Context, conid int) (*gobs.OptionContract, error) {
	return v.client.GetOptionContractInfo(ctx, conid)
}

