// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"github.com/bvk/tradebot/exchange"
	"github.com/shopspring/decimal"
)

// APIGreeksSnapshot is returned by
// GET /v1/api/iserver/marketdata/snapshot?conids={optionConid}&fields=7308,7309,7310,7311,7312.
//
// IBKR field IDs for option Greeks:
//
//	7308 - delta
//	7309 - gamma
//	7310 - implied volatility
//	7311 - vega
//	7312 - theta
//
// Rho is not available via this endpoint. All fields are strings and may be
// absent if the market data subscription is not yet ready.
type APIGreeksSnapshot struct {
	ConID   int   `json:"conid"`
	Updated int64 `json:"_updated"` // milliseconds since epoch

	DeltaStr string `json:"7308"`
	GammaStr string `json:"7309"`
	IVStr    string `json:"7310"` // implied volatility
	VegaStr  string `json:"7311"`
	ThetaStr string `json:"7312"`
}

// NewGreeksFromAPI converts an APIGreeksSnapshot into an exchange.Greeks.
// Returns nil if no Greek data is present (gateway not yet populated).
func NewGreeksFromAPI(a *APIGreeksSnapshot) *exchange.Greeks {
	delta, _ := decimal.NewFromString(a.DeltaStr)
	gamma, _ := decimal.NewFromString(a.GammaStr)
	iv, _ := decimal.NewFromString(a.IVStr)
	vega, _ := decimal.NewFromString(a.VegaStr)
	theta, _ := decimal.NewFromString(a.ThetaStr)

	if delta.IsZero() && gamma.IsZero() && vega.IsZero() && theta.IsZero() {
		return nil
	}
	return &exchange.Greeks{
		Delta: delta,
		Gamma: gamma,
		Theta: theta,
		Vega:  vega,
		IV:    iv,
	}
}
