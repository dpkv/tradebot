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

	// Legs holds each instrument of an order with several: a
	// multi-instrument order, such as the option and stock legs of the order
	// E*TRADE creates for an option assignment, or a multi-leg OCA order.
	// Such an order has no single symbol, side or size, so its
	// per-instrument fields above are zero and Commission is the legs'
	// total. See NewOrderFromAPI.
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

// NewOrderFromAPI converts an E*TRADE API order into an Order. The
// ClientUUID field is left as uuid.Nil and must be set by the caller.
//
// An order with one instrument fills the flat per-instrument fields. An
// order with several, across all its order details, has them in Legs
// instead and its per-instrument fields zero: that covers a multi-instrument
// order (the one E*TRADE creates for an option assignment, say) and a
// multi-leg OCA order. Such an order shares one orderId with no per-leg
// server ID, so it can't be tracked as an exchange.Order; callers that track
// orders skip it (see HasLegs).
func NewOrderFromAPI(accountID string, a *APIOrder) *Order {
	o := &Order{
		AccountID:     accountID,
		OrderID:       a.OrderID,
		ClientOrderID: a.ClientOrderID,
	}
	// An OCA order's details differ; their fields are on its legs.
	if len(a.OrderDetail) == 1 {
		o.setDetail(&a.OrderDetail[0])
	}

	var legs []*OrderLeg
	for i := range a.OrderDetail {
		d := &a.OrderDetail[i]
		for j := range d.Instrument {
			legs = append(legs, newOrderLeg(d, &d.Instrument[j]))
		}
	}
	switch len(legs) {
	case 0:
	case 1:
		o.setInstrument(legs[0])
	default:
		for _, leg := range legs {
			o.Commission = o.Commission.Add(leg.Commission)
		}
		o.Legs = legs
	}
	return o
}

// setDetail copies the order-level fields of an order detail.
func (o *Order) setDetail(d *apiOrderDetail) {
	o.Status = d.Status
	o.PriceType = d.PriceType
	o.OrderTerm = d.OrderTerm
	o.PlacedTimeMilli = d.PlacedTime
	o.ExecutedTimeMilli = d.ExecutedTime
	o.LimitPrice = d.LimitPrice
}

// setInstrument copies the per-instrument fields of an order's only leg.
func (o *Order) setInstrument(leg *OrderLeg) {
	o.Symbol = leg.Symbol
	o.SecurityType = leg.SecurityType
	o.Side = leg.Side
	o.OrderedQty = leg.OrderedQty
	o.FilledQty = leg.FilledQty
	o.AvgFillPrice = leg.AvgFillPrice
	o.Commission = leg.Commission
	o.ExpiryYear = leg.ExpiryYear
	o.ExpiryMonth = leg.ExpiryMonth
	o.ExpiryDay = leg.ExpiryDay
	o.StrikePrice = leg.StrikePrice
	o.CallPut = leg.CallPut
}

// HasLegs reports whether the order has several instruments, held in Legs.
// Such an order can't be tracked as an exchange.Order.
func (o *Order) HasLegs() bool {
	return len(o.Legs) > 0
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
