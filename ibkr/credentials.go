// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import "fmt"

// Credentials holds the connection details required to reach the IBKR Client
// Portal Gateway. The gateway is a locally-running process (IB Gateway or CP
// Gateway) that handles authentication and proxies requests to IBKR's servers.
//
// Unlike E*TRADE's OAuth1a flow, there are no API keys here — authentication
// is managed entirely by the gateway process via a browser login. The bot only
// needs to know where the gateway is listening and which account to trade.
type Credentials struct {
	// GatewayURL is the base URL of the locally-running CP Gateway or IB
	// Gateway, e.g. "https://localhost:5000". The gateway uses a self-signed
	// TLS certificate so the client skips verification.
	GatewayURL string `json:"gateway_url"`

	// AccountID is the IBKR account identifier used in all account-scoped API
	// paths, e.g. "U1234567". It is discovered interactively during setup by
	// listing the accounts returned by the gateway and saved here.
	AccountID string `json:"account_id"`

	// PaperTrading indicates this is a paper-trading (simulated) account.
	// IB Gateway runs paper and live accounts on different ports (4002 vs
	// 4001); the CP Gateway uses different login credentials. This flag is
	// informational — the GatewayURL already points to the correct endpoint.
	PaperTrading bool `json:"paper_trading,omitempty"`
}

// Check returns an error if any mandatory credential field is empty.
func (c *Credentials) Check() error {
	if c.GatewayURL == "" {
		return fmt.Errorf("ibkr: gateway_url is required")
	}
	if c.AccountID == "" {
		return fmt.Errorf("ibkr: account_id is required")
	}
	return nil
}
