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
	Config   *OptPositionConfig
	Progress *OptPositionProgress
}

// OptPositionConfig is fixed at creation; the owning greeler supplies the
// selector and re-price knobs.
type OptPositionConfig struct {
	ExchangeName string
	Underlying   string
}

// OptPositionProgress is everything trading writes.
type OptPositionProgress struct {
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
	Fee    decimal.Decimal // broker's assignment fee; zero on old records
}
