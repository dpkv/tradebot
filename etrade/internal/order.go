// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"encoding/json"
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

	Symbol string // equity ticker, e.g. "AAPL"
	Side   string // "BUY" or "SELL"

	Status string // E*TRADE status string, e.g. "OPEN", "EXECUTED"

	PlacedTimeMilli   int64
	ExecutedTimeMilli int64

	LimitPrice  decimal.Decimal
	OrderedQty  decimal.Decimal
	FilledQty   decimal.Decimal
	AvgFillPrice decimal.Decimal
	Commission  decimal.Decimal

	// ClientUUID is our internal tracking UUID. It is not present in the
	// E*TRADE API response and must be set by the caller.
	ClientUUID uuid.UUID
}

var _ exchange.Order = &Order{}
var _ exchange.OrderUpdate = &Order{}
var _ exchange.OrderDetail = &Order{}

// NewOrderFromAPI converts the nested E*TRADE API order structure into a flat
// Order. Returns nil for order types we don't support (multi-leg OCA orders,
// options, etc.) and logs the skipped order. The ClientUUID field is left as
// uuid.Nil and must be set by the caller.
func NewOrderFromAPI(a *APIOrder) *Order {
	skip := func(reason string) *Order {
		js, _ := json.MarshalIndent(a, "  ", "  ")
		slog.Warn("etrade: skipping order "+strconv.FormatInt(a.OrderID, 10)+" ("+reason+"): \n  "+string(js))
		return nil
	}

	o := &Order{
		OrderID:       a.OrderID,
		ClientOrderID: a.ClientOrderID,
	}
	if len(a.OrderDetail) == 0 {
		return o
	}
	if len(a.OrderDetail) > 1 {
		// TODO: OCA (One Cancels All) and other multi-leg order types are not
		// yet supported. They are silently skipped during polling. If support is
		// needed, parse each leg individually and track them as separate orders.
		return skip("multiple OrderDetail entries (e.g. OCA order)")
	}
	d := &a.OrderDetail[0]
	o.Status = d.Status
	o.PlacedTimeMilli = d.PlacedTime
	o.ExecutedTimeMilli = d.ExecutedTime
	o.LimitPrice = d.LimitPrice

	if len(d.Instrument) == 0 {
		return o
	}
	if len(d.Instrument) > 1 {
		return skip("multiple Instrument entries")
	}
	inst := &d.Instrument[0]
	if inst.Product.SecurityType != "EQ" {
		return skip("non-equity security type: " + inst.Product.SecurityType)
	}
	o.Symbol = inst.Product.Symbol
	o.Side = strings.ToUpper(inst.OrderAction)
	o.OrderedQty = inst.OrderedQuantity
	o.FilledQty = inst.FilledQuantity
	o.AvgFillPrice = inst.AverageExecutionPrice
	o.Commission = inst.EstimatedCommission.Add(inst.EstimatedFees)
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
