// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"fmt"
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
	// Snapshot is false while the gateway is still loading the list, in
	// which case Orders is empty or incomplete. The first request with a new
	// filter returns false. Nil if the response has no snapshot field.
	Snapshot *bool `json:"snapshot"`
}

// APIOrderStatus is the JSON returned by
// GET /v1/api/iserver/account/order/status/{orderId}. Unlike the orders
// list, it answers for any order the gateway still knows, but it has no
// client order id (order_ref). Numbers are JSON strings, e.g. "100.0".
type APIOrderStatus struct {
	OrderID      int64  `json:"order_id"`
	ConID        int    `json:"conid"`
	Symbol       string `json:"symbol"`
	SecType      string `json:"sec_type"`
	Side         string `json:"side"` // "B" or "S"
	OrderStatus  string `json:"order_status"`
	CumFill      string `json:"cum_fill"`
	TotalSize    string `json:"total_size"`
	LimitPrice   string `json:"limit_price"`
	AveragePrice string `json:"average_price"` // absent until the order has a fill
	// CancelReason starts with "Rejected by System" for an order IBKR
	// refused. The numeric order_ccp_status here doesn't match the orders
	// list's "Rejected", so this is used instead.
	CancelReason string `json:"order_cancellation_by_system_reason"`
}

// NewOrderFromAPIStatus converts an APIOrderStatus into a flat Order. The
// result's ClientOrderID is empty because the endpoint doesn't return it.
func NewOrderFromAPIStatus(a *APIOrderStatus) (*Order, error) {
	parse := func(name, s string) (decimal.Decimal, error) {
		if s == "" {
			return decimal.Zero, nil
		}
		d, err := decimal.NewFromString(s)
		if err != nil {
			return decimal.Zero, fmt.Errorf("ibkr: order %d: invalid %s %q: %w", a.OrderID, name, s, err)
		}
		return d, nil
	}
	filled, err := parse("cum_fill", a.CumFill)
	if err != nil {
		return nil, err
	}
	total, err := parse("total_size", a.TotalSize)
	if err != nil {
		return nil, err
	}
	limit, err := parse("limit_price", a.LimitPrice)
	if err != nil {
		return nil, err
	}
	avg, err := parse("average_price", a.AveragePrice)
	if err != nil {
		return nil, err
	}

	side := strings.ToUpper(a.Side)
	switch side {
	case "B":
		side = "BUY"
	case "S":
		side = "SELL"
	}
	var ccpStatus string
	if strings.HasPrefix(a.CancelReason, "Rejected") {
		ccpStatus = "Rejected"
	}
	return &Order{
		OrderID:      a.OrderID,
		ConID:        a.ConID,
		Symbol:       a.Symbol,
		SecType:      a.SecType,
		Side:         side,
		Status:       a.OrderStatus,
		CCPStatus:    ccpStatus,
		LimitPrice:   limit,
		OrderedQty:   total,
		FilledQty:    filled,
		AvgFillPrice: avg,
	}, nil
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
	// ClientOrderID is the order_ref field in the orders list response.
	// Note: the placement request uses "cOID" but the list response returns "order_ref".
	ClientOrderID   string `json:"order_ref"`
	ListingExchange string `json:"listingExchange"`
	// CCPStatus is the order's status at IBKR's order server. It is "Rejected"
	// for orders IBKR refused (e.g., the 15 working orders per side limit),
	// which Status still reports as "Cancelled".
	CCPStatus string `json:"order_ccp_status"`

	// Unused fields — decoded for completeness but not used by the bot.

	// Account is the IBKR account ID associated with this order.
	Account string `json:"acct"` // unused
	// CashCurrency is the currency used for cash settlement, e.g. "USD".
	CashCurrency string `json:"cashCcy"` // unused
	// OrderType is the human-readable order type, e.g. "Limit", "Market".
	OrderType string `json:"orderType"` // unused
	// OutsideRTH indicates whether the order is active outside regular trading hours.
	OutsideRTH bool `json:"outsideRTH"` // unused
	// CompanyName is the full company name for the instrument.
	CompanyName string `json:"companyName"` // unused
	// LastExecutionTimeStr is the human-readable form of LastExecutionTime.
	// The _r (raw/millis) field is used instead for precision.
	LastExecutionTimeStr string `json:"lastExecutionTime"` // unused
	// ConIDEx is the composite conid+exchange string, e.g. "265598@NASDAQ".
	ConIDEx string `json:"conidex"` // unused
}

// Order is a flat representation of an IBKR order. It implements
// exchange.Order, exchange.OrderUpdate and exchange.OrderDetail.
//
// Because IBKR's cOID field accepts strings up to 50 chars, UUIDs fit
// directly — no sequential counter or DB mapping is needed.
type Order struct {
	OrderID       int64
	ClientOrderID string // UUID string stored as cOID

	ConID   int    // IBKR contract ID — used to route option order updates
	Symbol  string // ticker, e.g. "AAPL"
	SecType string // "STK", "OPT", etc.
	Side    string // "BUY" or "SELL"

	Status string // IBKR status, e.g. "Filled", "Submitted", "Cancelled"
	// CCPStatus is IBKR's order server status (order_ccp_status). It tells a
	// rejected order apart from a cancelled one, since both have Status
	// "Cancelled". Empty for orders saved before this field existed.
	CCPStatus string

	LastExecutionTimeMilli int64
	// FillObservedAt is the wall-clock time (Unix millis) when this bot first
	// observed the order as Done. Set once in goWatchOrderUpdates and never
	// overwritten. Used as FinishedAt instead of LastExecutionTimeMilli, which
	// can be stale because filled orders drop off the IBKR orders list before
	// the next poll captures the updated timestamp.
	FillObservedAt int64
	// Commission is the fee charged for this order, populated from the trades
	// API after the order fills. Zero until the trades API is queried.
	Commission decimal.Decimal

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

func (o *Order) ExecutedFee() decimal.Decimal {
	return o.Commission
}

func (o *Order) IsDone() bool {
	switch strings.ToLower(o.Status) {
	case "filled", "cancelled":
		return true
	}
	return false
}

// IsRejected reports whether IBKR refused the order instead of accepting it.
func (o *Order) IsRejected() bool {
	return strings.EqualFold(o.CCPStatus, "Rejected")
}

func (o *Order) OrderStatus() string {
	return o.Status
}

func (o *Order) FinishedAt() gobs.RemoteTime {
	if o.IsDone() {
		if o.FillObservedAt != 0 {
			return gobs.RemoteTime{Time: time.UnixMilli(o.FillObservedAt)}
		}
		if o.LastExecutionTimeMilli != 0 {
			return gobs.RemoteTime{Time: time.UnixMilli(o.LastExecutionTimeMilli)}
		}
	}
	return gobs.RemoteTime{}
}

// NewOrderFromAPI converts an APIOrder into a flat Order.
func NewOrderFromAPI(a *APIOrder) *Order {
	return &Order{
		OrderID:                a.OrderID,
		ClientOrderID:          a.ClientOrderID,
		ConID:                  a.ConID,
		Symbol:                 a.Ticker,
		SecType:                a.SecType,
		Side:                   strings.ToUpper(a.Side),
		Status:                 a.Status,
		CCPStatus:              a.CCPStatus,
		LastExecutionTimeMilli: a.LastExecutionTime,
		LimitPrice:             a.Price,
		OrderedQty:             a.TotalSize,
		FilledQty:              a.FilledQuantity,
		AvgFillPrice:           a.AvgPrice,
	}
}
