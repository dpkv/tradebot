// Copyright (c) 2026 Deepak Vankadaru

package gobs

import (
	"time"

	"github.com/shopspring/decimal"
)

type GreelerState struct {
	V1 *GreelerStateV1
}

type GreelerStateV1 struct {
	// Options is runtime-mutable via SetOption but never written by trading,
	// so it belongs to neither Config nor Progress.
	Options map[string]string

	Config   *GreelConfig
	Progress *GreelProgress
}

// GreelConfig is fixed at creation; Load rebuilds the greeler from it alone.
type GreelConfig struct {
	ProductID    string
	ExchangeName string

	// GridLevels are in ascending price order and never reordered.
	GridLevels []*Pair

	GridPct       decimal.Decimal
	FarPct        decimal.Decimal
	HysteresisPct decimal.Decimal
	DwellTime     time.Duration // threshold for the open epoch's dwell clock

	ContractSelector string // registered implementation name; empty means the default
	WheelKnobs       *WheelKnobs
}

// GreelProgress is everything trading writes.
type GreelProgress struct {
	// Epochs is never empty; a child is recorded here before it can place an order.
	Epochs []*GreelEpoch
}

type WheelKnobs struct {
	TargetDelta     decimal.Decimal
	MinDTE, MaxDTE  int
	MinPremiumYield decimal.Decimal
	MinOpenInterest decimal.Decimal
	MaxSpreadPct    decimal.Decimal

	RepriceStep     decimal.Decimal // fraction of the bid-ask spread per step
	RepriceInterval time.Duration
}

type GreelEpoch struct {
	Mode    string // "grid" or "wheel"
	StartAt time.Time

	PositionID string // wheel epochs only

	// The dwell clock changes only while this is the last epoch.
	PendingFlip   string // "", "wheel-put", "wheel-call" or "grid"
	PendingFlipAt time.Time

	LevelLimiterIDs [][]string // grid epochs only; indexed like GridLevels
}
