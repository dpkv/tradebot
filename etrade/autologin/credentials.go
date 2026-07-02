// Copyright (c) 2026 Deepak Vankadaru

package autologin

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoginCredentials holds the raw E*TRADE website login (username/password),
// as distinct from the OAuth consumer key/secret or access tokens stored in
// etrade.Credentials. Kept in a separate file from secrets.json so the raw
// login password isn't mixed into the file the trading bot reads regularly.
type LoginCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Check returns an error if any credential field is empty.
func (c *LoginCredentials) Check() error {
	if c.Username == "" {
		return fmt.Errorf("autologin: username is required")
	}
	if c.Password == "" {
		return fmt.Errorf("autologin: password is required")
	}
	return nil
}

// LoadLoginCredentials reads login credentials from fpath. Returns an error
// satisfying os.IsNotExist if the file doesn't exist yet.
func LoadLoginCredentials(fpath string) (*LoginCredentials, error) {
	data, err := os.ReadFile(fpath)
	if err != nil {
		return nil, err
	}
	creds := new(LoginCredentials)
	if err := json.Unmarshal(data, creds); err != nil {
		return nil, fmt.Errorf("autologin: could not parse %s: %w", fpath, err)
	}
	return creds, nil
}

// SaveLoginCredentials writes login credentials to fpath with 0600
// permissions.
func SaveLoginCredentials(fpath string, creds *LoginCredentials) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fpath, data, 0600)
}
