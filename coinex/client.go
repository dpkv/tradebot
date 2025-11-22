// Copyright (c) 2025 BVK Chaitanya

package coinex

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bvk/tradebot/coinex/internal"
	"github.com/bvk/tradebot/ctxutil"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/syncmap"

	"github.com/visvasity/topic"
)

// websocketNoticeHandler is a function type for handling websocket notices from CoinEx.
type websocketNoticeHandler func(context.Context, *internal.WebsocketNotice) error

// Client represents a CoinEx API client that handles REST API calls and websocket connections.
// It manages authentication, time synchronization, order updates, and market data subscriptions.
type Client struct {
	// lifeCtx and lifeCancel manage the client's lifecycle and allow graceful shutdown.
	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	// wg tracks all goroutines started by the client for proper cleanup.
	wg sync.WaitGroup

	// opts contains configuration options for the client.
	opts Options

	// client is the HTTP client used for making REST API requests.
	client http.Client

	// key and secret are the API credentials for authenticating requests.
	key, secret string

	// timeOffsetMillis stores the time difference between local time and CoinEx server time
	// in milliseconds. This is used to adjust timestamps for accurate time synchronization.
	timeOffsetMillis atomic.Int64

	// refreshOrdersTopic publishes order updates that need to be refreshed from the server.
	refreshOrdersTopic *topic.Topic[*internal.Order]
	// balanceUpdatesTopic publishes balance update events from websocket messages.
	balanceUpdatesTopic *topic.Topic[*internal.BalanceUpdate]

	// marketOrderUpdateMap holds per-market topics for order update subscriptions.
	marketOrderUpdateMap syncmap.Map[string, *topic.Topic[*internal.Order]]
	// marketBBOUpdateMap holds per-market topics for best bid/offer (BBO) price update subscriptions.
	marketBBOUpdateMap syncmap.Map[string, *topic.Topic[*internal.BBOUpdate]]

	// websocketHandlerMap maps websocket notice types to their handler functions.
	websocketHandlerMap map[string]websocketNoticeHandler

	// websocketCallCh is a channel for sending websocket call requests.
	websocketCallCh chan *internal.WebsocketCall
	// websocketCallMap tracks pending websocket calls by their call ID.
	websocketCallMap syncmap.Map[int64, *internal.WebsocketCall]
}

// New creates and initializes a new CoinEx client instance.
// It validates credentials, sets up time synchronization, and starts background goroutines
// for websocket message handling and order refresh operations.
func New(ctx context.Context, key, secret string, opts *Options) (*Client, error) {
	if opts == nil {
		opts = new(Options)
	}
	opts.setDefaults()
	if err := opts.Check(); err != nil {
		return nil, err
	}

	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	c := &Client{
		opts:       *opts,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		key:        key,
		secret:     secret,
		client: http.Client{
			Timeout: opts.HttpClientTimeout,
		},
		websocketHandlerMap: make(map[string]websocketNoticeHandler),
		websocketCallCh:     make(chan *internal.WebsocketCall, 10),
		refreshOrdersTopic:  topic.New[*internal.Order](),
		balanceUpdatesTopic: topic.New[*internal.BalanceUpdate](),
	}
	c.websocketHandlerMap["bbo.update"] = c.onBBOUpdate
	c.websocketHandlerMap["order.update"] = c.onOrderUpdate
	c.websocketHandlerMap["balance.update"] = c.onBalanceUpdate

	if err := c.updateTimeAdjustment(ctx); err != nil {
		return nil, err
	}

	// Check that credentials are valid.
	if _, err := c.GetMarkets(ctx); err != nil {
		return nil, err
	}

	if !opts.NoWebsocket {
		c.wg.Add(1)
		go c.goGetMessages(c.lifeCtx)
	}

	c.wg.Add(1)
	go c.goRefreshOrders(c.lifeCtx)

	c.wg.Add(1)
	go c.goUpdateTimeAdjustment(c.lifeCtx)
	return c, nil
}

// Close releases resources and destroys the client instance.
func (c *Client) Close() error {
	c.lifeCancel(os.ErrClosed)
	c.wg.Wait()
	return nil
}

// goUpdateTimeAdjustment runs in a background goroutine to periodically update
// the time offset between local time and CoinEx server time. This ensures accurate
// timestamp generation for API requests.
func (c *Client) goUpdateTimeAdjustment(ctx context.Context) {
	defer c.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("CAUGHT PANIC", "panic", r)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	for ctx.Err() == nil {
		if err := c.updateTimeAdjustment(ctx); err != nil {
			ctxutil.Sleep(ctx, time.Second)
			continue
		}
		ctxutil.Sleep(ctx, time.Minute)
	}
}

// updateTimeAdjustment fetches the CoinEx server time and calculates the offset
// between local time and server time. It retries with exponential backoff on failure
// and validates that the time difference is within acceptable limits.
func (c *Client) updateTimeAdjustment(ctx context.Context) error {
	for i := 0; ctx.Err() == nil; i = min(5, i+1) {
		s := time.Now()
		stime, err := c.GetSystemTime(ctx)
		rtt := time.Since(s)
		if err != nil {
			slog.Error("could not fetch coinex server time (will retry)", "err", err)
			ctxutil.Sleep(ctx, time.Second<<i)
			continue
		}
		if rtt > c.opts.MaxFetchTimeLatency {
			slog.Error("took too long to fetch coinex server time (will retry)", "rtt", rtt, "max-allowed", c.opts.MaxFetchTimeLatency)
			ctxutil.Sleep(ctx, time.Second<<i)
			continue
		}
		localTimestamp := s.Add(rtt / 2)
		remoteTimestamp := time.UnixMilli(stime.TimestampMilli).Add(rtt / 2)
		adjustment := remoteTimestamp.Sub(localTimestamp)
		if adjustment > c.opts.MaxTimeAdjustment {
			slog.Error("time adjustment required is too large", "required", adjustment, "max-allowed", c.opts.MaxTimeAdjustment)
			return fmt.Errorf("time adjustment required is too large")
		}
		slog.Info("calculated time sync offset with coinex system time", "offset", adjustment)
		c.timeOffsetMillis.Store(int64(adjustment / time.Millisecond))
		return nil
	}
	return context.Cause(ctx)
}

// now returns the current time adjusted by the time offset to match CoinEx server time.
func (c *Client) now() gobs.RemoteTime {
	return gobs.RemoteTime{Time: time.Now().Add(time.Millisecond * time.Duration(c.timeOffsetMillis.Load()))}
}

// getMarketOrdersTopic returns or creates a topic for order updates for the given market.
// This allows multiple subscribers to receive order updates for a specific market.
func (c *Client) getMarketOrdersTopic(market string) *topic.Topic[*internal.Order] {
	tp, ok := c.marketOrderUpdateMap.Load(market)
	if !ok {
		tp, _ = c.marketOrderUpdateMap.LoadOrStore(market, topic.New[*internal.Order]())
	}
	return tp
}

// getMarketPricesTopic returns or creates a topic for best bid/offer (BBO) price updates
// for the given market. This allows subscribers to receive real-time price updates.
func (c *Client) getMarketPricesTopic(market string) *topic.Topic[*internal.BBOUpdate] {
	tp, ok := c.marketBBOUpdateMap.Load(market)
	if !ok {
		tp, _ = c.marketBBOUpdateMap.LoadOrStore(market, topic.New[*internal.BBOUpdate]())
	}
	return tp
}

// GetSystemTime retrieves the current system time from CoinEx servers.
// This is used for time synchronization to ensure accurate timestamps.
func (c *Client) GetSystemTime(ctx context.Context) (*internal.CoinExTime, error) {
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/time"),
	}
	resp := new(internal.GetSystemTimeResponse)
	if err := httpGetJSON(ctx, &c.client, addrURL, resp, &c.opts); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not get server time", "url", addrURL, "err", err)
		}
		return nil, err
	}
	return resp.Data, nil
}

// GetMarkets retrieves a list of all available spot markets and their status information.
func (c *Client) GetMarkets(ctx context.Context) ([]*internal.MarketStatus, error) {
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/spot/market"),
	}
	resp := new(internal.GetMarketsResponse)
	if err := httpGetJSON(ctx, &c.client, addrURL, resp, &c.opts); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not get market status", "url", addrURL, "err", err)
		}
		return nil, err
	}
	return resp.Data, nil
}

// GetMarket retrieves the status information for a specific market.
func (c *Client) GetMarket(ctx context.Context, market string) (*internal.MarketStatus, error) {
	values := make(url.Values)
	values.Set("market", market)

	addrURL := &url.URL{
		Scheme:   RestURL.Scheme,
		Host:     RestURL.Host,
		Path:     path.Join(RestURL.Path, "/spot/market"),
		RawQuery: values.Encode(),
	}
	resp := new(internal.GetMarketsResponse)
	if err := httpGetJSON(ctx, &c.client, addrURL, resp, &c.opts); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not get market status", "url", addrURL, "err", err)
		}
		return nil, err
	}
	return resp.Data[0], nil
}

// GetMarketInfo retrieves ticker information (last price, 24h volume, etc.) for a specific market.
func (c *Client) GetMarketInfo(ctx context.Context, market string) (*internal.MarketInfo, error) {
	values := make(url.Values)
	values.Set("market", market)

	addrURL := &url.URL{
		Scheme:   RestURL.Scheme,
		Host:     RestURL.Host,
		Path:     path.Join(RestURL.Path, "/spot/ticker"),
		RawQuery: values.Encode(),
	}
	resp := new(internal.GetMarketInfoResponse)
	if err := httpGetJSON(ctx, &c.client, addrURL, resp, &c.opts); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not get market ticker information", "url", addrURL, "err", err)
		}
		return nil, err
	}
	return resp.Data[0], nil
}

// GetBalances retrieves all funds information in spot accounts.
func (c *Client) GetBalances(ctx context.Context) ([]*internal.Balance, error) {
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/assets/spot/balance"),
	}
	resp := new(internal.GetBalancesResponse)
	if err := privateGetJSON(ctx, c, addrURL, nil /* request */, resp); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not get asset balances", "url", addrURL, "err", err)
		}
		return nil, err
	}
	return resp.Data, nil
}

// ListFilledOrders returns an iterator over all filled orders, optionally filtered by market and side.
// The iterator handles pagination automatically. Orders are also published to the market's order topic.
func (c *Client) ListFilledOrders(ctx context.Context, market, side string, errp *error) iter.Seq[*internal.Order] {
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/spot/finished-order"),
	}

	values := make(url.Values)
	values.Set("market_type", "SPOT")
	if market != "" {
		values.Set("market", market)
	}
	if side != "" {
		values.Set("side", side)
	}

	return func(yield func(*internal.Order) bool) {
		for page := 1; *errp == nil; page++ {
			values.Set("page", strconv.FormatInt(int64(page), 10))
			addrURL.RawQuery = values.Encode()

			resp := new(internal.ListFilledOrdersResponse)
			if err := privateGetJSON(ctx, c, addrURL, nil /* request */, resp); err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.Error("could not get filled orders", "page", page, "url", addrURL, "err", err)
				}
				*errp = err
				return
			}

			for _, order := range resp.Data {
				c.getMarketOrdersTopic(order.Market).Send(order)
				if !yield(order) {
					return
				}
			}

			if resp.Pagination == nil || resp.Pagination.HasNext == false {
				return
			}
		}
	}
}

// ListUnfilledOrders returns an iterator over all pending/unfilled orders, optionally filtered by market and side.
// The iterator handles pagination automatically. Orders are also published to the market's order topic.
func (c *Client) ListUnfilledOrders(ctx context.Context, market, side string, errp *error) iter.Seq[*internal.Order] {
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/spot/pending-order"),
	}

	values := make(url.Values)
	values.Set("market_type", "SPOT")
	if market != "" {
		values.Set("market", market)
	}
	if side != "" {
		values.Set("side", side)
	}

	return func(yield func(*internal.Order) bool) {
		for page := 1; *errp == nil; page++ {
			values.Set("page", strconv.FormatInt(int64(page), 10))
			addrURL.RawQuery = values.Encode()

			resp := new(internal.ListFilledOrdersResponse)
			if err := privateGetJSON(ctx, c, addrURL, nil /* request */, resp); err != nil {
				if !errors.Is(err, context.Canceled) {
					slog.Error("could not get unfilled orders", "page", page, "url", addrURL, "err", err)
				}
				*errp = err
				return
			}

			for _, order := range resp.Data {
				c.getMarketOrdersTopic(order.Market).Send(order)
				if !yield(order) {
					return
				}
			}

			if resp.Pagination == nil || resp.Pagination.HasNext == false {
				return
			}
		}
	}
}

// CreateOrder creates a new spot order on CoinEx. The created order is published
// to the market's order topic for subscribers to receive updates.
func (c *Client) CreateOrder(ctx context.Context, req *internal.CreateOrderRequest) (*internal.Order, error) {
	if req.MarketType != "SPOT" {
		return nil, fmt.Errorf("market type must be SPOT: %w", os.ErrInvalid)
	}
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/spot/order"),
	}
	resp := new(internal.CreateOrderResponse)
	if err := privatePostJSON(ctx, c, addrURL, req /* request */, resp); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not create order", "market", req.Market, "side", req.Side, "price", req.Price, "size", req.Amount, "url", addrURL, "err", err)
		}
		return nil, err
	}
	c.getMarketOrdersTopic(req.Market).Send(resp.Data)
	return resp.Data, nil
}

// GetOrder retrieves the current status of an order by its order ID and market.
// The order is also published to the market's order topic.
func (c *Client) GetOrder(ctx context.Context, market string, orderID int64) (*internal.Order, error) {
	values := make(url.Values)
	values.Set("market", market)
	values.Set("order_id", strconv.FormatInt(orderID, 10))

	addrURL := &url.URL{
		Scheme:   RestURL.Scheme,
		Host:     RestURL.Host,
		Path:     path.Join(RestURL.Path, "/spot/order-status"),
		RawQuery: values.Encode(),
	}
	resp := new(internal.GetOrderResponse)
	if err := privateGetJSON(ctx, c, addrURL, nil /* request */, resp); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, os.ErrNotExist) {
			slog.Error("could not get order information", "url", addrURL, "err", err)
		}
		return nil, err
	}
	c.getMarketOrdersTopic(market).Send(resp.Data)
	return resp.Data, nil
}

// BatchQueryOrders retrieves the status of multiple orders in a single API call.
// This is more efficient than calling GetOrder multiple times. Orders are published
// to the market's order topic if the query is successful.
func (c *Client) BatchQueryOrders(ctx context.Context, market string, ids []int64) ([]*internal.GetOrderResponse, error) {
	var sb strings.Builder
	for i, id := range ids {
		if i != 0 {
			sb.WriteRune(',')
		}
		sb.WriteString(strconv.FormatInt(id, 10))
	}

	values := make(url.Values)
	values.Set("market", market)
	values.Set("order_ids", sb.String())

	addrURL := &url.URL{
		Scheme:   RestURL.Scheme,
		Host:     RestURL.Host,
		Path:     path.Join(RestURL.Path, "/spot/batch-order-status"),
		RawQuery: values.Encode(),
	}
	resp := new(internal.BatchQueryOrdersResponse)
	if err := privateGetJSON(ctx, c, addrURL, nil /* request */, resp); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not batch query orders", "url", addrURL, "err", err)
		}
		return nil, err
	}
	for i, item := range resp.Data {
		if item.Code == 0 {
			c.getMarketOrdersTopic(market).Send(item.Data)
			continue
		}
		log.Printf("TODO: query order status for order %d failed with status code=%d message=%s", ids[i], item.Code, item.Message)
	}
	return resp.Data, nil
}

// CancelOrder cancels an existing order by its order ID. CoinEx does not save
// zero-filled canceled orders, so os.ErrNotExist is treated as success in that case.
// The canceled order is published to the market's order topic.
func (c *Client) CancelOrder(ctx context.Context, market string, orderID int64) (*internal.Order, error) {
	req := internal.CancelOrderRequest{
		Market:     market,
		MarketType: "SPOT",
		OrderID:    orderID,
	}
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/spot/cancel-order"),
	}
	resp := new(internal.CancelOrderResponse)
	if err := privatePostJSON(ctx, c, addrURL, &req, resp); err != nil {
		// CoinEx does not save zero-filled canceled orders, so os.ErrNotExist is a success.
		if !errors.Is(err, os.ErrNotExist) {
			if !errors.Is(err, context.Canceled) {
				slog.Error("could not cancel order", "orderID", orderID, "market", market, "url", addrURL, "err", err)
			}
			return nil, err
		}
		resp.Data = &internal.Order{}
	}
	// CoinEx does not save zero-filled canceled orders, so we won't be able to
	// query/refresh them.
	if resp.Data.FilledAmount.IsZero() {
		resp.Data.Status = "canceled"
	} else {
		c.refreshOrdersTopic.Send(resp.Data)
	}
	c.getMarketOrdersTopic(market).Send(resp.Data)
	return resp.Data, nil
}

// CancelOrderByClientID cancels an order using the client-provided order ID instead of
// the server-assigned order ID. This is useful when you only have the client ID.
func (c *Client) CancelOrderByClientID(ctx context.Context, market string, clientOrderID string) (*internal.Order, error) {
	req := internal.CancelOrderByClientIDRequest{
		Market:     market,
		MarketType: "SPOT",
		ClientID:   clientOrderID,
	}
	addrURL := &url.URL{
		Scheme: RestURL.Scheme,
		Host:   RestURL.Host,
		Path:   path.Join(RestURL.Path, "/spot/cancel-order-by-client-id"),
	}
	resp := new(internal.CancelOrderByClientIDResponse)
	if err := privatePostJSON(ctx, c, addrURL, &req, resp); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			if !errors.Is(err, context.Canceled) {
				slog.Error("could not cancel order by client id", "clientID", clientOrderID, "market", market, "url", addrURL, "err", err)
			}
			return nil, err
		}
	}
	if len(resp.Data) == 0 {
		slog.Warn("cancel order by client id request returned empty data", "clientID", clientOrderID, "market", market)
		return nil, os.ErrNotExist
	}
	if resp.Data[0].Code == internal.OrderNotFound {
		return nil, os.ErrNotExist
	}
	c.getMarketOrdersTopic(market).Send(resp.Data[0].Data)
	return resp.Data[0].Data, nil
}

// WatchMarket subscribes to streaming updates for a market.
func (c *Client) WatchMarket(ctx context.Context, market string) error {
	if _, ok := c.marketBBOUpdateMap.Load(market); ok {
		return os.ErrExist
	}
	if err := c.websocketSign(ctx); err != nil {
		return err
	}
	if err := c.websocketMarketListSubscribe(ctx, "bbo.subscribe", []string{market}); err != nil {
		return err
	}
	if err := c.websocketMarketListSubscribe(ctx, "order.subscribe", []string{market}); err != nil {
		return err
	}
	c.marketBBOUpdateMap.LoadOrStore(market, topic.New[*internal.BBOUpdate]())
	return nil
}

// UnwatchMarket unsubscribes from streaming updates for a market.
func (c *Client) UnwatchMarket(ctx context.Context, market string) error {
	old, ok := c.marketBBOUpdateMap.Load(market)
	if !ok {
		return os.ErrNotExist
	}
	if err := c.websocketMarketListUnsubscribe(ctx, "bbo.unsubscribe", []string{market}); err != nil {
		return err
	}
	if ok := c.marketBBOUpdateMap.CompareAndDelete(market, old); ok {
		old.Close()
	}
	return nil
}

// do performs an authenticated HTTP request to CoinEx API. It constructs the request signature
// using HMAC-SHA256 with the API secret, and includes the API key and timestamp in headers.
func (c *Client) do(ctx context.Context, method string, addrURL *url.URL, body, contentType string) (*http.Response, error) {
	var sb strings.Builder
	sb.WriteString(method)
	sb.WriteString(addrURL.Path)
	if len(addrURL.RawQuery) != 0 {
		sb.WriteRune('?')
		sb.WriteString(addrURL.RawQuery)
	}
	if body != "" {
		sb.WriteString(body)
	}

	now := c.now()
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	sb.WriteString(timestamp)

	hash := hmac.New(sha256.New, []byte(c.secret))
	io.WriteString(hash, sb.String())
	signature := hash.Sum(nil)

	req, err := http.NewRequestWithContext(ctx, method, addrURL.String(), strings.NewReader(body))
	if err != nil {
		slog.Error("could not create http request object with context", "method", method, "url", addrURL, "err", err)
		return nil, err
	}

	if len(contentType) != 0 {
		req.Header.Add("Content-Type", contentType)
	}
	req.Header.Add("X-COINEX-KEY", c.key)
	req.Header.Add("X-COINEX-SIGN", fmt.Sprintf("%x", signature))
	req.Header.Add("X-COINEX-TIMESTAMP", timestamp)
	return c.client.Do(req)
}

// httpGetJSON performs a public (unauthenticated) HTTP GET request and unmarshals
// the JSON response. It handles retries for rate limiting and bad gateway errors.
func httpGetJSON[PT *T, T any](ctx context.Context, client *http.Client, addrURL *url.URL, response PT, opts *Options) error {
	if opts == nil {
		opts = new(Options)
		opts.setDefaults()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addrURL.String(), nil)
	if err != nil {
		slog.Error("could not create http get request with context", "url", addrURL, "err", err)
		return err
	}

	s := time.Now()
	resp, err := client.Do(req)
	if d := time.Now().Sub(s); d > opts.HttpClientTimeout {
		slog.Warn(fmt.Sprintf("get request took %s which is more than the http client timeout %s", d, opts.HttpClientTimeout))
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not perform http get request", "url", addrURL, "err", err)
		}
		return err
	}
	defer resp.Body.Close()

	sleep := func(d time.Duration) error {
		sctx, scancel := context.WithTimeout(ctx, d)
		<-sctx.Done()
		scancel()
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		slog.Warn("http get returned unsuccessful status code", "status-code", resp.StatusCode, "url", addrURL)
		if body, err := io.ReadAll(resp.Body); err == nil {
			log.Printf("server response was %s", body)
		}

		if resp.StatusCode == http.StatusBadGateway {
			if err := sleep(time.Second); err != nil {
				return err
			}
			return httpGetJSON(ctx, client, addrURL, response, opts)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
			timeout := time.Second
			if x := resp.Header.Get("Retry-After"); len(x) != 0 {
				if v, err := strconv.Atoi(x); err == nil {
					timeout = time.Duration(v) * time.Second
				}
			}

			if err := sleep(timeout); err != nil {
				return err
			}
			return httpGetJSON(ctx, client, addrURL, response, opts)
		}

		slog.Error("http GET is unsuccessful", "status", resp.StatusCode, "url", addrURL)
		return fmt.Errorf("http GET returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// Parse into a generic response.
	var genericResp internal.GenericResponse
	if err := json.Unmarshal(data, &genericResp); err != nil {
		slog.Error("could not unmarshal into generic response", "response", string(data), "err", err)
		return err
	}
	if genericResp.Code != 0 {
		slog.Error("public GET request failed", "url", addrURL, "response", string(data), "err", err)
		return fmt.Errorf("failed with code=%d message=%s", genericResp.Code, genericResp.Message)
	}

	if err := json.Unmarshal(data, response); err != nil {
		slog.Error("could not decode response to json", "err", err)
		return err
	}
	return nil
}

// sleep sleeps for the specified duration while respecting context cancellation.
func sleep(ctx context.Context, d time.Duration) error {
	sctx, scancel := context.WithTimeout(ctx, d)
	<-sctx.Done()
	scancel()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

// privateGetJSON performs an authenticated HTTP GET request and unmarshals the JSON response.
// It handles retries for rate limiting and bad gateway errors, and maps CoinEx error codes
// to standard Go errors (e.g., OrderNotFound -> os.ErrNotExist).
func privateGetJSON[PT *T, T any](ctx context.Context, c *Client, addrURL *url.URL, request any, response PT) error {
	var sb strings.Builder
	contentType := ""
	if request != nil {
		if err := json.NewEncoder(&sb).Encode(request); err != nil {
			return err
		}
		contentType = "application/json"
	}

	resp, err := c.do(ctx, http.MethodGet, addrURL, sb.String(), contentType)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not perform http get request", "url", addrURL, "err", err)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("http get returned unsuccessful status code", "status-code", resp.StatusCode, "url", addrURL)
		if body, err := io.ReadAll(resp.Body); err == nil {
			log.Printf("server response was %s", body)
		}

		if resp.StatusCode == http.StatusBadGateway {
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
			return privateGetJSON(ctx, c, addrURL, request, response)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
			timeout := time.Second
			if x := resp.Header.Get("Retry-After"); len(x) != 0 {
				if v, err := strconv.Atoi(x); err == nil {
					timeout = time.Duration(v) * time.Second
				}
			}

			if err := sleep(ctx, timeout); err != nil {
				return err
			}
			return privateGetJSON(ctx, c, addrURL, request, response)
		}

		slog.Error("http GET is unsuccessful", "status", resp.StatusCode, "url", addrURL)
		return fmt.Errorf("http GET returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// Parse into a generic response.
	var genericResp internal.GenericResponse
	if err := json.Unmarshal(data, &genericResp); err != nil {
		slog.Error("could not unmarshal into generic response", "response", string(data), "err", err)
		return err
	}
	if genericResp.Code != 0 {
		if genericResp.Code == internal.OrderNotFound {
			return os.ErrNotExist
		}
		slog.Error("private GET request failed", "url", addrURL, "body", sb.String(), "response", string(data), "err", err)
		return fmt.Errorf("failed with code=%d message=%s", genericResp.Code, genericResp.Message)
	}

	if err := json.Unmarshal(data, response); err != nil {
		slog.Error("could not decode response to json", "err", err)
		return err
	}
	return nil
}

// privatePostJSON performs an authenticated HTTP POST request and unmarshals the JSON response.
// It handles retries for rate limiting and bad gateway errors, and maps CoinEx error codes
// to standard Go errors (e.g., InsufficientFunds -> exchange.ErrNoFund, OrderNotFound -> os.ErrNotExist).
func privatePostJSON[PT *T, T any](ctx context.Context, c *Client, addrURL *url.URL, request any, response PT) error {
	var sb strings.Builder
	if request != nil {
		if err := json.NewEncoder(&sb).Encode(request); err != nil {
			return err
		}
	}

	resp, err := c.do(ctx, http.MethodPost, addrURL, sb.String(), "application/json")
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("could not perform http post request", "url", addrURL, "err", err)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("http post returned unsuccessful status code", "status-code", resp.StatusCode, "url", addrURL)
		if body, err := io.ReadAll(resp.Body); err == nil {
			log.Printf("server response was %s", body)
		}

		if resp.StatusCode == http.StatusBadGateway {
			if err := sleep(ctx, time.Second); err != nil {
				return err
			}
			return privatePostJSON(ctx, c, addrURL, request, response)
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusTeapot {
			timeout := time.Second
			if x := resp.Header.Get("Retry-After"); len(x) != 0 {
				if v, err := strconv.Atoi(x); err == nil {
					timeout = time.Duration(v) * time.Second
				}
			}

			if err := sleep(ctx, timeout); err != nil {
				return err
			}
			return privatePostJSON(ctx, c, addrURL, request, response)
		}

		slog.Error("http POST is unsuccessful", "status", resp.StatusCode, "url", addrURL)
		return fmt.Errorf("http POST returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// Parse into a generic response.
	var genericResp internal.GenericResponse
	if err := json.Unmarshal(data, &genericResp); err != nil {
		slog.Error("could not unmarshal into generic response", "response", string(data), "err", err)
		return err
	}
	if genericResp.Code != 0 {
		if genericResp.Code == internal.InsufficientFunds {
			return exchange.ErrNoFund
		}
		if genericResp.Code == internal.OrderNotFound {
			return os.ErrNotExist
		}
		slog.Error("POST request failed", "url", addrURL, "body", sb.String(), "response", string(data), "err", err)
		return fmt.Errorf("failed with code=%d message=%s", genericResp.Code, genericResp.Message)
	}
	if len(genericResp.Data) == 0 {
		slog.Warn("received empty Data response", "url", addrURL, "body", sb.String(), "response", string(data), "err", err)
	}

	if err := json.Unmarshal(data, response); err != nil {
		slog.Error("could not decode response to json", "err", err)
		return err
	}
	return nil
}

// goRefreshOrders runs in a background goroutine to periodically refresh order statuses
// from the server. It subscribes to the refreshOrdersTopic and queries the server for
// fresh order data when updates are requested, then publishes the results to market-specific topics.
func (c *Client) goRefreshOrders(ctx context.Context) {
	defer c.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("CAUGHT PANIC", "panic", r)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	receiver, err := topic.Subscribe(c.refreshOrdersTopic, 0, true)
	if err != nil {
		slog.Error("could not subscribe to refreshOrdersTopic (unexpected)", "err", err)
		return
	}
	defer receiver.Close()

	stopf := context.AfterFunc(ctx, receiver.Close)
	defer stopf()

	for ctx.Err() == nil {
		order, err := receiver.Receive()
		if err != nil {
			continue
		}

		fresh, err := c.GetOrder(ctx, order.Market, order.OrderID)
		if err != nil {
			if !errors.Is(err, context.Cause(ctx)) {
				slog.Warn("could not query order (will retry)", "market", order.Market, "orderID", order.OrderID, "err", err)
				time.AfterFunc(time.Second, func() {
					c.refreshOrdersTopic.Send(order)
				}) // Schedule a retry.
			}
			continue
		}
		// Publish the order as an update.
		if topic, ok := c.marketOrderUpdateMap.Load(order.Market); ok {
			topic.Send(fresh)
		}
	}
}
