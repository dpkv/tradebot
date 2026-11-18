// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bvk/tradebot/etrade/internal"
	"github.com/shopspring/decimal"
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

// GetOptionChain returns every standard, unadjusted call and put on the
// underlying, across all expiries and strikes. A successful chain is cached
// for Options.ChainCacheTTL, and concurrent callers share one in-flight
// fetch, so greelers on any account re-selecting at the same moment cost one
// chain fetch. A failed fetch is not cached. The returned quotes are shared
// with other callers and must not be modified.
func (c *Client) GetOptionChain(ctx context.Context, symbol string) ([]*internal.OptionQuote, error) {
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

// GetOptionQuote fetches the live quote and greeks of one option contract
// from GET /v1/market/quote/{symbol}?detailFlag=OPTIONS. quoteSymbol is in
// E*TRADE's quote form (contract.QuoteSymbol). It is never cached. Returns
// os.ErrNotExist when E*TRADE has no option quote for the symbol.
func (c *Client) GetOptionQuote(ctx context.Context, quoteSymbol string) (*internal.OptionQuote, error) {
	params := url.Values{"detailFlag": {"OPTIONS"}}
	apiPath := "/v1/market/quote/" + url.PathEscape(quoteSymbol)
	var wrapper quoteResponseWrapper
	if err := doGetJSON(ctx, c, apiPath, params, &wrapper); err != nil {
		return nil, err
	}
	for _, qd := range wrapper.QuoteResponse.QuoteData {
		if q := internal.NewOptionQuoteFromAPI(qd); q != nil {
			return q, nil
		}
	}
	return nil, fmt.Errorf("etrade: no option quote for %s: %w", quoteSymbol, os.ErrNotExist)
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
	switch action {
	case OptionBuyOpen, OptionSellOpen, OptionBuyClose, OptionSellClose:
	default:
		return 0, fmt.Errorf("etrade: invalid option order action %q: %w", action, os.ErrInvalid)
	}
	if !qty.IsPositive() || !qty.Equal(qty.Truncate(0)) {
		return 0, fmt.Errorf("etrade: option order quantity %s must be a positive whole number: %w", qty, os.ErrInvalid)
	}
	if !limitPrice.IsPositive() {
		return 0, fmt.Errorf("etrade: option order limit price %s must be positive: %w", limitPrice, os.ErrInvalid)
	}

	creds := c.creds.Load()
	accountIDKey, err := creds.GetAccountIDKey(accountID)
	if err != nil {
		return 0, err
	}

	strike := k.Strike
	orderDetail := placeOrderDetail{
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
	}
	accountPath := "/v1/accounts/" + url.PathEscape(accountIDKey)
	return c.previewAndPlace(ctx, accountPath, "OPTN", clientOrderID, &orderDetail)
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
