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
	AccountName  string
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

	// Proposal is the selection waiting for an operator's approval; nil
	// when none is (greel/approval-story.md).
	Proposal *OptProposal

	// Rejected are contracts the operator turned down for this position;
	// selection skips them.
	Rejected []string

	// Asks counts the approval requests made so far; the next one's key
	// ends in ask-<Asks>.
	Asks int
}

// OptProposal is a selection waiting for approval. It is saved before it is
// asked, so a restart asks again with the same key.
type OptProposal struct {
	Key       string // the approval request's idempotency key
	RequestID string // empty until the request is made

	Contract   *OptionContract
	MinPremium decimal.Decimal
	Spot       decimal.Decimal // at selection, for the summary; zero if unknown

	// Switch is true when the proposal replaces the contract of an unfilled
	// attempt that keeps working while it waits.
	Switch bool

	ExpiresAt time.Time
}

type AssignmentFact struct {
	Key    string          // broker transaction ID
	Shares decimal.Decimal // positive for a put, negative for a call
	Price  decimal.Decimal // strike
	Fee    decimal.Decimal // broker's assignment fee; zero on old records
}
