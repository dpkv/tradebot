// Copyright (c) 2026 Deepak Vankadaru

package api

const WallerGetPath = "/trader/waller"

// WallerGetPairItem holds per-pair stats for a Waller job.
type WallerGetPairItem struct {
	Index        int
	Pair         string
	Budget       string
	Return       string
	AnnualReturn string
	Days         string
	Buys         int
	Sells        int
	Profit       string
	Fees         string
	BoughtValue  string
	SoldValue    string
	UnsoldValue  string
	SoldSize     string
	UnsoldSize   string
}

type WallerGetResponse struct {
	UID          string
	Name         string
	Status       string
	Type         string
	ProductID    string
	ExchangeName string
	ManualFlag   bool
	HasStatus    bool

	// High-level summary
	Budget       string
	Return       string
	AnnualReturn string
	ProfitPerDay string
	Days         string
	Buys         int
	Sells        int
	Profit       string
	Fees         string

	// Bought breakdown
	BoughtFees  string
	BoughtSize  string
	BoughtValue string

	// Sold breakdown
	SoldFees  string
	SoldSize  string
	SoldValue string

	// Unsold breakdown
	UnsoldFees  string
	UnsoldSize  string
	UnsoldValue string

	// Oversold breakdown
	OversoldFees  string
	OversoldSize  string
	OversoldValue string

	// Per-pair breakdown — populated only for Waller jobs.
	Pairs []*WallerGetPairItem
}
