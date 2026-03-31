// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// apiPositionEntry is one entry from
// GET /v1/api/portfolio/{accountId}/positions/0.
type apiPositionEntry struct {
	ConID         int     `json:"conid"`
	Ticker        string  `json:"ticker"`
	SecType       string  `json:"secType"`
	Position      float64 `json:"position"`      // quantity (negative = short)
	AvgCost       float64 `json:"avgCost"`
	MktPrice      float64 `json:"mktPrice"`
	MktValue      float64 `json:"mktValue"`
	UnrealizedPnl float64 `json:"unrealizedPnl"`
	RealizedPnl   float64 `json:"realizedPnl"`
}

// Position is a portfolio position. For OPT positions the option-specific
// fields (OccSymbol, Underlying, OptionType, Strike, Expiry) are populated
// by the caller via GetOptionContractInfo.
type Position struct {
	ConID         int
	Ticker        string
	SecType       string
	Qty           decimal.Decimal
	AvgCost       decimal.Decimal
	MktPrice      decimal.Decimal
	MktValue      decimal.Decimal
	UnrealizedPnl decimal.Decimal
	RealizedPnl   decimal.Decimal

	// Populated for OPT by the caller.
	OccSymbol  string
	Underlying string
	OptionType string
	Strike     decimal.Decimal
	Expiry     time.Time
}

// GetPositions fetches all current portfolio positions from the gateway.
func (c *Client) GetPositions(ctx context.Context) ([]*Position, error) {
	path := "/v1/api/portfolio/" + c.creds.AccountID + "/positions/0"
	var entries []*apiPositionEntry
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &entries); err != nil {
		return nil, err
	}
	positions := make([]*Position, 0, len(entries))
	for _, e := range entries {
		positions = append(positions, &Position{
			ConID:         e.ConID,
			Ticker:        e.Ticker,
			SecType:       e.SecType,
			Qty:           decimal.NewFromFloat(e.Position),
			AvgCost:       decimal.NewFromFloat(e.AvgCost),
			MktPrice:      decimal.NewFromFloat(e.MktPrice),
			MktValue:      decimal.NewFromFloat(e.MktValue),
			UnrealizedPnl: decimal.NewFromFloat(e.UnrealizedPnl),
			RealizedPnl:   decimal.NewFromFloat(e.RealizedPnl),
		})
	}
	return positions, nil
}
