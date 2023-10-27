// Copyright (c) 2023 BVK Chaitanya

package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

type OrderID string

type RemoteTime time.Time

type Order struct {
	OrderID OrderID

	ClientOrderID string

	Side string

	CreateTime RemoteTime

	Fee         decimal.Decimal
	FilledSize  decimal.Decimal
	FilledPrice decimal.Decimal

	Status string

	// Done is true if order is complete. DoneReason below indicates if order has
	// failed or succeeded.
	Done bool

	// When Done is true, an empty DoneReason value indicates a successfull
	// execution of the order and a non-empty DoneReason indicates a failure with
	// the reason for the failure.
	DoneReason string
}

type Ticker struct {
	Timestamp RemoteTime
	Price     decimal.Decimal
}

type Product interface {
	ID() string

	TickerCh() <-chan *Ticker
	OrderUpdatesCh(id OrderID) <-chan *Order

	LimitBuy(ctx context.Context, clientOrderID string, size, price decimal.Decimal) (OrderID, error)
	LimitSell(ctx context.Context, clientOrderID string, size, price decimal.Decimal) (OrderID, error)

	Get(ctx context.Context, id OrderID) (*Order, error)
	List(ctx context.Context) ([]*Order, error)
	Cancel(ctx context.Context, id OrderID) error

	// Retire(id OrderID)
}
