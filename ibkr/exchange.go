// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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

func (v *Exchange) GetOptionChain(ctx context.Context, underlying string) ([]*gobs.OptionContract, error) {
	return v.client.GetOptionChain(ctx, underlying)
}

// occContractID returns the compact OCC option symbol for the given contract,
// e.g. "AAPL261218C00200000". The suffix is always 15 chars: expiry as YYMMDD,
// right (C/P), strike × 1000 zero-padded to 8 digits.
func occContractID(symbol string, expiry time.Time, right string, strike decimal.Decimal) string {
	strikePennies := strike.Mul(decimal.NewFromInt(1000)).IntPart()
	return fmt.Sprintf("%s%s%s%08d", symbol, expiry.Format("060102"), right, strikePennies)
}

// parseOCCContractID parses a compact OCC option symbol back into its
// components. The suffix is always 15 chars, so the symbol is everything
// before that. Returns an error if the string is malformed.
func parseOCCContractID(contractID string) (symbol, right string, expiry time.Time, strike decimal.Decimal, err error) {
	const suffixLen = 15 // YYMMDD(6) + right(1) + strike(8)
	if len(contractID) <= suffixLen {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: too short", contractID)
	}
	split := len(contractID) - suffixLen
	symbol = contractID[:split]
	suffix := contractID[split:]
	expiry, err = time.Parse("060102", suffix[:6])
	if err != nil {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: bad expiry: %w", contractID, err)
	}
	right = string(suffix[6])
	if right != "C" && right != "P" {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: right must be C or P", contractID)
	}
	pennies, err := strconv.ParseInt(suffix[7:], 10, 64)
	if err != nil {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: bad strike: %w", contractID, err)
	}
	strike = decimal.NewFromInt(pennies).Div(decimal.NewFromInt(1000))
	return symbol, right, expiry, strike, nil
}

// GetOptionsProduct returns a metadata snapshot for the option contract
// identified by its OCC Symbol. It calls secdef/info to resolve the exact
// IBKR conid, which is stored in ContractID. Analogous to GetSpotProduct.
func (v *Exchange) GetOptionsProduct(ctx context.Context, occSymbol string) (*gobs.OptionContract, error) {
	symbol, right, expiry, strike, err := parseOCCContractID(occSymbol)
	if err != nil {
		return nil, err
	}

	underlyingConid, err := v.resolveConid(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("ibkr: GetOptionsProduct: could not resolve conid for %q: %w", symbol, err)
	}

	month := strings.ToUpper(expiry.Format("Jan06"))

	infoPath := fmt.Sprintf(
		"/v1/api/iserver/secdef/info?conid=%d&sectype=OPT&month=%s&right=%s&strike=%s&exchange=SMART",
		underlyingConid, month, right, strike.String(),
	)
	var entries []*apiSecDefInfoEntry
	if err := v.client.doRequest(ctx, "GET", infoPath, nil, &entries); err != nil {
		return nil, fmt.Errorf("ibkr: secdef/info for %q: %w", occSymbol, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("ibkr: no contract found for %q: %w", occSymbol, os.ErrNotExist)
	}
	e := entries[0]

	multiplier := decimal.NewFromInt(100)
	if e.Multiplier != "" {
		if m, err2 := decimal.NewFromString(e.Multiplier); err2 == nil {
			multiplier = m
		}
	}

	optType := "CALL"
	if right == "P" {
		optType = "PUT"
	}

	return &gobs.OptionContract{
		Symbol:       occSymbol,
		ContractID:   strconv.Itoa(e.Conid),
		Underlying:   symbol,
		OptionType:   optType,
		Strike:       strike,
		Expiry:       expiry,
		ContractSize: multiplier,
	}, nil
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
	return NewOptionsProduct(ctx, v.client, contract)
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
