// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bvk/tradebot/etrade/autologin"
	"github.com/bvk/tradebot/etrade/internal"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/syncmap"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// contractSize is the shares per contract. Only standard contracts are
// listed (decision 3), so it never varies.
var contractSize = decimal.NewFromInt(100)

// OptionProduct implements exchange.OptionsProduct for one option contract
// in one account (greel/etrade-options-story.md, scenario 3).
//
// Orders are regular-session, good-for-the-day limit orders, sent unrounded.
// There is no extended-hours retry and no background cancel of failed
// placements: optlimiter waits for the session and recovers a failed
// placement itself through GetOptionsOrderByClientID, which works because
// every order's client ID entry is written before the broker sees it.
type OptionProduct struct {
	lifeCtx    context.Context
	lifeCancel context.CancelCauseFunc

	wg sync.WaitGroup

	db       kv.Database
	client   *Client
	account  autologin.Account
	contract *contract
	id       string

	// etradeOrderID generates the numeric clientOrderId E*TRADE requires. It
	// is not persisted or used for lookup.
	etradeOrderID atomic.Int64

	// placeMu serializes placements, so two calls with the same client ID
	// can't both pass the existing-entry check.
	placeMu sync.Mutex

	// serverIDMap maps this account's server order IDs for the contract to
	// their client UUIDs, to restore ClientUUID on updates and Get.
	serverIDMap syncmap.Map[int64, uuid.UUID]
}

var _ exchange.OptionsProduct = &OptionProduct{}

// newOptionProduct opens the contract for trading in the account. It adds
// the contract to the price poll, loads the account's order entries for the
// contract, and re-tracks those not yet seen finished.
func newOptionProduct(ctx context.Context, db kv.Database, client *Client, account autologin.Account, contractID string) (_ *OptionProduct, status error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return nil, err
	}
	if _, err := client.WatchOption(ctx, contractID); err != nil {
		return nil, fmt.Errorf("etrade: could not watch option contract %s: %w", contractID, err)
	}
	defer func() {
		if status != nil {
			client.UnwatchOption(contractID)
		}
	}()

	lifeCtx, lifeCancel := context.WithCancelCause(context.Background())
	p := &OptionProduct{
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		db:         db,
		client:     client,
		account:    account,
		contract:   k,
		id:         contractID,
	}
	p.etradeOrderID.Store(time.Now().UnixNano())

	var track []int64
	if err := ascendOptionOrders(ctx, db, func(uid uuid.UUID, e *optionOrderEntry) error {
		if e.AccountID != account.AccountID || e.ContractID != contractID || e.ServerOrderID == 0 {
			return nil
		}
		p.serverIDMap.Store(e.ServerOrderID, uid)
		if !e.Done {
			track = append(track, e.ServerOrderID)
		}
		return nil
	}); err != nil {
		lifeCancel(err)
		return nil, fmt.Errorf("etrade: could not load option order entries for %s: %w", contractID, err)
	}

	// Subscribe before tracking, so no update of a re-tracked order is missed.
	sub, err := topic.Subscribe(client.getSymbolOrdersTopic(contractID), 0, false)
	if err != nil {
		lifeCancel(err)
		return nil, err
	}
	p.wg.Add(1)
	go p.goMarkDone(p.lifeCtx, sub)

	for _, id := range track {
		client.TrackOrder(account.AccountID, id)
	}
	return p, nil
}

// Close stops the product and releases its price watch.
func (p *OptionProduct) Close() error {
	p.lifeCancel(os.ErrClosed)
	p.wg.Wait()
	p.client.UnwatchOption(p.id)
	return nil
}

func (p *OptionProduct) ContractID() string            { return p.id }
func (p *OptionProduct) Underlying() string            { return p.contract.Root }
func (p *OptionProduct) OptionType() string            { return p.contract.CallPut }
func (p *OptionProduct) Strike() decimal.Decimal       { return p.contract.Strike }
func (p *OptionProduct) Expiry() time.Time             { return p.contract.Expiry() }
func (p *OptionProduct) ContractSize() decimal.Decimal { return contractSize }
func (p *OptionProduct) ExchangeName() string          { return "etrade" }

func (p *OptionProduct) AccountName() string {
	return fmt.Sprintf("etrade/%s", p.account.AccountName)
}

func (p *OptionProduct) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
	tp := p.client.getOptionPriceTopic(p.id)
	if tp == nil {
		return nil, fmt.Errorf("etrade: option contract %s is not watched: %w", p.id, os.ErrClosed)
	}
	fn := func(q *internal.OptionQuote) exchange.PriceUpdate { return q }
	return topic.SubscribeFunc(tp, fn, 1, true)
}

func (p *OptionProduct) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
	fn := func(o *internal.Order) exchange.OrderUpdate {
		if o.AccountID != p.account.AccountID {
			return nil
		}
		return p.withClientUUID(o)
	}
	return topic.SubscribeFunc(p.client.getSymbolOrdersTopic(p.id), fn, 0, true)
}

// withClientUUID returns the order with its client UUID restored, cloning it
// so the shared topic value isn't modified.
func (p *OptionProduct) withClientUUID(o *internal.Order) *internal.Order {
	if o.ClientUUID != uuid.Nil {
		return o
	}
	uid, ok := p.serverIDMap.Load(o.OrderID)
	if !ok {
		return o
	}
	clone := *o
	clone.ClientUUID = uid
	return &clone
}

// GetGreeks fetches the contract's live greeks. Never cached.
func (p *OptionProduct) GetGreeks(ctx context.Context) (*exchange.Greeks, error) {
	q, err := p.client.GetOptionQuote(ctx, p.contract.QuoteSymbol())
	if err != nil {
		return nil, err
	}
	return q.Greeks(), nil
}

func (p *OptionProduct) LimitBuyToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.place(ctx, clientID, OptionBuyOpen, numContracts, limitPrice)
}

func (p *OptionProduct) LimitSellToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.place(ctx, clientID, OptionSellOpen, numContracts, limitPrice)
}

func (p *OptionProduct) LimitBuyToClose(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.place(ctx, clientID, OptionBuyClose, numContracts, limitPrice)
}

func (p *OptionProduct) LimitSellToClose(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (exchange.Order, error) {
	return p.place(ctx, clientID, OptionSellClose, numContracts, limitPrice)
}

// place writes the client ID entry, places the order, and records its server
// ID. A client ID that already has an entry is never placed again: its order
// is returned if the server ID is known, and an error otherwise.
func (p *OptionProduct) place(ctx context.Context, clientID uuid.UUID, action string, qty, price decimal.Decimal) (exchange.Order, error) {
	if clientID == uuid.Nil {
		return nil, fmt.Errorf("etrade: option order needs a client id: %w", os.ErrInvalid)
	}
	if err := context.Cause(p.lifeCtx); err != nil {
		return nil, err
	}

	p.placeMu.Lock()
	defer p.placeMu.Unlock()

	existing, err := getOptionOrderEntry(ctx, p.db, clientID)
	if err == nil {
		if existing.AccountID != p.account.AccountID || existing.ContractID != p.id {
			return nil, fmt.Errorf("etrade: client id %s is already used by another option order", clientID)
		}
		if existing.ServerOrderID == 0 {
			return nil, fmt.Errorf("etrade: client id %s was already placed with an unknown outcome; look it up by client id", clientID)
		}
		order, err := p.client.GetOrder(ctx, p.account.AccountID, existing.ServerOrderID)
		if err != nil {
			return nil, err
		}
		order.ClientUUID = clientID
		return order, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	now := time.Now().UnixMilli()
	entry := &optionOrderEntry{
		AccountID:  p.account.AccountID,
		ContractID: p.id,
		Placement: orderPlacementInfo{
			RequestTimeMilli: now,
			Side:             action,
			Price:            price,
			Qty:              qty,
			PriceType:        "LIMIT",
			OrderTerm:        "GOOD_FOR_DAY",
			MarketSession:    "REGULAR",
		},
	}
	// Written before anything reaches the broker: no entry means never sent.
	if err := setOptionOrderEntry(ctx, p.db, clientID, entry); err != nil {
		return nil, err
	}

	etradeClientID := strconv.FormatInt(p.etradeOrderID.Add(1), 10)
	orderID, err := p.client.PlaceOptionLimitOrder(ctx, p.account.AccountID, p.contract, action, qty, price, etradeClientID)
	if err != nil {
		// The order may or may not exist at the broker. The entry stays with a
		// zero server ID, so a lookup by client ID can still find it.
		return nil, err
	}

	// The order exists at the broker, so keep trying to save its server ID
	// even if the caller gives up; only closing the product stops this.
	entry.ServerOrderID = orderID
	for {
		err := setOptionOrderEntry(p.lifeCtx, p.db, clientID, entry)
		if err == nil {
			break
		}
		slog.Error("etrade: could not persist option order server id (will retry)", "contract", p.id, "client-id", clientID, "order-id", orderID, "err", err)
		if err := sleep(p.lifeCtx, time.Second); err != nil {
			return nil, fmt.Errorf("etrade: product closed before option order %d could be saved: %w", orderID, err)
		}
	}
	p.serverIDMap.Store(orderID, clientID)
	p.client.TrackOrder(p.account.AccountID, orderID)

	// A partial order with what is known now; polling fills in the rest.
	return &internal.Order{
		AccountID:       p.account.AccountID,
		OrderID:         orderID,
		ClientUUID:      clientID,
		Symbol:          p.contract.Root,
		SecurityType:    "OPTN",
		Side:            action,
		Status:          "OPEN",
		PriceType:       "LIMIT",
		OrderTerm:       "GOOD_FOR_DAY",
		LimitPrice:      price,
		OrderedQty:      qty,
		PlacedTimeMilli: now,
		ExpiryYear:      p.contract.Year,
		ExpiryMonth:     p.contract.Month,
		ExpiryDay:       p.contract.Day,
		StrikePrice:     p.contract.Strike,
		CallPut:         p.contract.CallPut,
	}, nil
}

// Get fetches an order of this contract by server ID, with its client UUID
// restored.
func (p *OptionProduct) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("etrade: invalid server order id %q: %w", serverID, err)
	}
	order, err := p.client.GetOrder(ctx, p.account.AccountID, orderID)
	if err != nil {
		return nil, err
	}
	if key, err := orderTopicKey(order); err != nil || key != p.id {
		return nil, fmt.Errorf("etrade: order %d is not for option contract %s: %w", orderID, p.id, os.ErrNotExist)
	}
	return p.withClientUUID(order), nil
}

func (p *OptionProduct) Cancel(ctx context.Context, serverID string) error {
	orderID, err := strconv.ParseInt(serverID, 10, 64)
	if err != nil {
		return fmt.Errorf("etrade: invalid server order id %q: %w", serverID, err)
	}
	return p.client.CancelOrder(ctx, p.account.AccountID, orderID)
}

// goMarkDone sets Done on the entry of each of our orders seen finished, so
// opening the contract again doesn't re-track it.
func (p *OptionProduct) goMarkDone(ctx context.Context, sub *topic.Receiver[*internal.Order]) {
	defer p.wg.Done()
	defer sub.Close()

	defer func() {
		if r := recover(); r != nil {
			slog.Error("etrade: CAUGHT PANIC in OptionProduct.goMarkDone", "panic", r)
			slog.Error(string(debug.Stack()))
			panic(r)
		}
	}()

	updatesCh, err := topic.ReceiveCh(sub)
	if err != nil {
		slog.Error("etrade: could not create receive channel for option order updates", "contract", p.id, "err", err)
		return
	}
	marked := make(map[int64]bool)
	for {
		var order *internal.Order
		select {
		case <-ctx.Done():
			return
		case o, ok := <-updatesCh:
			if !ok {
				return
			}
			order = o
		}
		if order.AccountID != p.account.AccountID || !order.IsDone() || marked[order.OrderID] {
			continue
		}
		uid, ok := p.serverIDMap.Load(order.OrderID)
		if !ok {
			continue // not placed by us
		}
		entry, err := getOptionOrderEntry(ctx, p.db, uid)
		if err != nil {
			slog.Warn("etrade: could not read option order entry", "contract", p.id, "client-id", uid, "err", err)
			continue
		}
		if !entry.Done {
			entry.Done = true
			if err := setOptionOrderEntry(ctx, p.db, uid, entry); err != nil {
				slog.Warn("etrade: could not mark option order entry done", "contract", p.id, "client-id", uid, "err", err)
				continue
			}
		}
		marked[order.OrderID] = true
	}
}
