// Copyright (c) 2026 Deepak Vankadaru

package etrade

import "fmt"

// Credentials holds the OAuth 1.0a keys and account identifier required to
// authenticate with the E*TRADE API. All fields are mandatory; the setup
// etrade subcommand populates them interactively and saves them to secrets.json.
type Credentials struct {
	// ConsumerKey and ConsumerSecret are the application-level OAuth credentials
	// issued by E*TRADE when you register a developer application.
	ConsumerKey    string `json:"consumer_key"`
	ConsumerSecret string `json:"consumer_secret"`

	// AccessToken and AccessTokenSecret are the user-level OAuth credentials
	// obtained during the OAuth 1.0a authorization flow. They must be renewed
	// daily (E*TRADE tokens expire at midnight US Eastern time).
	AccessToken       string `json:"access_token"`
	AccessTokenSecret string `json:"access_token_secret"`

	// AccountIDKey is the accountIdKey used in all account-scoped API URLs. It is
	// discovered interactively during setup by listing the user's accounts and
	// saved here so it does not need to be passed on every run.
	AccountIDKey string `json:"account_id_key"`

	// Sandbox selects the E*TRADE sandbox environment when true.
	Sandbox bool `json:"sandbox,omitempty"`
}

// Check returns an error if any credential field is empty.
func (c *Credentials) Check() error {
	if c.ConsumerKey == "" {
		return fmt.Errorf("etrade: consumer_key is required")
	}
	if c.ConsumerSecret == "" {
		return fmt.Errorf("etrade: consumer_secret is required")
	}
	if c.AccessToken == "" {
		return fmt.Errorf("etrade: access_token is required")
	}
	if c.AccessTokenSecret == "" {
		return fmt.Errorf("etrade: access_token_secret is required")
	}
	if c.AccountIDKey == "" {
		return fmt.Errorf("etrade: account_id_key is required")
	}
	return nil
}
