// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// The following nested types mirror the E*TRADE JSON response structure for
// orders. They are used only for unmarshaling and are not exposed outside this
// package. The public Order type below is a flat representation derived from
// these.

type apiProduct struct {
	Symbol       string `json:"symbol"`
	SecurityType string `json:"securityType"`

	// Unused: only relevant for options orders, not equity.
	CallPut     string          `json:"callPut"`
	ExpiryYear  int             `json:"expiryYear"`
	ExpiryMonth int             `json:"expiryMonth"`
	ExpiryDay   int             `json:"expiryDay"`
	StrikePrice decimal.Decimal `json:"strikePrice"`
}

type apiInstrument struct {
	Product               apiProduct      `json:"Product"`
	OrderAction           string          `json:"orderAction"`
	OrderedQuantity       decimal.Decimal `json:"orderedQuantity"`
	FilledQuantity        decimal.Decimal `json:"filledQuantity"`
	AverageExecutionPrice decimal.Decimal `json:"averageExecutionPrice"`
	EstimatedCommission   decimal.Decimal `json:"estimatedCommission"`
	EstimatedFees         decimal.Decimal `json:"estimatedFees"`

	// Unused: human-readable company name, e.g. "APPLE INC".
	SymbolDescription string `json:"symbolDescription"`
	// Unused: always "QUANTITY" for our limit orders; "DOLLAR" for dollar-based orders.
	QuantityType string `json:"quantityType"`
	// Unused: market snapshot fields present in the response but not order data.
	Bid       decimal.Decimal `json:"bid"`
	Ask       decimal.Decimal `json:"ask"`
	LastPrice decimal.Decimal `json:"lastprice"`
}

type apiOrderDetail struct {
	PlacedTime   int64           `json:"placedTime"`
	ExecutedTime int64           `json:"executedTime"`
	Status       string          `json:"status"`
	PriceType    string          `json:"priceType"`
	LimitPrice   decimal.Decimal `json:"limitPrice"`
	Instrument   []apiInstrument `json:"Instrument"`

	// Unused: order term, e.g. "GOOD_FOR_DAY", "IMMEDIATE_OR_CANCEL".
	OrderTerm string `json:"orderTerm"`
	// Unused: total notional value of the order.
	OrderValue decimal.Decimal `json:"orderValue"`
	// Unused: only relevant for stop and stop-limit orders.
	StopPrice      decimal.Decimal `json:"stopPrice"`
	StopLimitPrice decimal.Decimal `json:"stopLimitPrice"`
}

// APIOrder is the top-level E*TRADE order as it appears in JSON responses. It
// is exported so that response-level structs in client.go can embed it.
type APIOrder struct {
	OrderID       int64            `json:"orderId"`
	ClientOrderID string           `json:"clientOrderId"`
	OrderDetail   []apiOrderDetail `json:"OrderDetail"`

	// Unused: URL to the order details endpoint.
	Details string `json:"details"`
	// Unused: order type, e.g. "EQ" for equity, "OPTN" for options.
	OrderType string `json:"orderType"`
	// Unused: top-level rollups; we use instrument-level values instead.
	TotalOrderValue decimal.Decimal `json:"totalOrderValue"`
	TotalCommission decimal.Decimal `json:"totalCommission"`
}

// Order is a flat representation of an E*TRADE equity order. It implements
// exchange.Order, exchange.OrderUpdate and exchange.OrderDetail.
//
// Because E*TRADE's clientOrderId field is a numeric string (not a UUID), the
// ClientUUID field cannot be derived from the API response. It must be set
// externally by the caller after constructing the Order — typically by looking
// up the sequential clientOrderId in a local map maintained by Product.
type Order struct {
	OrderID       int64
	ClientOrderID string // E*TRADE's numeric sequential id, not a UUID

	Symbol       string // equity ticker, e.g. "AAPL"
	SecurityType string // "EQ", "OPTN", etc.
	Side         string // "BUY" or "SELL"

	Status    string // E*TRADE status string, e.g. "OPEN", "EXECUTED"
	PriceType string // e.g. "LIMIT"
	OrderTerm string // e.g. "GOOD_UNTIL_CANCEL", "GOOD_FOR_DAY"

	PlacedTimeMilli   int64
	ExecutedTimeMilli int64

	LimitPrice   decimal.Decimal
	OrderedQty   decimal.Decimal
	FilledQty    decimal.Decimal
	AvgFillPrice decimal.Decimal
	Commission   decimal.Decimal

	// Option-specific fields; zero for non-option orders.
	ExpiryYear  int
	ExpiryMonth int
	ExpiryDay   int
	StrikePrice decimal.Decimal
	CallPut     string // "CALL" or "PUT"

	// AccountID is the ETrade account identifier. Order ids are only unique
	// within the account id namespace, so account ids must be matched before
	// using an order id.
	AccountID string

	// ClientUUID is our internal tracking UUID. It is not present in the
	// E*TRADE API response and must be set by the caller.
	ClientUUID uuid.UUID

	// Legs holds each instrument of an order NewOrderFromAPI can't represent:
	// a multi-instrument order, such as the option and stock legs of the
	// order E*TRADE creates for an option assignment, or a multi-leg OCA
	// order. Only NewOrderLegsFromAPI fills it (Client.GetOrder returns such
	// orders). Such an order has no single symbol, side or size, so its
	// per-instrument fields above are zero.
	Legs []*OrderLeg `json:",omitempty"`
}

// OrderLeg is one instrument of an order with several. Status through
// ExecutedTimeMilli come from the instrument's order detail: the legs of a
// multi-instrument order share them, while each leg of an OCA order has its
// own.
type OrderLeg struct {
	Status            string
	PriceType         string
	OrderTerm         string
	LimitPrice        decimal.Decimal
	PlacedTimeMilli   int64
	ExecutedTimeMilli int64

	Symbol       string
	SecurityType string
	Side         string

	OrderedQty   decimal.Decimal
	FilledQty    decimal.Decimal
	AvgFillPrice decimal.Decimal
	Commission   decimal.Decimal

	// Option-specific fields; zero for non-option legs.
	ExpiryYear  int
	ExpiryMonth int
	ExpiryDay   int
	StrikePrice decimal.Decimal
	CallPut     string
}

func newOrderLeg(d *apiOrderDetail, inst *apiInstrument) *OrderLeg {
	return &OrderLeg{
		Status:            d.Status,
		PriceType:         d.PriceType,
		OrderTerm:         d.OrderTerm,
		LimitPrice:        d.LimitPrice,
		PlacedTimeMilli:   d.PlacedTime,
		ExecutedTimeMilli: d.ExecutedTime,

		Symbol:       inst.Product.Symbol,
		SecurityType: inst.Product.SecurityType,
		Side:         strings.ToUpper(inst.OrderAction),
		OrderedQty:   inst.OrderedQuantity,
		FilledQty:    inst.FilledQuantity,
		AvgFillPrice: inst.AverageExecutionPrice,
		Commission:   inst.EstimatedCommission.Add(inst.EstimatedFees),
		ExpiryYear:   inst.Product.ExpiryYear,
		ExpiryMonth:  inst.Product.ExpiryMonth,
		ExpiryDay:    inst.Product.ExpiryDay,
		StrikePrice:  inst.Product.StrikePrice,
		CallPut:      inst.Product.CallPut,
	}
}

var _ exchange.Order = &Order{}
var _ exchange.OrderUpdate = &Order{}
var _ exchange.OrderDetail = &Order{}

func skip(orderID int64, reason string) *Order {
	slog.Warn("etrade: skipping order", "orderID", orderID, "reason", reason)
	return nil
}

// NewOrderFromAPI converts an E*TRADE API order into a flat Order. Returns nil
// if the order is unsupported (multi-leg OCA or multi-instrument spread): both
// share a single orderId with no per-leg server ID, so they cannot be modeled
// as independent orders. The ClientUUID field is left as uuid.Nil and must be
// set by the caller.
func NewOrderFromAPI(accountID string, a *APIOrder) *Order {
	if len(a.OrderDetail) == 0 {
		return &Order{
			AccountID:     accountID,
			OrderID:       a.OrderID,
			ClientOrderID: a.ClientOrderID,
		}
	}

	if len(a.OrderDetail) > 1 {
		return skip(a.OrderID, "multi-leg OCA orders are not supported")
	}

	d := a.OrderDetail[0]

	if len(d.Instrument) == 0 {
		return &Order{
			AccountID:         accountID,
			OrderID:           a.OrderID,
			ClientOrderID:     a.ClientOrderID,
			Status:            d.Status,
			PlacedTimeMilli:   d.PlacedTime,
			ExecutedTimeMilli: d.ExecutedTime,
			LimitPrice:        d.LimitPrice,
		}
	}

	if len(d.Instrument) > 1 {
		return skip(a.OrderID, "multi-instrument spread orders are not supported")
	}

	leg := newOrderLeg(&d, &d.Instrument[0])
	return &Order{
		AccountID:         accountID,
		OrderID:           a.OrderID,
		ClientOrderID:     a.ClientOrderID,
		Status:            d.Status,
		PriceType:         d.PriceType,
		OrderTerm:         d.OrderTerm,
		PlacedTimeMilli:   d.PlacedTime,
		ExecutedTimeMilli: d.ExecutedTime,
		LimitPrice:        d.LimitPrice,
		Symbol:            leg.Symbol,
		SecurityType:      leg.SecurityType,
		Side:              leg.Side,
		OrderedQty:        leg.OrderedQty,
		FilledQty:         leg.FilledQty,
		AvgFillPrice:      leg.AvgFillPrice,
		Commission:        leg.Commission,
		ExpiryYear:        leg.ExpiryYear,
		ExpiryMonth:       leg.ExpiryMonth,
		ExpiryDay:         leg.ExpiryDay,
		StrikePrice:       leg.StrikePrice,
		CallPut:           leg.CallPut,
	}
}

// NewOrderLegsFromAPI converts an E*TRADE order that NewOrderFromAPI can't
// represent into an Order with one entry in Legs per instrument, across all
// of its order details, and its Commission the legs' total. That covers a
// multi-instrument order (an option assignment's, say) and a multi-leg OCA
// order. The order-level detail fields (Status, LimitPrice and so on) are set
// only when the order has a single detail; an OCA order's are on its legs.
// Its per-instrument fields are zero, so as an exchange.Order it reads as
// unfilled. Returns nil for an order with at most one detail and one
// instrument, which NewOrderFromAPI handles.
func NewOrderLegsFromAPI(accountID string, a *APIOrder) *Order {
	if len(a.OrderDetail) == 0 || (len(a.OrderDetail) == 1 && len(a.OrderDetail[0].Instrument) < 2) {
		return nil
	}
	o := &Order{
		AccountID:     accountID,
		OrderID:       a.OrderID,
		ClientOrderID: a.ClientOrderID,
	}
	if len(a.OrderDetail) == 1 {
		d := &a.OrderDetail[0]
		o.Status = d.Status
		o.PriceType = d.PriceType
		o.OrderTerm = d.OrderTerm
		o.PlacedTimeMilli = d.PlacedTime
		o.ExecutedTimeMilli = d.ExecutedTime
		o.LimitPrice = d.LimitPrice
	}
	for i := range a.OrderDetail {
		d := &a.OrderDetail[i]
		for j := range d.Instrument {
			leg := newOrderLeg(d, &d.Instrument[j])
			o.Commission = o.Commission.Add(leg.Commission)
			o.Legs = append(o.Legs, leg)
		}
	}
	return o
}

func (o *Order) ServerID() string {
	return strconv.FormatInt(o.OrderID, 10)
}

func (o *Order) ClientID() uuid.UUID {
	return o.ClientUUID
}

func (o *Order) OrderSide() string {
	return o.Side
}

func (o *Order) CreatedAt() gobs.RemoteTime {
	return gobs.RemoteTime{Time: time.UnixMilli(o.PlacedTimeMilli)}
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
	switch strings.ToUpper(o.Status) {
	case "EXECUTED", "CANCELLED", "REJECTED", "EXPIRED", "PARTIAL_CANCEL":
		return true
	}
	return false
}

func (o *Order) OrderStatus() string {
	return o.Status
}

func (o *Order) FinishedAt() gobs.RemoteTime {
	if o.IsDone() && o.ExecutedTimeMilli != 0 {
		return gobs.RemoteTime{Time: time.UnixMilli(o.ExecutedTimeMilli)}
	}
	return gobs.RemoteTime{}
}
