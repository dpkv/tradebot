// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/bvk/tradebot/syncmap"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// OptionsProduct implements exchange.OptionsProduct for a single IBKR option
// contract. Follows the same pattern as Product (stocks) but keyed by conid
// for order routing and uses PlaceOptionOrder for order placement.
type OptionsProduct struct {
	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	wg sync.WaitGroup

	client *Client

	// Contract identity.
	occSymbol    string
	conid        int
	underlying   string
	optionType   string
	strike       decimal.Decimal
	expiry       time.Time
	contractSize decimal.Decimal

	clientIDStatusMap syncmap.Map[uuid.UUID, *clientIDStatus]
}

var _ exchange.OptionsProduct = &OptionsProduct{}

// NewOptionsProduct creates an OptionsProduct from a resolved gobs.OptionContract
// and starts its background goroutine. WatchOptionConid must have been called
// on the client before NewOptionsProduct so that the price topic exists.
func NewOptionsProduct(ctx context.Context, client *Client, contract *gobs.OptionContract) (*OptionsProduct, error) {
	conid, err := strconv.Atoi(contract.ContractID)
	if err != nil {
		return nil, fmt.Errorf("ibkr: OptionsProduct: invalid conid %q in contract %s", contract.ContractID, contract.Symbol)
	}

	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	p := &OptionsProduct{
		lifeCtx:      lifeCtx,
		lifeCancel:   lifeCancel,
		client:       client,
		occSymbol:    contract.Symbol,
		conid:        conid,
		underlying:   contract.Underlying,
		optionType:   contract.OptionType,
		strike:       contract.Strike,
		expiry:       contract.Expiry,
		contractSize: contract.ContractSize,
	}

	p.wg.Add(1)
	go p.goWatchOrderUpdates(p.lifeCtx)

	return p, nil
}

func (p *OptionsProduct) Close() error {
	p.lifeCancel(os.ErrClosed)
	p.wg.Wait()
	return nil
}

func (p *OptionsProduct) Symbol() string             { return p.occSymbol }
func (p *OptionsProduct) ContractID() string         { return strconv.Itoa(p.conid) }
func (p *OptionsProduct) Underlying() string         { return p.underlying }
func (p *OptionsProduct) OptionType() string         { return p.optionType }
func (p *OptionsProduct) Strike() decimal.Decimal    { return p.strike }
func (p *OptionsProduct) Expiry() time.Time          { return p.expiry }
func (p *OptionsProduct) ContractSize() decimal.Decimal { return p.contractSize }

// GetOrderUpdates returns a receiver that delivers order updates for this
// option contract as they are published by goWatchOrders in the client.
func (p *OptionsProduct) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	t := p.client.GetOptionOrdersTopic(p.conid)
	fn := func(o *internal.Order) exchange.OrderUpdate { return o }
	return topic.SubscribeFunc(t, fn, 0, true)
}

// GetPriceUpdates returns a receiver that delivers price updates for this
// option contract as they are published by goWatchPrices in the client.
func (p *OptionsProduct) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	t := p.client.GetSymbolPricesTopic(p.occSymbol)
	if t == nil {
		return nil, os.ErrNotExist
	}
	fn := func(q *internal.Quote) exchange.PriceUpdate { return q }
	return topic.SubscribeFunc(t, fn, 1, true)
}

// GetGreeks fetches a current snapshot of the option sensitivities from the
// CP Gateway market data snapshot endpoint.
func (p *OptionsProduct) GetGreeks(ctx context.Context) (*exchange.Greeks, error) {
	path := fmt.Sprintf("/v1/api/iserver/marketdata/snapshot?conids=%d&fields=7308,7309,7310,7311,7312", p.conid)
	var snapshots []*internal.APIGreeksSnapshot
	if err := p.client.doRequest(ctx, "GET", path, nil, &snapshots); err != nil {
		return nil, fmt.Errorf("ibkr: Greeks snapshot for %s: %w", p.occSymbol, err)
	}
	for _, snap := range snapshots {
		if g := internal.NewGreeksFromAPI(snap); g != nil {
			return g, nil
		}
	}
	return nil, fmt.Errorf("ibkr: Greeks not yet available for %s (gateway still populating)", p.occSymbol)
}

// LimitBuyToOpen places a GTC limit buy order to enter a new long position.
func (p *OptionsProduct) LimitBuyToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.placeOrder(ctx, clientID, "BUY", numContracts, limitPrice)
}

// LimitSellToOpen places a GTC limit sell order to enter a new short position.
func (p *OptionsProduct) LimitSellToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.placeOrder(ctx, clientID, "SELL", numContracts, limitPrice)
}

// LimitBuyToClose places a GTC limit buy order to close an existing short position.
func (p *OptionsProduct) LimitBuyToClose(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.placeOrder(ctx, clientID, "BUY", numContracts, limitPrice)
}

// LimitSellToClose places a GTC limit sell order to close an existing long position.
func (p *OptionsProduct) LimitSellToClose(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.placeOrder(ctx, clientID, "SELL", numContracts, limitPrice)
}

func (p *OptionsProduct) placeOrder(ctx context.Context, clientID uuid.UUID, side string, qty, price decimal.Decimal) (_ exchange.Order, status error) {
	cstatus, loaded := p.clientIDStatusMap.LoadOrStore(clientID, newClientIDStatus())
	cstatus.mu.Lock()
	defer cstatus.mu.Unlock()

	if loaded {
		if cstatus.err != nil {
			return nil, cstatus.err
		}
		if cstatus.order != nil {
			return cstatus.order, nil
		}
	}
	defer func() {
		cstatus.err = status
	}()

	serverOrderID, err := p.client.PlaceOptionOrder(ctx, p.underlying, p.conid, side, qty, price, clientID.String())
	if err != nil {
		return nil, err
	}

	order := &internal.Order{
		OrderID:       serverOrderID,
		ClientOrderID: clientID.String(),
		ConID:         p.conid,
		Symbol:        p.underlying,
		SecType:       "OPT",
		Side:          side,
		Status:        "Submitted",
		LimitPrice:    price,
		OrderedQty:    qty,
	}
	cstatus.order = order
	return order, nil
}

// Get returns the current state of an order by server ID.
func (p *OptionsProduct) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return nil, err
	}

	for cid, cstatus := range p.clientIDStatusMap.Range {
		_ = cid
		cstatus.mu.Lock()
		if cstatus.order != nil && cstatus.order.OrderID == orderID {
			o := cstatus.order
			cstatus.mu.Unlock()
			return o, nil
		}
		cstatus.mu.Unlock()
	}

	orders, err := p.client.GetOrders(ctx)
	if err != nil {
		return nil, err
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			return o, nil
		}
	}
	return nil, os.ErrNotExist
}

// Cancel cancels an open order by its server ID.
func (p *OptionsProduct) Cancel(ctx context.Context, serverID string) error {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return err
	}
	return p.client.CancelOrder(ctx, orderID)
}

// goWatchOrderUpdates subscribes to the per-conid orders topic and updates the
// cached order snapshot for any order whose cOID matches a known clientID.
func (p *OptionsProduct) goWatchOrderUpdates(ctx context.Context) {
	defer p.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("ibkr: CAUGHT PANIC in OptionsProduct.goWatchOrderUpdates", "panic", r, "symbol", p.occSymbol)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	sub, err := topic.Subscribe(p.client.GetOptionOrdersTopic(p.conid), 0, true)
	if err != nil {
		slog.Error("ibkr: could not subscribe to option order updates topic", "symbol", p.occSymbol, "err", err)
		return
	}
	defer sub.Close()

	stopf := context.AfterFunc(ctx, sub.Close)
	defer stopf()

	for ctx.Err() == nil {
		order, err := sub.Receive()
		if err != nil {
			return
		}
		cid := order.ClientID()
		if cid == uuid.Nil {
			continue
		}
		cstatus, ok := p.clientIDStatusMap.Load(cid)
		if !ok {
			continue
		}
		cstatus.mu.Lock()
		cstatus.order = order
		cstatus.mu.Unlock()
	}
}
