// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"strconv"
	"strings"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// APIOrdersResponse is the JSON wrapper returned by
// GET /v1/api/iserver/account/orders.
type APIOrdersResponse struct {
	Orders []*APIOrder `json:"orders"`
}

// APIOrder is the per-order JSON structure from the IBKR CP API orders list.
type APIOrder struct {
	OrderID           int64           `json:"orderId"`
	ConID             int             `json:"conid"`
	Ticker            string          `json:"ticker"`
	SecType           string          `json:"secType"`
	Side              string          `json:"side"`
	Status            string          `json:"status"`
	TotalSize         decimal.Decimal `json:"totalSize"`
	FilledQuantity    decimal.Decimal `json:"filledQuantity"`
	RemainingQuantity decimal.Decimal `json:"remainingQuantity"`
	AvgPrice          decimal.Decimal `json:"avgPrice"`
	Price             decimal.Decimal `json:"price"`
	// LastExecutionTime is the last event timestamp in milliseconds (field
	// "lastExecutionTime_r" in the JSON response).
	LastExecutionTime int64  `json:"lastExecutionTime_r"`
	TimeInForce       string `json:"timeInForce"`
	// ClientOrderID is the cOID field set when the order was placed.
	ClientOrderID   string `json:"cOID"`
	ListingExchange string `json:"listingExchange"`
}

// Order is a flat representation of an IBKR order. It implements
// exchange.Order, exchange.OrderUpdate and exchange.OrderDetail.
//
// Because IBKR's cOID field accepts strings up to 50 chars, UUIDs fit
// directly — no sequential counter or DB mapping is needed.
type Order struct {
	OrderID       int64
	ClientOrderID string // UUID string stored as cOID

	Symbol string // equity ticker, e.g. "AAPL"
	Side   string // "BUY" or "SELL"

	Status string // IBKR status, e.g. "Filled", "Submitted", "Cancelled"

	LastExecutionTimeMilli int64

	LimitPrice   decimal.Decimal
	OrderedQty   decimal.Decimal
	FilledQty    decimal.Decimal
	AvgFillPrice decimal.Decimal
}

var _ exchange.Order = &Order{}
var _ exchange.OrderUpdate = &Order{}
var _ exchange.OrderDetail = &Order{}

func (o *Order) ServerID() string {
	return strconv.FormatInt(o.OrderID, 10)
}

func (o *Order) ClientID() uuid.UUID {
	id, err := uuid.Parse(o.ClientOrderID)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func (o *Order) OrderSide() string {
	return strings.ToUpper(o.Side)
}

func (o *Order) CreatedAt() gobs.RemoteTime {
	if o.LastExecutionTimeMilli == 0 {
		return gobs.RemoteTime{}
	}
	return gobs.RemoteTime{Time: time.UnixMilli(o.LastExecutionTimeMilli)}
}

func (o *Order) ExecutedSize() decimal.Decimal {
	return o.FilledQty
}

func (o *Order) ExecutedValue() decimal.Decimal {
	return o.FilledQty.Mul(o.AvgFillPrice)
}

// ExecutedFee returns zero. The IBKR orders list API does not include
// commission data. Use the Flex Query API for accurate fee reporting.
func (o *Order) ExecutedFee() decimal.Decimal {
	return decimal.Zero
}

func (o *Order) IsDone() bool {
	switch strings.ToLower(o.Status) {
	case "filled", "cancelled", "inactive":
		return true
	}
	return false
}

func (o *Order) OrderStatus() string {
	return o.Status
}

func (o *Order) FinishedAt() gobs.RemoteTime {
	if o.IsDone() && o.LastExecutionTimeMilli != 0 {
		return gobs.RemoteTime{Time: time.UnixMilli(o.LastExecutionTimeMilli)}
	}
	return gobs.RemoteTime{}
}

// NewOrderFromAPI converts an APIOrder into a flat Order.
func NewOrderFromAPI(a *APIOrder) *Order {
	return &Order{
		OrderID:                a.OrderID,
		ClientOrderID:          a.ClientOrderID,
		Symbol:                 a.Ticker,
		Side:                   strings.ToUpper(a.Side),
		Status:                 a.Status,
		LastExecutionTimeMilli: a.LastExecutionTime,
		LimitPrice:             a.Price,
		OrderedQty:             a.TotalSize,
		FilledQty:              a.FilledQuantity,
		AvgFillPrice:           a.AvgPrice,
	}
}
