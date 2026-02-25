// Copyright (c) 2026 Deepak Vankadaru

package api

// WallerValidatePath is used to validate a hypothetical waller job spec and
// return the generated buy/sell pairs and summary statistics (similar to the
// "waller query" subcommand).
const WallerValidatePath = "/trader/waller/validate"

// WallerValidateRequest mirrors the Spec flags used by the "waller add/query"
// subcommands.
type WallerValidateRequest struct {
	BeginPrice      float64
	EndPrice        float64
	BuyInterval     float64
	BuyIntervalPct  float64
	ProfitMargin    float64
	ProfitMarginPct float64
	BuySize         float64
	CancelOffsetPct float64
	FeePct          float64
}

// WallerValidatePairPreview holds a human-readable preview of a single
// buy/sell pair (similar to "waller query -print-pairs").
type WallerValidatePairPreview struct {
	BuySize     string
	BuyPrice    string
	BuyCancel   string
	SellSize    string
	SellPrice   string
	SellCancel  string
	PriceMargin string
	Profit      string
}

// WallerValidateAPRRow describes how many sells are required to achieve a given
// annual return rate.
type WallerValidateAPRRow struct {
	RatePct          float64
	NumSellsPerYear  string
	NumSellsPerMonth string
	NumSellsPerDay   string
}

// WallerValidateSummary aggregates analysis metrics for a hypothetical waller
// spec. These match the values printed by "waller query".
type WallerValidateSummary struct {
	Budget  string
	FeePct  string
	NumPairs int

	MinLoopFee string
	AvgLoopFee string
	MaxLoopFee string

	MinPriceMargin string
	AvgPriceMargin string
	MaxPriceMargin string

	MinProfitMargin string
	AvgProfitMargin string
	MaxProfitMargin string

	APRs []*WallerValidateAPRRow
}

// WallerValidateResponse represents the result of validating a hypothetical
// waller spec. When Valid is false, Error contains a user-readable explanation
// and other fields are not populated.
type WallerValidateResponse struct {
	Valid bool
	Error string

	Summary *WallerValidateSummary

	// PreviewPairs is used by the UI to show a human-friendly table and to
	// reconstruct buy/sell pairs on the client side. Cancel prices are
	// included here but not shown in the UI.
	PreviewPairs []*WallerValidatePairPreview
}

