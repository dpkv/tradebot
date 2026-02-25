// Copyright (c) 2026 Deepak Vankadaru

package api

const AlertsPath = "/api/alerts"

// AlertsGetResponse is the GET /api/alerts response.
type AlertsGetResponse struct {
	// Default low-balance limits (currency -> minimum amount string).
	LowBalanceLimits map[string]string `json:"lowBalanceLimits"`
	// Per-exchange overrides (exchange name -> limits).
	PerExchange map[string]AlertsExchangeConfig `json:"perExchange,omitempty"`
}

// AlertsExchangeConfig holds low-balance limits for one exchange.
type AlertsExchangeConfig struct {
	LowBalanceLimits map[string]string `json:"lowBalanceLimits"`
}

// AlertsPostRequest is the POST /api/alerts body for setting low-balance limits.
type AlertsPostRequest struct {
	// Currency -> minimum amount string (e.g. "USD": "200", "BCH": "100").
	LowBalanceLimits map[string]string `json:"lowBalanceLimits"`
	// If set, update limits for this exchange only; otherwise update default limits.
	Exchange string `json:"exchange,omitempty"`
}

// AlertsPostResponse is the POST /api/alerts success response.
type AlertsPostResponse struct {
	OK bool `json:"ok"`
}
