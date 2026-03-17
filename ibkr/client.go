// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/bvk/tradebot/syncmap"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// Client is the HTTP client for the IBKR Client Portal Gateway. It manages
// the gateway session (via tickle), polls orders/prices/balances, and exposes
// methods for placing and cancelling orders.
//
// The gateway uses a self-signed TLS certificate, so InsecureSkipVerify is
// set on the HTTP transport. All requests are relative to the GatewayURL in
// Credentials.
type Client struct {
	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	wg sync.WaitGroup

	opts  Options
	creds Credentials

	httpClient http.Client

	balanceUpdatesTopic *topic.Topic[*internal.Balance]

	// marketOrderUpdateMap holds a per-symbol topic that goWatchOrders
	// publishes to on every poll cycle.
	marketOrderUpdateMap syncmap.Map[string, *topic.Topic[*internal.Order]]

	// marketPriceUpdateMap holds a per-symbol topic that goWatchPrices
	// publishes to on every poll cycle.
	marketPriceUpdateMap syncmap.Map[string, *topic.Topic[*internal.Quote]]
}

// apiTickleResponse is the JSON structure returned by POST /v1/api/tickle.
type apiTickleResponse struct {
	Session    string `json:"session"`
	SSOExpires int    `json:"ssoExpires"`
	IServer    struct {
		AuthStatus struct {
			Authenticated bool `json:"authenticated"`
			Connected     bool `json:"connected"`
			Competing     bool `json:"competing"`
		} `json:"authStatus"`
	} `json:"iserver"`
}

// apiAuthStatusResponse is the JSON structure returned by
// GET /v1/api/iserver/auth/status.
type apiAuthStatusResponse struct {
	Authenticated bool `json:"authenticated"`
	Connected     bool `json:"connected"`
	Competing     bool `json:"competing"`
}

// apiSecDefResult is one entry in the JSON array returned by
// GET /v1/api/iserver/secdef/search.
// The gateway returns conid as a JSON string (e.g. "265598"), not a number.
type apiSecDefResult struct {
	ConID       string `json:"conid"`
	Symbol      string `json:"symbol"`
	CompanyName string `json:"companyName"`
	Sections    []struct {
		SecType string `json:"secType"`
	} `json:"sections"`
}

// apiPlaceOrderRequest is the JSON body for
// POST /v1/api/iserver/account/{accountId}/orders.
type apiPlaceOrderRequest struct {
	AccountID  string  `json:"acctId"`
	ConID      int     `json:"conid"`
	// SecType is the composite "conid:secType" string required by the gateway.
	SecType    string  `json:"secType"`
	Ticker     string  `json:"ticker"`
	ClientOID  string  `json:"cOID"`
	OrderType  string  `json:"orderType"` // always "LMT"
	// Price and Quantity must be JSON numbers; shopspring/decimal marshals as
	// quoted strings which the gateway rejects with 400.
	Price      float64 `json:"price"`
	Side       string  `json:"side"`       // "BUY" or "SELL"
	Quantity   float64 `json:"quantity"`
	TIF        string  `json:"tif"`        // "GTC"
	OutsideRTH bool    `json:"outsideRTH"`
}

// apiPlaceOrderResponse is one element of the JSON array returned by
// POST /v1/api/iserver/account/{accountId}/orders.
//
// The gateway returns either a direct order confirmation (OrderID is set) or a
// reply challenge (ID and Message are set) that must be confirmed before the
// order is accepted.
type apiPlaceOrderResponse struct {
	// Set when order is accepted directly.
	OrderID     string `json:"order_id"`
	OrderStatus string `json:"order_status"`
	// Set when a reply challenge is issued.
	ID      string   `json:"id"`
	Message []string `json:"message"`
}

// apiPlaceOrdersRequestWrapper wraps orders in the envelope expected by the CP Gateway.
// The gateway requires {"orders": [...]} not a bare JSON array.
type apiPlaceOrdersRequestWrapper struct {
	Orders []*apiPlaceOrderRequest `json:"orders"`
}

// apiReplyRequest is the JSON body for
// POST /v1/api/iserver/account/{accountId}/orders/{replyId}.
type apiReplyRequest struct {
	Confirmed bool `json:"confirmed"`
}

// New creates a new Client, starts background goroutines, and returns it.
// The caller must call Close() to stop the goroutines.
func New(ctx context.Context, creds *Credentials, opts *Options) (*Client, error) {
	if err := creds.Check(); err != nil {
		return nil, err
	}
	if opts == nil {
		opts = new(Options)
	}
	opts.setDefaults()
	if err := opts.Check(); err != nil {
		return nil, err
	}

	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	c := &Client{
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		opts:       *opts,
		creds:      *creds,
		httpClient: http.Client{
			Timeout: opts.HttpClientTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // gateway uses a self-signed cert
				},
			},
		},
		balanceUpdatesTopic: topic.New[*internal.Balance](),
	}

	status, err := c.GetAuthStatus(ctx)
	if err != nil {
		lifeCancel(err)
		return nil, fmt.Errorf("ibkr: could not reach gateway at %s: %w", creds.GatewayURL, err)
	}
	if !status.Authenticated {
		lifeCancel(fmt.Errorf("not authenticated"))
		return nil, fmt.Errorf("ibkr: gateway session is not authenticated — log in via the gateway UI first")
	}

	c.wg.Add(1)
	go c.goTickle(c.lifeCtx)

	c.wg.Add(1)
	go c.goWatchOrders(c.lifeCtx)

	c.wg.Add(1)
	go c.goWatchBalances(c.lifeCtx)

	return c, nil
}

// Close stops all background goroutines and waits for them to exit.
func (c *Client) Close() error {
	c.lifeCancel(nil)
	c.wg.Wait()
	return nil
}

// WatchSymbol registers a symbol+conid with the client so that
// goWatchPrices is started for it. Idempotent — safe to call multiple times
// for the same symbol.
func (c *Client) WatchSymbol(symbol string, conid int) {
	topic := topic.New[*internal.Quote]()
	if _, loaded := c.marketPriceUpdateMap.LoadOrStore(symbol, topic); loaded {
		return // already watching
	}
	c.wg.Add(1)
	go c.goWatchPrices(c.lifeCtx, symbol, conid)
}

// GetSymbolOrdersTopic returns the per-symbol orders topic, creating it if
// needed. Products subscribe to this to receive order update notifications.
func (c *Client) GetSymbolOrdersTopic(symbol string) *topic.Topic[*internal.Order] {
	t := topic.New[*internal.Order]()
	if existing, loaded := c.marketOrderUpdateMap.LoadOrStore(symbol, t); loaded {
		return existing
	}
	return t
}

// GetSymbolPricesTopic returns the per-symbol prices topic. Returns nil if
// WatchSymbol has not been called for this symbol yet.
func (c *Client) GetSymbolPricesTopic(symbol string) *topic.Topic[*internal.Quote] {
	t, _ := c.marketPriceUpdateMap.Load(symbol)
	return t
}

// doRequest executes an HTTP request against the gateway and decodes the JSON
// response body into dst. If body is non-nil it is sent as JSON.
func (c *Client) doRequest(ctx context.Context, method, path string, body, dst any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.creds.GatewayURL+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == internal.StatusUnauthorized {
		return fmt.Errorf("ibkr: unauthorized (session expired): %w", errUnauthorized)
	}
	if resp.StatusCode == internal.StatusNotFound {
		return fmt.Errorf("ibkr: not found: %w", errNotFound)
	}
	if resp.StatusCode == internal.StatusTooManyRequests {
		return fmt.Errorf("ibkr: rate limited: %w", errRateLimited)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ibkr: unexpected status %d: %s", resp.StatusCode, string(data))
	}

	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return fmt.Errorf("ibkr: could not decode response: %w", err)
		}
	}
	return nil
}

// GetAuthStatus returns the current gateway authentication status.
func (c *Client) GetAuthStatus(ctx context.Context) (*apiAuthStatusResponse, error) {
	var status apiAuthStatusResponse
	if err := c.doRequest(ctx, http.MethodGet, "/v1/api/iserver/auth/status", nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// ListAccounts returns the list of account IDs available in the gateway
// session. Used by the setup subcommand to discover the AccountID.
func (c *Client) ListAccounts(ctx context.Context) ([]string, error) {
	var resp struct {
		Accounts []string `json:"accounts"`
	}
	if err := c.doRequest(ctx, http.MethodGet, "/v1/api/iserver/accounts", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Accounts, nil
}

// ResolveConid looks up the conid (contract ID) for a stock symbol by
// searching the IBKR security definition API. Returns the first STK match.
func (c *Client) ResolveConid(ctx context.Context, symbol string) (int, error) {
	var results []*apiSecDefResult
	path := "/v1/api/iserver/secdef/search?symbol=" + symbol + "&secType=STK"
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &results); err != nil {
		return 0, err
	}
	for _, r := range results {
		for _, s := range r.Sections {
			if s.SecType == "STK" {
				conid, err := strconv.Atoi(r.ConID)
				if err != nil {
					return 0, fmt.Errorf("ibkr: could not parse conid %q for symbol %q: %w", r.ConID, symbol, err)
				}
				return conid, nil
			}
		}
	}
	return 0, fmt.Errorf("ibkr: no STK conid found for symbol %q: %w", symbol, errNotFound)
}

// PlaceOrder places a limit order and returns the server-assigned order ID.
// If the gateway issues a reply challenge, it is auto-confirmed.
func (c *Client) PlaceOrder(ctx context.Context, symbol string, conid int, side string, qty, price decimal.Decimal, cOID string) (int64, error) {
	req := &apiPlaceOrderRequest{
		AccountID:  c.creds.AccountID,
		ConID:      conid,
		SecType:    strconv.Itoa(conid) + ":STK",
		Ticker:     symbol,
		ClientOID:  cOID,
		OrderType:  "LMT",
		Price:      price.InexactFloat64(),
		Side:       side,
		Quantity:   qty.InexactFloat64(),
		TIF:        "GTC",
		OutsideRTH: false,
	}
	path := "/v1/api/iserver/account/" + c.creds.AccountID + "/orders"

	wrapped := &apiPlaceOrdersRequestWrapper{Orders: []*apiPlaceOrderRequest{req}}

	var responses []apiPlaceOrderResponse
	if err := c.doRequest(ctx, http.MethodPost, path, wrapped, &responses); err != nil {
		return 0, err
	}

	// Resolve any reply challenges before reading the order ID.
	for _, r := range responses {
		if r.ID != "" {
			responses, err := c.confirmReply(ctx, r.ID)
			if err != nil {
				return 0, fmt.Errorf("ibkr: could not confirm order reply %q: %w", r.ID, err)
			}
			// Use the confirmed responses for the final order ID extraction.
			for _, cr := range responses {
				if cr.OrderID != "" {
					id, err := strconv.ParseInt(cr.OrderID, 10, 64)
					if err != nil {
						return 0, fmt.Errorf("ibkr: could not parse confirmed order id %q: %w", cr.OrderID, err)
					}
					return id, nil
				}
			}
			return 0, fmt.Errorf("ibkr: no order id in reply confirmation response")
		}
		if r.OrderID != "" {
			id, err := strconv.ParseInt(r.OrderID, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("ibkr: could not parse order id %q: %w", r.OrderID, err)
			}
			return id, nil
		}
	}
	return 0, fmt.Errorf("ibkr: place order returned no order id and no reply id")
}

// confirmReply sends a confirmation for a gateway reply challenge.
// The reply endpoint is /v1/api/iserver/reply/{replyId}, not account-scoped.
func (c *Client) confirmReply(ctx context.Context, replyID string) ([]apiPlaceOrderResponse, error) {
	path := "/v1/api/iserver/reply/" + replyID
	var responses []apiPlaceOrderResponse
	if err := c.doRequest(ctx, http.MethodPost, path, &apiReplyRequest{Confirmed: true}, &responses); err != nil {
		return nil, err
	}
	return responses, nil
}

// CancelOrder cancels an open order by its server-assigned order ID.
func (c *Client) CancelOrder(ctx context.Context, orderID int64) error {
	path := "/v1/api/iserver/account/" + c.creds.AccountID + "/order/" + strconv.FormatInt(orderID, 10)
	return c.doRequest(ctx, http.MethodDelete, path, nil, nil)
}

// GetOrders fetches all live orders from the gateway and converts them to the
// flat Order type.
func (c *Client) GetOrders(ctx context.Context) ([]*internal.Order, error) {
	var resp internal.APIOrdersResponse
	if err := c.doRequest(ctx, http.MethodGet, "/v1/api/iserver/account/orders", nil, &resp); err != nil {
		return nil, err
	}
	orders := make([]*internal.Order, 0, len(resp.Orders))
	for _, a := range resp.Orders {
		orders = append(orders, internal.NewOrderFromAPI(a))
	}
	return orders, nil
}

// GetBalance fetches the current portfolio summary and converts it to a Balance.
func (c *Client) GetBalance(ctx context.Context) (*internal.Balance, error) {
	path := "/v1/api/portfolio/" + c.creds.AccountID + "/summary"
	var summary internal.APISummary
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &summary); err != nil {
		return nil, err
	}
	b := internal.NewBalanceFromAPI(&summary)
	if b == nil {
		return nil, fmt.Errorf("ibkr: portfolio summary returned null or empty available funds")
	}
	return b, nil
}

// goTickle is a background goroutine that keeps the gateway session alive by
// sending POST /v1/api/tickle at every TickleInterval. It logs a warning if
// the gateway reports the session is no longer authenticated.
func (c *Client) goTickle(ctx context.Context) {
	defer c.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("ibkr: CAUGHT PANIC in goTickle", "panic", r)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.TickleInterval):
			var resp apiTickleResponse
			if err := c.doRequest(ctx, http.MethodPost, "/v1/api/tickle", nil, &resp); err != nil {
				slog.Warn("ibkr: tickle failed (session may expire)", "err", err)
				continue
			}
			if !resp.IServer.AuthStatus.Authenticated {
				slog.Warn("ibkr: gateway session is no longer authenticated — re-login required")
			}
		}
	}
}

// goWatchOrders polls GET /v1/api/iserver/account/orders at PollOrdersInterval
// and publishes each order to its per-symbol topic.
func (c *Client) goWatchOrders(ctx context.Context) {
	defer c.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("ibkr: CAUGHT PANIC in goWatchOrders", "panic", r)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.PollOrdersInterval):
			orders, err := c.GetOrders(ctx)
			if err != nil {
				slog.Warn("ibkr: could not poll orders (will retry)", "err", err)
				continue
			}
			for _, order := range orders {
				t, ok := c.marketOrderUpdateMap.Load(order.Symbol)
				if !ok {
					continue
				}
				if err := t.Send(order); err != nil {
					slog.Warn("ibkr: could not publish order update", "symbol", order.Symbol, "orderID", order.OrderID, "err", err)
				}
			}
		}
	}
}

// goWatchPrices polls the market data snapshot for a single symbol at
// PollPricesInterval and publishes non-nil quotes to the per-symbol topic.
func (c *Client) goWatchPrices(ctx context.Context, symbol string, conid int) {
	defer c.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("ibkr: CAUGHT PANIC in goWatchPrices", "panic", r, "symbol", symbol)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	t, ok := c.marketPriceUpdateMap.Load(symbol)
	if !ok {
		slog.Error("ibkr: goWatchPrices started but price topic not found", "symbol", symbol)
		return
	}

	path := fmt.Sprintf("/v1/api/iserver/marketdata/snapshot?conids=%d&fields=31,84,86", conid)

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.PollPricesInterval):
			var snapshots []*internal.APISnapshot
			if err := c.doRequest(ctx, http.MethodGet, path, nil, &snapshots); err != nil {
				slog.Warn("ibkr: could not poll price snapshot (will retry)", "symbol", symbol, "err", err)
				continue
			}
			for _, snap := range snapshots {
				q := internal.NewQuoteFromAPI(snap)
				if q == nil {
					continue // gateway not yet populated for this conid
				}
				if err := t.Send(q); err != nil {
					slog.Warn("ibkr: could not publish price update", "symbol", symbol, "err", err)
				}
			}
		}
	}
}

// goWatchBalances polls the portfolio summary at PollBalancesInterval and
// publishes balance updates to balanceUpdatesTopic.
func (c *Client) goWatchBalances(ctx context.Context) {
	defer c.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("ibkr: CAUGHT PANIC in goWatchBalances", "panic", r)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.opts.PollBalancesInterval):
			b, err := c.GetBalance(ctx)
			if err != nil {
				slog.Warn("ibkr: could not poll balance (will retry)", "err", err)
				continue
			}
			if err := c.balanceUpdatesTopic.Send(b); err != nil {
				slog.Warn("ibkr: could not publish balance update", "err", err)
			}
		}
	}
}

// Sentinel errors for HTTP status codes, used by callers to detect specific
// failure modes without string matching.
var (
	errUnauthorized = fmt.Errorf("unauthorized")
	errNotFound     = fmt.Errorf("not found")
	errRateLimited  = fmt.Errorf("rate limited")
)
