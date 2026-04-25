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

// Exchange is a simulated exchange for backtesting. It tracks balances,
// registers known products, and creates MockProducts on demand.
type Exchange struct {
	name    string
	feeRate decimal.Decimal

	mu           sync.Mutex
	balances     map[string]decimal.Decimal      // currency → available balance
	productDefs  map[string]*gobs.Product        // productID → metadata
	openProducts map[string]*Product             // productID → open MockProduct
	balTopic     *topic.Topic[exchange.BalanceUpdate]
}

var _ exchange.Exchange = (*Exchange)(nil)

// NewExchange creates a simulated exchange with the given initial balances and
// known product definitions. feeRate is applied on each simulated fill
// (e.g. 0.006 for 0.6%).
func NewExchange(name string, feeRate decimal.Decimal, balances map[string]decimal.Decimal, products []*gobs.Product) *Exchange {
	defs := make(map[string]*gobs.Product, len(products))
	for _, p := range products {
		defs[p.ProductID] = p
	}
	bal := make(map[string]decimal.Decimal, len(balances))
	for k, v := range balances {
		bal[k] = v
	}
	return &Exchange{
		name:         name,
		feeRate:      feeRate,
		balances:     bal,
		productDefs:  defs,
		openProducts: make(map[string]*Product),
		balTopic:     topic.New[exchange.BalanceUpdate](),
	}
}

func (e *Exchange) ExchangeName() string { return e.name }

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
	p := newProduct(def, e.feeRate, e.applyFill)
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

// Balances returns a snapshot of current balances (currency → amount).
func (e *Exchange) Balances() map[string]decimal.Decimal {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]decimal.Decimal, len(e.balances))
	for k, v := range e.balances {
		out[k] = v
	}
	return out
}

// applyFill is called by MockProduct when an order fills. It debits/credits
// balances and emits a BalanceUpdate for each affected currency.
func (e *Exchange) applyFill(side string, size, price, fee decimal.Decimal, base, quote string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	value := size.Mul(price)
	if strings.EqualFold(side, "buy") {
		cost := value.Add(fee)
		if e.balances[quote].LessThan(cost) {
			return exchange.ErrNoFund
		}
		e.balances[quote] = e.balances[quote].Sub(cost)
		e.balances[base] = e.balances[base].Add(size)
	} else {
		if e.balances[base].LessThan(size) {
			return exchange.ErrNoFund
		}
		e.balances[base] = e.balances[base].Sub(size)
		e.balances[quote] = e.balances[quote].Add(value.Sub(fee))
	}

	e.balTopic.Send(&exchange.SimpleBalance{Symbol: base, FreeBalance: e.balances[base]})
	e.balTopic.Send(&exchange.SimpleBalance{Symbol: quote, FreeBalance: e.balances[quote]})
	return nil
}
