// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bvk/tradebot/etrade/autologin"
	"github.com/bvk/tradebot/etrade/internal"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// The exchange.OptionsAccount methods and GetStockHolding on ExchangeAccount
// (greel/etrade-options-story.md). Market data (the chain and quotes) comes
// from the shared client; orders, settlement and holdings are per account.

var _ exchange.OptionsAccount = &ExchangeAccount{}

// optionContractFromQuote converts an option quote into the contract
// metadata greel reads.
func optionContractFromQuote(q *internal.OptionQuote) (*gobs.OptionContract, error) {
	k, err := newContract(q.Symbol, q.CallPut, q.Year, q.Month, q.Day, q.Strike)
	if err != nil {
		return nil, err
	}
	price, _ := q.PricePoint()
	return &gobs.OptionContract{
		ContractID:        k.ID(),
		Underlying:        k.Root,
		OptionType:        k.CallPut,
		Strike:            k.Strike,
		Expiry:            k.Expiry(),
		ContractSize:      contractSize,
		Price:             price,
		Bid:               q.Bid,
		Ask:               q.Ask,
		Volume:            q.Volume,
		OpenInterest:      q.OpenInterest,
		ImpliedVolatility: q.IV,
	}, nil
}

// GetOptionsChain returns every standard call and put on the underlying,
// across all expiries and strikes, from the client's shared chain cache, so
// the snapshot can be up to Options.ChainCacheTTL old.
func (ea *ExchangeAccount) GetOptionsChain(ctx context.Context, underlying string) ([]*gobs.OptionContract, error) {
	quotes, err := ea.exchange.client.GetCachedOptionChain(ctx, underlying)
	if err != nil {
		return nil, err
	}
	chain := make([]*gobs.OptionContract, 0, len(quotes))
	for _, q := range quotes {
		c, err := optionContractFromQuote(q)
		if err != nil {
			slog.Warn("etrade: skipping option chain entry with an invalid contract", "underlying", underlying, "err", err)
			continue
		}
		chain = append(chain, c)
	}
	return chain, nil
}

// GetOptionsProduct returns a live snapshot of one contract. Never cached:
// optlimiter re-prices from it.
func (ea *ExchangeAccount) GetOptionsProduct(ctx context.Context, contractID string) (*gobs.OptionContract, error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return nil, err
	}
	q, err := ea.exchange.client.GetOptionQuote(ctx, k.QuoteSymbol())
	if err != nil {
		return nil, err
	}
	return optionContractFromQuote(q)
}

// OpenOptionsProduct opens the contract for trading in this account. Opens
// of the same contract in the same account share one OptionProduct; it is
// closed when every returned product has been closed.
func (ea *ExchangeAccount) OpenOptionsProduct(ctx context.Context, contractID string) (exchange.OptionsProduct, error) {
	return ea.exchange.openOptionProduct(ctx, ea.account, contractID)
}

// GetOptionsOrderByClientID finds the option order this account placed with
// the client ID, in any status. See lookupOptionOrder.
func (ea *ExchangeAccount) GetOptionsOrderByClientID(ctx context.Context, clientID uuid.UUID) (exchange.OrderDetail, error) {
	order, err := lookupOptionOrder(ctx, ea.exchange.db, ea.exchange.client, ea.account.AccountID, clientID)
	if err != nil {
		return nil, err
	}
	// A recovered order is new to an open product on the contract: let it
	// restore the client ID on updates and track the order.
	if key, err := orderTopicKey(order); err == nil {
		if p := ea.exchange.getOpenOptionProduct(ea.account.AccountID, key); p != nil {
			p.adopt(order)
		}
	}
	return order, nil
}

// GetOptionsSettlement reports how a contract this account wrote stands,
// from the account's transactions. See Client.getOptionsSettlement.
func (ea *ExchangeAccount) GetOptionsSettlement(ctx context.Context, contractID string) (*exchange.OptionsSettlement, error) {
	return ea.exchange.client.getOptionsSettlement(ctx, ea.account, contractID)
}

// GetStockHolding returns how many shares of the symbol this account holds,
// settled or not. It implements greelladder.StockHoldings.
func (ea *ExchangeAccount) GetStockHolding(ctx context.Context, productID string) (decimal.Decimal, error) {
	return ea.exchange.client.GetStockHolding(ctx, ea.account.AccountID, productID)
}

// sharedOptionProduct is an open OptionProduct and how many opens hold it.
type sharedOptionProduct struct {
	product *OptionProduct
	refs    int
}

// optionProductHandle is what OpenOptionsProduct returns: the shared
// product, with a Close that releases this open only.
type optionProductHandle struct {
	*OptionProduct

	once    sync.Once
	release func()
}

func (h *optionProductHandle) Close() error {
	h.once.Do(h.release)
	return nil
}

func optionProductKey(accountID, contractID string) string {
	return accountID + "/" + contractID
}

func (v *Exchange) openOptionProduct(ctx context.Context, account autologin.Account, contractID string) (exchange.OptionsProduct, error) {
	key := optionProductKey(account.AccountID, contractID)

	v.optionMu.Lock()
	defer v.optionMu.Unlock()

	s, ok := v.optionProducts[key]
	if !ok {
		p, err := newOptionProduct(ctx, v.db, v.client, account, contractID)
		if err != nil {
			return nil, err
		}
		s = &sharedOptionProduct{product: p}
		if v.optionProducts == nil {
			v.optionProducts = make(map[string]*sharedOptionProduct)
		}
		v.optionProducts[key] = s
	}
	s.refs++
	return &optionProductHandle{OptionProduct: s.product, release: func() { v.releaseOptionProduct(key, s) }}, nil
}

func (v *Exchange) releaseOptionProduct(key string, s *sharedOptionProduct) {
	v.optionMu.Lock()
	s.refs--
	last := s.refs == 0
	if last && v.optionProducts[key] == s {
		delete(v.optionProducts, key)
	}
	v.optionMu.Unlock()

	if last {
		s.product.Close()
	}
}

// getOpenOptionProduct returns the account's open product on the contract,
// or nil.
func (v *Exchange) getOpenOptionProduct(accountID, contractID string) *OptionProduct {
	v.optionMu.Lock()
	defer v.optionMu.Unlock()

	if s, ok := v.optionProducts[optionProductKey(accountID, contractID)]; ok {
		return s.product
	}
	return nil
}

// Settlement outcomes, as exchange.OptionsSettlement.Status.
const (
	settlementOpen     = "open"
	settlementAssigned = "assigned"
	settlementExpired  = "expired"
)

// settlementKind classifies a transaction type on the contract: "assigned",
// "expired", "trade" (an opening sale or a closing buy) or "" for a type
// we don't know. E*TRADE doesn't document the strings, so substrings are
// matched to survive the likely spellings.
func settlementKind(txType string) string {
	t := strings.ToLower(txType)
	switch {
	case strings.Contains(t, "assign"):
		return settlementAssigned
	case strings.Contains(t, "expir"):
		return settlementExpired
	case strings.Contains(t, "bought"), strings.Contains(t, "sold"):
		return "trade"
	}
	return ""
}

// isContractTransaction reports whether the transaction's product is the
// contract.
func isContractTransaction(tx *internal.Transaction, k *contract) bool {
	return tx.SecurityType == "OPTN" && tx.Symbol == k.Root && tx.CallPut == k.CallPut &&
		tx.Year == k.Year && tx.Month == k.Month && tx.Day == k.Day && tx.Strike.Equal(k.Strike)
}

// getOptionsSettlement reads the account's transactions over the
// SettlementLookback and reports the contract "assigned" or "expired" from
// an explicit broker record, else "open". When both appear (a position
// partly assigned and the rest expired) the answer is "assigned", with the
// assigned count, since that moves shares. A transaction on the contract of
// a type we don't know is logged and sent as an alert once per process; the
// answer stays "open" until a person looks, which keeps the position
// settling.
func (c *Client) getOptionsSettlement(ctx context.Context, account autologin.Account, contractID string) (*exchange.OptionsSettlement, error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	txs, err := c.ListTransactions(ctx, account.AccountID, now.Add(-c.opts.SettlementLookback), now)
	if err != nil {
		return nil, err
	}

	results := map[string]*exchange.OptionsSettlement{}
	for _, tx := range txs {
		if !isContractTransaction(tx, k) {
			continue
		}
		kind := settlementKind(tx.Type)
		switch kind {
		case "trade":
			continue
		case "":
			c.alertUnknownSettlement(ctx, account, contractID, tx)
			continue
		}
		at := time.UnixMilli(tx.DateMilli)
		r, ok := results[kind]
		if !ok {
			r = &exchange.OptionsSettlement{Status: kind}
			results[kind] = r
		}
		r.Contracts = r.Contracts.Add(tx.Quantity.Abs())
		r.Fee = r.Fee.Add(tx.Fee)
		if !at.Before(r.At) {
			r.Key, r.At = strconv.FormatInt(tx.ID, 10), at
		}
	}
	if r, ok := results[settlementAssigned]; ok {
		return r, nil
	}
	if r, ok := results[settlementExpired]; ok {
		return r, nil
	}
	return &exchange.OptionsSettlement{Status: settlementOpen}, nil
}

// alertUnknownSettlement logs a transaction on a contract whose type the
// settlement matcher doesn't know, and alerts through the messenger once per
// transaction per process.
func (c *Client) alertUnknownSettlement(ctx context.Context, account autologin.Account, contractID string, tx *internal.Transaction) {
	if _, loaded := c.settlementAlerted.LoadOrStore(tx.ID, true); loaded {
		return
	}
	slog.Warn("etrade: unknown transaction type on an option contract; settlement stays open until checked",
		"account", account.AccountName, "contract", contractID, "transaction-id", tx.ID, "type", tx.Type, "description", tx.Description)
	if c.opts.Messenger != nil {
		c.opts.Messenger.SendMessage(ctx, time.Now(), "E*TRADE: unknown transaction type %q on option %s in account %s (transaction %d); its settlement stays open until checked",
			tx.Type, contractID, account.AccountName, tx.ID)
	}
}

// GetStockHolding sums the quantity of the account's stock positions in the
// symbol from the latest portfolio snapshot, which goTrackPortfolio refreshes
// every minute, fetching the portfolio itself only when there is no snapshot
// yet. The portfolio is on a trade-date basis, so unsettled buys and sells
// count. Option positions are skipped. No position is zero, not an error.
func (c *Client) GetStockHolding(ctx context.Context, accountID, symbol string) (decimal.Decimal, error) {
	if _, err := c.creds.Load().GetAccountIDKey(accountID); err != nil {
		return decimal.Zero, err
	}
	positions, ok := c.positionsMap.Load(accountID)
	if !ok {
		var err error
		if positions, err = c.refreshPortfolio(ctx, accountID); err != nil {
			return decimal.Zero, fmt.Errorf("etrade: could not fetch portfolio for stock holding: %w", err)
		}
	}
	var sum decimal.Decimal
	for _, p := range positions {
		if p.isEquity() && strings.EqualFold(p.Product.Symbol, symbol) {
			sum = sum.Add(p.Quantity)
		}
	}
	return sum, nil
}

// adopt records an order recovered by client ID, so updates carry its client
// ID and, while live, it is tracked to completion.
func (p *OptionProduct) adopt(order *internal.Order) {
	if order.AccountID != p.account.AccountID || order.ClientUUID == uuid.Nil {
		return
	}
	if _, loaded := p.serverIDMap.LoadOrStore(order.OrderID, order.ClientUUID); loaded {
		return
	}
	if !order.IsDone() {
		p.client.TrackOrder(p.account.AccountID, order.OrderID)
	}
}
