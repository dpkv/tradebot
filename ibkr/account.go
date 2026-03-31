// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"net/http"

	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/shopspring/decimal"
)

// AccountSummary holds all key balance metrics for an IBKR account.
type AccountSummary struct {
	Currency       string
	NetLiquidation decimal.Decimal
	AvailableFunds decimal.Decimal
	SettledCash    decimal.Decimal
	CashBalance    decimal.Decimal
	BuyingPower    decimal.Decimal
}

// GetAccountSummary fetches the full portfolio summary from the gateway.
func (c *Client) GetAccountSummary(ctx context.Context) (*AccountSummary, error) {
	path := "/v1/api/portfolio/" + c.creds.AccountID + "/summary"
	var summary internal.APISummary
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &summary); err != nil {
		return nil, err
	}
	currency := summary.NetLiquidation.Currency
	if currency == "" {
		currency = summary.AvailableFunds.Currency
	}
	return &AccountSummary{
		Currency:       currency,
		NetLiquidation: summary.NetLiquidation.Amount,
		AvailableFunds: summary.AvailableFunds.Amount,
		SettledCash:    summary.SettledCash.Amount,
		CashBalance:    summary.CashBalance.Amount,
		BuyingPower:    summary.BuyingPower.Amount,
	}, nil
}
