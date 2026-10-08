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
	Config   *OptLimiterConfig
	Progress *OptLimiterProgress
}

// OptLimiterConfig is fixed at creation.
type OptLimiterConfig struct {
	ExchangeName string
	ContractID   string

	ContractSize decimal.Decimal
	NumContracts decimal.Decimal

	// MinPremium is the per-share floor the order is never re-priced below.
	MinPremium decimal.Decimal

	RepriceStep     decimal.Decimal // fraction of the bid-ask spread per step
	RepriceInterval time.Duration

	ClientIDSeed string
}

// OptLimiterProgress is everything trading writes.
type OptLimiterProgress struct {
	// ClientIDOffset is saved before each order is placed, so any lower ID may be at the broker.
	ClientIDOffset uint64

	// Orders maps server order ID to order; at most one is live at a time.
	Orders map[string]*Order

	// LookupOffset is where recovery starts looking client IDs up at the
	// broker: every lower ID is in Orders or settled absent.
	LookupOffset uint64

	// Absent maps the client IDs from LookupOffset up that aren't in Orders
	// and that the broker hasn't listed.
	Absent map[string]*OptAbsence
}

// OptAbsence is a client ID below the offset that the broker didn't list.
type OptAbsence struct {
	// Since is when its placement failed, or when a lookup first missed it.
	Since time.Time

	// Settled is set once a lookup made the optlimiter's absentSettle after
	// Since still missed it: the ID counts as never placed and isn't looked
	// up again.
	Settled bool
}
