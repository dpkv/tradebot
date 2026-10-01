// Copyright (c) 2026 Deepak Vankadaru

package gobs

import (
	"time"

	"github.com/shopspring/decimal"
)

type OptPositionState struct {
	V1 *OptPositionStateV1
}

type OptPositionStateV1 struct {
	ExchangeName string
	Underlying   string

	// Contract caches the current attempt's contract; each leg's own record is authoritative.
	Contract *OptionContract

	// Legs are optlimiter UIDs in order; only the last one may have filled.
	Legs []string

	Outcome   string // "" while open or settling; else "expired", "assigned" or "unfilled"
	OutcomeAt time.Time

	// Assignment is set iff Outcome is "assigned".
	Assignment *AssignmentFact
}

type AssignmentFact struct {
	Key    string          // broker transaction ID
	Shares decimal.Decimal // positive for a put, negative for a call
	Price  decimal.Decimal // strike
}
