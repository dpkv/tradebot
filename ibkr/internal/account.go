// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"github.com/bvk/tradebot/exchange"
	"github.com/shopspring/decimal"
)

// APISummaryField is one entry in the portfolio summary response. Each field
// in the summary is an object with an amount, currency, and null indicator.
type APISummaryField struct {
	Amount   decimal.Decimal `json:"amount"`
	Currency string          `json:"currency"`
	IsNull   bool            `json:"isNull"`
}

// APISummary is the JSON structure returned by
// GET /v1/api/portfolio/{accountId}/summary.
//
// AvailableFunds is used as the tradeable balance. See the follow-up task in
// NOTES regarding settled vs available cash for cash vs margin accounts.
type APISummary struct {
	AvailableFunds APISummaryField `json:"availablefunds"`

	// Unused fields — decoded for completeness but not used by the bot.

	// SettledCash is cash from settled trades only. Safe to use in cash
	// accounts to avoid trading on unsettled proceeds. See NOTES follow-up.
	SettledCash APISummaryField `json:"settledcash"` // unused
	// CashBalance is total cash including unsettled proceeds.
	CashBalance APISummaryField `json:"cashbalance"` // unused
	// NetLiquidation is the total account value (cash + positions).
	NetLiquidation APISummaryField `json:"netliquidation"` // unused
	// BuyingPower is margin-adjusted purchasing power; higher than cash for
	// margin accounts.
	BuyingPower APISummaryField `json:"buyingpower"` // unused
}

// Balance is a flat balance update derived from an IBKR portfolio summary. It
// implements exchange.BalanceUpdate.
type Balance struct {
	Currency  string
	Available decimal.Decimal
}

var _ exchange.BalanceUpdate = &Balance{}

func (b *Balance) Balance() (string, decimal.Decimal) {
	return b.Currency, b.Available
}

// NewBalanceFromAPI converts an APISummary into a Balance. Returns nil if the
// available funds field is null or has no currency set.
func NewBalanceFromAPI(a *APISummary) *Balance {
	f := a.AvailableFunds
	if f.IsNull || f.Currency == "" {
		return nil
	}
	return &Balance{
		Currency:  f.Currency,
		Available: f.Amount,
	}
}
