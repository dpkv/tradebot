// Copyright (c) 2026 Deepak Vankadaru

package gobs

import (
	"time"

	"github.com/shopspring/decimal"
)

type OptLimiterState struct {
	V1 *OptLimiterStateV1
}

type OptLimiterStateV1 struct {
	ExchangeName string
	ContractID   string

	ContractSize decimal.Decimal
	NumContracts decimal.Decimal

	// MinPremium is the per-share floor the order is never re-priced below.
	MinPremium decimal.Decimal

	RepriceStep     decimal.Decimal // fraction of the bid-ask spread per step
	RepriceInterval time.Duration

	// ClientIDOffset is saved before each order is placed, so any lower ID may be at the broker.
	ClientIDSeed   string
	ClientIDOffset uint64

	// Orders maps server order ID to order; at most one is live at a time.
	Orders map[string]*Order
}
