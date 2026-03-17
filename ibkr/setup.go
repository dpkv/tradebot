// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// setupClient returns a minimal HTTP client suitable for setup-time calls to
// the gateway. It shares the same InsecureSkipVerify setting as the main
// client since the gateway always uses a self-signed certificate.
func setupClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
}

func setupGet(ctx context.Context, gatewayURL, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := setupClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ibkr setup: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// CheckAuth reports whether the gateway at gatewayURL has an authenticated
// session. Returns an error if the gateway is unreachable.
//
// Used by the setup subcommand to verify the user has logged in before saving
// credentials.
func CheckAuth(ctx context.Context, gatewayURL string) (authenticated bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL+"/v1/api/iserver/auth/status", nil)
	if err != nil {
		return false, err
	}
	resp, err := setupClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("ibkr: could not reach gateway at %s: %w", gatewayURL, err)
	}
	defer resp.Body.Close()
	// 401 means the gateway is running but the session is not yet authenticated
	// (user has not logged in via the browser). Treat it as authenticated=false,
	// not as an unreachable error.
	if resp.StatusCode == 401 {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("ibkr: unexpected status from gateway at %s: HTTP %d: %s", gatewayURL, resp.StatusCode, string(body))
	}
	var status apiAuthStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return false, fmt.Errorf("ibkr: could not decode auth status response: %w", err)
	}
	return status.Authenticated, nil
}

// ListAccounts returns the account IDs available in the current gateway
// session. The user selects one to store in Credentials.AccountID.
//
// Returns an error if the gateway is unreachable or the session is not
// authenticated.
func ListAccounts(ctx context.Context, gatewayURL string) ([]string, error) {
	authenticated, err := CheckAuth(ctx, gatewayURL)
	if err != nil {
		return nil, err
	}
	if !authenticated {
		return nil, fmt.Errorf("ibkr: gateway session is not authenticated — see setup instructions below")
	}
	// The /iserver/accounts endpoint returns an object with an "accounts" array,
	// not a bare JSON array.
	var resp struct {
		Accounts []string `json:"accounts"`
	}
	if err := setupGet(ctx, gatewayURL, "/v1/api/iserver/accounts", &resp); err != nil {
		return nil, err
	}
	return resp.Accounts, nil
}

// PrintSetupInstructions writes step-by-step gateway authentication
// instructions to w. Called by the setup subcommand before CheckAuth so the
// user knows what to do if the session is not yet authenticated.
func PrintSetupInstructions(w io.Writer) {
	fmt.Fprintln(w, `IBKR Client Portal Gateway — Setup Instructions
================================================

The bot connects to IBKR through the Client Portal Gateway (CP Gateway) or
IB Gateway running locally on your machine. The gateway handles authentication
and proxies API requests to IBKR's servers.

Step 1 — Download and start the gateway
  CP Gateway: https://www.interactivebrokers.com/en/trading/ib-api.php
  IB Gateway:  same page, choose "IB Gateway" for headless use

  Default ports:
    CP Gateway  : https://localhost:5000  (live)
    IB Gateway  : https://localhost:4001  (live)
                  https://localhost:4002  (paper trading)

Step 2 — Log in
  Open the gateway URL in your browser and log in with your IBKR credentials.
  For IB Gateway, use the desktop login dialog.

Step 3 — Run the setup command
  Once logged in, run:
    tradebot setup ibkr --gateway-url https://localhost:5000

  The command will verify authentication, list your accounts, and write the
  chosen account ID to secrets.json.

Note: The gateway session expires after ~24 hours or if it goes idle for
60 seconds. The bot keeps the session alive automatically with periodic
tickle requests while it is running. You must re-authenticate in the
gateway if the bot was not running and the session expired.`)
}
