package mockexchange

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

// Exchange is a simulated exchange for backtesting. It is the single source of
// truth for balances across all products. Products call reserve/release/settle
// for all fund movements so cross-product balance correctness is maintained.
type Exchange struct {
	name    string
	feePct decimal.Decimal

	mu           sync.Mutex
	balance      map[string]decimal.Decimal // currency → total balance
	available    map[string]decimal.Decimal // currency → available (unreserved) balance
	productDefs  map[string]*gobs.Product   // productID → metadata
	openProducts map[string]*Product        // productID → open MockProduct
	balTopic     *topic.Topic[exchange.BalanceUpdate]
}

var _ exchange.Exchange = (*Exchange)(nil)

// NewExchange creates a simulated exchange with the given initial balances and
// known product definitions. feePct is applied on each simulated fill
// (e.g. 0.006 for 0.6%).
func NewExchange(name string, feePct decimal.Decimal, balances map[string]decimal.Decimal, products []*gobs.Product) *Exchange {
	defs := make(map[string]*gobs.Product, len(products))
	for _, p := range products {
		defs[p.ProductID] = p
	}
	bal := make(map[string]decimal.Decimal, len(balances))
	avail := make(map[string]decimal.Decimal, len(balances))
	for k, v := range balances {
		bal[k] = v
		avail[k] = v
	}
	return &Exchange{
		name:         name,
		feePct:      feePct,
		balance:      bal,
		available:    avail,
		productDefs:  defs,
		openProducts: make(map[string]*Product),
		balTopic:     topic.New[exchange.BalanceUpdate](),
	}
}

func (e *Exchange) ExchangeName() string       { return e.name }
func (e *Exchange) CanDedupOnClientUUID() bool { return false }

func (e *Exchange) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.openProducts {
		p.Close()
	}
	e.openProducts = nil
	return nil
}

func (e *Exchange) GetBalanceUpdates() (*topic.Receiver[exchange.BalanceUpdate], error) {
	r, err := topic.Subscribe[exchange.BalanceUpdate](e.balTopic, 1, false)
	if err != nil {
		return nil, fmt.Errorf("could not subscribe to balance updates: %w", err)
	}
	return r, nil
}

func (e *Exchange) OpenSpotProduct(ctx context.Context, productID string) (exchange.Product, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if p, ok := e.openProducts[productID]; ok {
		return p, nil
	}
	def, ok := e.productDefs[productID]
	if !ok {
		return nil, fmt.Errorf("product %q is not registered in mock exchange", productID)
	}
	p := newProduct(def, e.feePct, e)
	e.openProducts[productID] = p
	return p, nil
}

func (e *Exchange) GetSpotProduct(ctx context.Context, base, quote string) (*gobs.Product, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, def := range e.productDefs {
		if strings.EqualFold(def.BaseCurrencyID, base) && strings.EqualFold(def.QuoteCurrencyID, quote) {
			return def, nil
		}
	}
	return nil, fmt.Errorf("no product found for %s-%s in mock exchange", base, quote)
}

func (e *Exchange) GetOrder(ctx context.Context, productID string, serverID string) (exchange.OrderDetail, error) {
	e.mu.Lock()
	p, ok := e.openProducts[productID]
	e.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("product %q is not open", productID)
	}
	return p.Get(ctx, serverID)
}

// Balances returns a snapshot of total and available balances per currency.
func (e *Exchange) Balances() (total, available map[string]decimal.Decimal) {
	e.mu.Lock()
	defer e.mu.Unlock()
	tot := make(map[string]decimal.Decimal, len(e.balance))
	avail := make(map[string]decimal.Decimal, len(e.available))
	for k, v := range e.balance {
		tot[k] = v
	}
	for k, v := range e.available {
		avail[k] = v
	}
	return tot, avail
}

// reserve deducts amount from available balance for currency.
// Returns ErrNoFund if insufficient available balance.
func (e *Exchange) reserve(currency string, amount decimal.Decimal) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.available[currency].LessThan(amount) {
		return exchange.ErrNoFund
	}
	e.available[currency] = e.available[currency].Sub(amount)
	e.balTopic.Send(&exchange.SimpleBalance{Symbol: currency, FreeBalance: e.available[currency]})
	return nil
}

// release restores amount to available balance for currency (called on cancel).
func (e *Exchange) release(currency string, amount decimal.Decimal) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.available[currency] = e.available[currency].Add(amount)
	e.balTopic.Send(&exchange.SimpleBalance{Symbol: currency, FreeBalance: e.available[currency]})
}

// settle applies a fill: transfers actual balances and makes proceeds available.
// Reserved funds were already deducted from available at order placement.
func (e *Exchange) settle(side, base, quote string, size, price, fee decimal.Decimal) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if strings.EqualFold(side, "buy") {
		// Reserved quote (size*price + fee) is now consumed; receive base.
		cost := size.Mul(price).Add(fee)
		e.balance[quote] = e.balance[quote].Sub(cost)
		e.balance[base] = e.balance[base].Add(size)
		e.available[base] = e.available[base].Add(size)
	} else {
		// Reserved base (size) is now consumed; proceeds go to quote.
		proceeds := size.Mul(price).Sub(fee)
		e.balance[base] = e.balance[base].Sub(size)
		e.balance[quote] = e.balance[quote].Add(proceeds)
		e.available[quote] = e.available[quote].Add(proceeds)
	}

	e.balTopic.Send(&exchange.SimpleBalance{Symbol: base, FreeBalance: e.available[base]})
	e.balTopic.Send(&exchange.SimpleBalance{Symbol: quote, FreeBalance: e.available[quote]})
}
