// Copyright (c) 2023 BVK Chaitanya

package exchange

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/topic"
)

var ErrNoFund = errors.New("insufficient fund")

type Order interface {
	ServerID() string
	ClientID() uuid.UUID
	OrderSide() string
}

type OrderUpdate interface {
	ServerID() string
	ClientID() uuid.UUID

	CreatedAt() gobs.RemoteTime

	ExecutedFee() decimal.Decimal
	ExecutedSize() decimal.Decimal
	ExecutedValue() decimal.Decimal

	IsDone() bool
	OrderStatus() string
}

type OrderDetail interface {
	Order
	OrderUpdate

	FinishedAt() gobs.RemoteTime
}

type PriceUpdate interface {
	PricePoint() (decimal.Decimal, gobs.RemoteTime)
}

type BalanceUpdate interface {
	Balance() (string, decimal.Decimal)
}

type Product interface {
	io.Closer

	ProductID() string
	ExchangeName() string
	BaseMinSize() decimal.Decimal

	GetPriceUpdates() (*topic.Receiver[PriceUpdate], error)
	GetOrderUpdates() (*topic.Receiver[OrderUpdate], error)

	LimitBuy(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (Order, error)
	LimitSell(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (Order, error)

	Get(ctx context.Context, serverID string) (OrderDetail, error)
	Cancel(ctx context.Context, serverID string) error
}

// Greeks holds the option sensitivities for an options contract.
type Greeks struct {
	Delta decimal.Decimal
	Gamma decimal.Decimal
	Theta decimal.Decimal
	Vega  decimal.Decimal
	Rho   decimal.Decimal
	IV    decimal.Decimal // implied volatility
}

// OptionsProduct represents an open options contract ready for trading.
// Analogous to Product but for options.
type OptionsProduct interface {
	io.Closer

	// Contract identity.
	Symbol() string     // OCC option symbol, e.g. "AAPL  261218C00200000"
	ContractID() string // exchange-native identifier, e.g. IBKR conid
	Underlying() string // underlying ticker, e.g. "AAPL"
	OptionType() string // "CALL" or "PUT"
	Strike() decimal.Decimal
	Expiry() time.Time
	ContractSize() decimal.Decimal // shares per contract, typically 100

	GetPriceUpdates() (*topic.Receiver[PriceUpdate], error)
	GetOrderUpdates() (*topic.Receiver[OrderUpdate], error)

	// GetGreeks fetches a current snapshot of the option sensitivities.
	GetGreeks(ctx context.Context) (*Greeks, error)

	// LimitBuyToOpen / LimitSellToOpen are used to enter a new options position.
	// LimitBuyToClose / LimitSellToClose are used to exit an existing position.
	// numContracts is the number of contracts (not shares).
	LimitBuyToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (Order, error)
	LimitSellToOpen(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (Order, error)
	LimitBuyToClose(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (Order, error)
	LimitSellToClose(ctx context.Context, clientID uuid.UUID, numContracts, limitPrice decimal.Decimal) (Order, error)

	Get(ctx context.Context, serverID string) (OrderDetail, error)
	Cancel(ctx context.Context, serverID string) error
}

type Exchange interface {
	io.Closer

	ExchangeName() string

	// GetBalanceUpdates is a channel that sends update notifications
	// asynchronously when any asset balance (available for orders) changes on
	// the exchange.
	GetBalanceUpdates() (*topic.Receiver[BalanceUpdate], error)

	// CanDedupOnClientUUID returns true if exchange back is able to maintain
	// unique client-id constraint (eg: Coinbase). Must return false, if exchange
	// does not or cannot maintain client id uniqueness.
	//
	// For exchanges that return true, we expect that BUY/SELL orders with same
	// client-uuid will receive the existing or expired or completed, older
	// server order.
	CanDedupOnClientUUID() bool

	OpenSpotProduct(ctx context.Context, productID string) (Product, error)

	GetSpotProduct(ctx context.Context, base, quote string) (*gobs.Product, error)

	GetOrder(ctx context.Context, productID string, serverID string) (OrderDetail, error)

	// SupportsOptions returns true if this exchange supports options trading.
	// If false, GetOptionChain, GetOptionsProduct, and OpenOptionsProduct
	// return errors.ErrUnsupported.
	SupportsOptions() bool

	// GetOptionChain returns all available option contracts for the given
	// underlying across all expirations and types. Callers filter by expiry,
	// type, or strike as needed.
	GetOptionChain(ctx context.Context, underlying string) ([]*gobs.OptionContract, error)

	// GetOptionsProduct returns a read-only metadata snapshot for a specific
	// contract. Analogous to GetSpotProduct.
	GetOptionsProduct(ctx context.Context, contractID string) (*gobs.OptionContract, error)

	// OpenOptionsProduct opens a specific options contract for active trading
	// (price streams, order placement). Analogous to OpenSpotProduct.
	OpenOptionsProduct(ctx context.Context, contractID string) (OptionsProduct, error)
}
