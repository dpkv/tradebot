// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bvk/tradebot/etrade/internal"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// Option market data, option order placement and account transactions. The
// chain and quotes are market data shared by every account; orders and
// transactions take an account ID.

type optionExpireDateResponseWrapper struct {
	OptionExpireDateResponse internal.APIOptionExpireDateResponse `json:"OptionExpireDateResponse"`
}

type optionChainResponseWrapper struct {
	OptionChainResponse internal.APIOptionChainResponse `json:"OptionChainResponse"`
}

type transactionListResponseWrapper struct {
	TransactionListResponse internal.APITransactionListResponse `json:"TransactionListResponse"`
}

// GetOptionExpiryDates fetches every listed expiry date for the underlying
// from GET /v1/market/optionexpiredate.
func (c *Client) GetOptionExpiryDates(ctx context.Context, symbol string) ([]*internal.APIExpirationDate, error) {
	params := url.Values{
		"symbol":     {strings.ToUpper(symbol)},
		"expiryType": {"ALL"},
	}
	var wrapper optionExpireDateResponseWrapper
	if err := doGetJSON(ctx, c, "/v1/market/optionexpiredate", params, &wrapper); err != nil {
		return nil, err
	}
	return wrapper.OptionExpireDateResponse.ExpirationDate, nil
}

// getOptionChainForDate fetches the standard, unadjusted calls and puts of
// one expiry, at every strike, from GET /v1/market/optionchains.
func (c *Client) getOptionChainForDate(ctx context.Context, symbol string, date *internal.APIExpirationDate) ([]*internal.OptionQuote, error) {
	params := url.Values{
		"symbol":         {symbol},
		"expiryYear":     {strconv.Itoa(date.Year)},
		"expiryMonth":    {strconv.Itoa(date.Month)},
		"expiryDay":      {strconv.Itoa(date.Day)},
		"chainType":      {"CALLPUT"},
		"optionCategory": {"STANDARD"},
		"skipAdjusted":   {"true"},
		"includeWeekly":  {"true"},
	}
	var wrapper optionChainResponseWrapper
	if err := doGetJSON(ctx, c, "/v1/market/optionchains", params, &wrapper); err != nil {
		return nil, err
	}
	resp := &wrapper.OptionChainResponse
	if resp.SelectedED.Year == 0 {
		resp.SelectedED.Year, resp.SelectedED.Month, resp.SelectedED.Day = date.Year, date.Month, date.Day
	}
	if resp.SelectedED.Year != date.Year || resp.SelectedED.Month != date.Month || resp.SelectedED.Day != date.Day {
		return nil, fmt.Errorf("etrade: option chain for %s %04d-%02d-%02d returned expiry %04d-%02d-%02d", symbol,
			date.Year, date.Month, date.Day, resp.SelectedED.Year, resp.SelectedED.Month, resp.SelectedED.Day)
	}

	var quotes []*internal.OptionQuote
	for _, q := range internal.NewOptionQuotesFromChain(resp) {
		// skipAdjusted should already drop these; adjustedFlag is checked again
		// because greel sizes every contract as 100 shares.
		if q.Adjusted || (q.Category != "" && q.Category != "STANDARD") {
			continue
		}
		if q.Symbol != symbol {
			slog.Warn("etrade: skipping option chain entry for another underlying", "want", symbol, "got", q.Symbol)
			continue
		}
		quotes = append(quotes, q)
	}
	return quotes, nil
}

// chainEntry is one cached option chain. done is closed when the fetch
// finishes; quotes, err and at are set before that and never change after.
type chainEntry struct {
	done   chan struct{}
	at     time.Time
	quotes []*internal.OptionQuote
	err    error
}

func (e *chainEntry) finished() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// GetCachedOptionChain returns every standard, unadjusted call and put on the
// underlying, across all expiries and strikes. A successful chain is cached
// for Options.ChainCacheTTL, and concurrent callers share one in-flight
// fetch, so greelers on any account re-selecting at the same moment cost one
// chain fetch. A failed fetch is not cached. The returned quotes are shared
// with other callers and must not be modified.
func (c *Client) GetCachedOptionChain(ctx context.Context, symbol string) ([]*internal.OptionQuote, error) {
	symbol = strings.ToUpper(symbol)

	c.chainMu.Lock()
	e, ok := c.chainMap[symbol]
	if !ok || (e.finished() && (e.err != nil || time.Since(e.at) >= c.opts.ChainCacheTTL)) {
		e = &chainEntry{done: make(chan struct{})}
		if c.chainMap == nil {
			c.chainMap = make(map[string]*chainEntry)
		}
		c.chainMap[symbol] = e
		// The fetch runs on the client's lifetime, not the caller's context, so
		// one caller giving up doesn't fail the others waiting on it.
		go c.fetchOptionChain(c.lifeCtx, symbol, e)
	}
	c.chainMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-e.done:
		return e.quotes, e.err
	}
}

func (c *Client) fetchOptionChain(ctx context.Context, symbol string, e *chainEntry) {
	defer close(e.done)
	defer func() { e.at = time.Now() }()

	dates, err := c.GetOptionExpiryDates(ctx, symbol)
	if err != nil {
		e.err = fmt.Errorf("etrade: could not fetch option expiry dates for %s: %w", symbol, err)
		return
	}
	var quotes []*internal.OptionQuote
	for _, date := range dates {
		qs, err := c.getOptionChainForDate(ctx, symbol, date)
		if err != nil {
			// A partial chain could hide the contract a greeler should pick, so
			// one failed expiry fails the chain.
			e.err = fmt.Errorf("etrade: could not fetch option chain for %s %04d-%02d-%02d: %w", symbol, date.Year, date.Month, date.Day, err)
			return
		}
		quotes = append(quotes, qs...)
	}
	e.quotes = quotes
}

// maxQuoteSymbols is the most symbols E*TRADE accepts in one quote request.
const maxQuoteSymbols = 25

// GetOptionQuotes fetches live quotes and greeks for option contracts from
// GET /v1/market/quote/{symbols}?detailFlag=OPTIONS, in batches of
// maxQuoteSymbols. quoteSymbols are in E*TRADE's quote form
// (contract.QuoteSymbol). Contracts E*TRADE has no option quote for are
// missing from the result. Never cached.
func (c *Client) GetOptionQuotes(ctx context.Context, quoteSymbols []string) ([]*internal.OptionQuote, error) {
	params := url.Values{"detailFlag": {"OPTIONS"}}
	var quotes []*internal.OptionQuote
	for start := 0; start < len(quoteSymbols); start += maxQuoteSymbols {
		batch := quoteSymbols[start:min(start+maxQuoteSymbols, len(quoteSymbols))]
		apiPath := "/v1/market/quote/" + url.PathEscape(strings.Join(batch, ","))
		var wrapper quoteResponseWrapper
		if err := doGetJSON(ctx, c, apiPath, params, &wrapper); err != nil {
			return nil, err
		}
		for _, qd := range wrapper.QuoteResponse.QuoteData {
			if q := internal.NewOptionQuoteFromAPI(qd); q != nil {
				quotes = append(quotes, q)
			}
		}
	}
	return quotes, nil
}

// GetOptionQuote fetches the live quote and greeks of one option contract.
// See GetOptionQuotes. Returns os.ErrNotExist when E*TRADE has no option
// quote for the symbol.
func (c *Client) GetOptionQuote(ctx context.Context, quoteSymbol string) (*internal.OptionQuote, error) {
	quotes, err := c.GetOptionQuotes(ctx, []string{quoteSymbol})
	if err != nil {
		return nil, err
	}
	if len(quotes) == 0 {
		return nil, fmt.Errorf("etrade: no option quote for %s: %w", quoteSymbol, os.ErrNotExist)
	}
	return quotes[0], nil
}

// optionQuoteContractID returns the contract ID an option quote is for.
func optionQuoteContractID(q *internal.OptionQuote) (string, error) {
	k, err := newContract(q.Symbol, q.CallPut, q.Year, q.Month, q.Day, q.Strike)
	if err != nil {
		return "", err
	}
	return k.ID(), nil
}

// orderTopicKey returns the order topic key for an order: the symbol for an
// equity order and the contract ID for an option order. E*TRADE reports an
// option order's symbol as its underlying, so keying options by symbol would
// deliver them to the underlying's equity product.
func orderTopicKey(o *internal.Order) (string, error) {
	if !strings.EqualFold(o.SecurityType, "OPTN") {
		return o.Symbol, nil
	}
	k, err := newContract(o.Symbol, o.CallPut, o.ExpiryYear, o.ExpiryMonth, o.ExpiryDay, o.StrikePrice)
	if err != nil {
		return "", err
	}
	return k.ID(), nil
}

// publishOrder sends an order update to its instrument's order topic.
func (c *Client) publishOrder(o *internal.Order) {
	key, err := orderTopicKey(o)
	if err != nil {
		slog.Warn("etrade: dropping option order update with an invalid contract", "account-id", o.AccountID, "order-id", o.OrderID, "err", err)
		return
	}
	c.getSymbolOrdersTopic(key).Send(o)
}

// getOptionPriceTopic returns the contract's price topic, or nil when the
// contract isn't watched.
func (c *Client) getOptionPriceTopic(contractID string) *topic.Topic[*internal.OptionQuote] {
	tp, ok := c.optionPriceTopicMap.Load(contractID)
	if !ok {
		return nil
	}
	return tp
}

// WatchOption adds the contract to the price poll and returns its current
// quote. Watches are counted: the contract stays in the poll, and its updates
// keep going to its price topic, until every WatchOption has been matched by
// an UnwatchOption. Each open OptionProduct holds one watch.
func (c *Client) WatchOption(ctx context.Context, contractID string) (*internal.OptionQuote, error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return nil, err
	}
	q, err := c.GetOptionQuote(ctx, k.QuoteSymbol())
	if err != nil {
		return nil, err
	}

	c.optionWatchMu.Lock()
	defer c.optionWatchMu.Unlock()

	tp, ok := c.optionPriceTopicMap.Load(contractID)
	if !ok {
		tp = topic.New[*internal.OptionQuote]()
		c.optionPriceTopicMap.Store(contractID, tp)
	}
	if c.optionWatchRefs == nil {
		c.optionWatchRefs = make(map[string]int)
	}
	c.optionWatchRefs[contractID]++
	tp.Send(q)
	return q, nil
}

// UnwatchOption releases one WatchOption. When the last watch is released
// the contract leaves the price poll, so a closed or expired contract stops
// costing a quote per poll, and its price topic is closed.
func (c *Client) UnwatchOption(contractID string) {
	c.optionWatchMu.Lock()
	defer c.optionWatchMu.Unlock()

	n := c.optionWatchRefs[contractID]
	if n == 0 {
		return
	}
	if n > 1 {
		c.optionWatchRefs[contractID] = n - 1
		return
	}
	delete(c.optionWatchRefs, contractID)
	if tp, ok := c.optionPriceTopicMap.LoadAndDelete(contractID); ok {
		tp.Close()
	}
}

// pollOptionPrices fetches quotes for every watched contract and sends each
// to its contract's price topic. Called from goPollPrices.
func (c *Client) pollOptionPrices(ctx context.Context) {
	var symbols []string
	c.optionPriceTopicMap.Range(func(contractID string, _ *topic.Topic[*internal.OptionQuote]) bool {
		if k, err := parseContractID(contractID); err == nil {
			symbols = append(symbols, k.QuoteSymbol())
		}
		return true
	})
	if len(symbols) == 0 {
		return
	}

	quotes, err := c.GetOptionQuotes(ctx, symbols)
	if err != nil && !errors.Is(err, context.Cause(ctx)) {
		slog.Warn("etrade: could not poll option prices (will retry)", "symbols", symbols, "err", err)
	}
	for _, q := range quotes {
		id, err := optionQuoteContractID(q)
		if err != nil {
			slog.Warn("etrade: dropping option quote with an invalid contract", "symbol", q.Symbol, "err", err)
			continue
		}
		if tp := c.getOptionPriceTopic(id); tp != nil {
			tp.Send(q)
		}
	}
}

// Option order actions.
const (
	OptionBuyOpen   = "BUY_OPEN"
	OptionSellOpen  = "SELL_OPEN"
	OptionBuyClose  = "BUY_CLOSE"
	OptionSellClose = "SELL_CLOSE"
)

// PlaceOptionLimitOrder submits a regular-session, good-for-the-day limit
// order on one option contract through the same preview-then-place flow as
// PlaceLimitOrder. qty is in contracts and limitPrice is per share; neither
// is rounded. action is one of the Option* order actions. Returns the
// E*TRADE-assigned order ID.
func (c *Client) PlaceOptionLimitOrder(ctx context.Context, accountID string, k *contract, action string, qty, limitPrice decimal.Decimal, clientOrderID string) (int64, error) {
	orderDetail, err := optionOrderDetail(k, action, qty, limitPrice)
	if err != nil {
		return 0, err
	}
	accountPath, err := c.accountPath(accountID)
	if err != nil {
		return 0, err
	}
	return c.previewAndPlace(ctx, accountPath, "OPTN", clientOrderID, orderDetail)
}

// PlaceOptionOrder is PlaceOptionLimitOrder for a contract named by its
// compact OSI contract ID, e.g. "AAPL261218P00200000". An ID that doesn't
// parse fails with os.ErrInvalid before any request.
func (c *Client) PlaceOptionOrder(ctx context.Context, accountID, contractID, action string, qty, limitPrice decimal.Decimal, clientOrderID string) (int64, error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return 0, err
	}
	return c.PlaceOptionLimitOrder(ctx, accountID, k, action, qty, limitPrice, clientOrderID)
}

// PreviewOptionOrder runs only the preview step for the order that
// PlaceOptionOrder would place with the same arguments. Nothing is placed.
func (c *Client) PreviewOptionOrder(ctx context.Context, accountID, contractID, action string, qty, limitPrice decimal.Decimal, clientOrderID string) (*OrderPreview, error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return nil, err
	}
	orderDetail, err := optionOrderDetail(k, action, qty, limitPrice)
	if err != nil {
		return nil, err
	}
	accountPath, err := c.accountPath(accountID)
	if err != nil {
		return nil, err
	}
	resp, err := c.previewOrder(ctx, accountPath, "OPTN", clientOrderID, orderDetail)
	if err != nil {
		return nil, err
	}
	return newOrderPreview(resp), nil
}

// optionOrderDetail checks and builds the order detail of an option limit
// order, shared by placing and previewing it.
func optionOrderDetail(k *contract, action string, qty, limitPrice decimal.Decimal) (*placeOrderDetail, error) {
	switch action {
	case OptionBuyOpen, OptionSellOpen, OptionBuyClose, OptionSellClose:
	default:
		return nil, fmt.Errorf("etrade: invalid option order action %q: %w", action, os.ErrInvalid)
	}
	if !qty.IsPositive() || !qty.Equal(qty.Truncate(0)) {
		return nil, fmt.Errorf("etrade: option order quantity %s must be a positive whole number: %w", qty, os.ErrInvalid)
	}
	if !limitPrice.IsPositive() {
		return nil, fmt.Errorf("etrade: option order limit price %s must be positive: %w", limitPrice, os.ErrInvalid)
	}

	strike := k.Strike
	return &placeOrderDetail{
		PriceType:     "LIMIT",
		OrderTerm:     "GOOD_FOR_DAY",
		MarketSession: "REGULAR",
		LimitPrice:    limitPrice,
		Instrument: []placeOrderInstrument{{
			Product: placeOrderProduct{
				SecurityType: "OPTN",
				Symbol:       k.Root,
				CallPut:      k.CallPut,
				ExpiryYear:   k.Year,
				ExpiryMonth:  k.Month,
				ExpiryDay:    k.Day,
				StrikePrice:  &strike,
			},
			OrderAction:  action,
			QuantityType: "QUANTITY",
			Quantity:     qty,
		}},
	}, nil
}

// maxTransactionsPerPage is the largest page E*TRADE serves for the
// transactions list.
const maxTransactionsPerPage = 50

// ListTransactions fetches the account's transactions dated from start to
// end, as New York calendar days, from GET /v1/accounts/{accountIdKey}/transactions,
// following marker pagination until every page has been fetched.
func (c *Client) ListTransactions(ctx context.Context, accountID string, start, end time.Time) ([]*internal.Transaction, error) {
	creds := c.creds.Load()
	accountIDKey, err := creds.GetAccountIDKey(accountID)
	if err != nil {
		return nil, err
	}
	apiPath := "/v1/accounts/" + url.PathEscape(accountIDKey) + "/transactions"

	var txs []*internal.Transaction

	marker := ""
	for page := 0; ; page++ {
		if page >= maxOrderPages {
			return nil, fmt.Errorf("etrade: ListTransactions exceeded %d pages (marker %q); aborting", maxOrderPages, marker)
		}

		params := url.Values{
			"startDate": {etradeDate(start)},
			"endDate":   {etradeDate(end)},
			"count":     {strconv.Itoa(maxTransactionsPerPage)},
		}
		if marker != "" {
			params.Set("marker", marker)
		}

		var wrapper transactionListResponseWrapper
		if err := doGetJSON(ctx, c, apiPath, params, &wrapper); err != nil {
			return nil, err
		}
		resp := &wrapper.TransactionListResponse
		for _, t := range resp.Transaction {
			txs = append(txs, internal.NewTransactionFromAPI(t))
		}

		if !resp.MoreTransactions || resp.Marker == "" {
			break
		}
		marker = resp.Marker
	}
	return txs, nil
}
