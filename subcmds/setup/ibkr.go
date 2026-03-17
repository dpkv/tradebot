// Copyright (c) 2026 Deepak Vankadaru

package setup

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/server"
	"github.com/visvasity/cli"
)

type IBKR struct {
	dataDir      string
	gatewayURL   string
	accountID    string
	paperTrading bool
}

func (c *IBKR) Purpose() string {
	return "Setup configures IBKR Client Portal Gateway access parameters."
}

func (c *IBKR) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("ibkr", flag.ContinueOnError)
	fset.StringVar(&c.dataDir, "data-dir", "", "path to the data directory")
	fset.StringVar(&c.gatewayURL, "gateway-url", "https://localhost:5000", "URL of the CP Gateway or IB Gateway")
	fset.StringVar(&c.accountID, "account-id", "", "IBKR account ID to use (required if multiple accounts exist)")
	fset.BoolVar(&c.paperTrading, "paper-trading", false, "mark credentials as paper-trading account")
	return "ibkr", fset, cli.CmdFunc(c.run)
}

func (c *IBKR) run(ctx context.Context, args []string) error {
	if len(c.dataDir) == 0 {
		c.dataDir = filepath.Join(os.Getenv("HOME"), ".tradebot")
	}
	if _, err := os.Stat(c.dataDir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("could not stat data directory %q: %w", c.dataDir, err)
		}
		if err := os.MkdirAll(c.dataDir, 0700); err != nil {
			return fmt.Errorf("could not create data directory %q: %w", c.dataDir, err)
		}
	}
	dataDir, err := filepath.Abs(c.dataDir)
	if err != nil {
		return fmt.Errorf("could not determine data-dir %q absolute path: %w", c.dataDir, err)
	}

	ibkr.PrintSetupInstructions(os.Stderr)
	fmt.Fprintln(os.Stderr)

	authenticated, err := ibkr.CheckAuth(ctx, c.gatewayURL)
	if err != nil {
		return err
	}
	if !authenticated {
		return fmt.Errorf("gateway at %s is not authenticated — log in via the gateway UI and re-run this command", c.gatewayURL)
	}

	accounts, err := ibkr.ListAccounts(ctx, c.gatewayURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "available accounts: %v\n", accounts)

	accountID := c.accountID
	if len(accountID) == 0 {
		if len(accounts) == 1 {
			accountID = accounts[0]
			fmt.Fprintf(os.Stderr, "using account: %s\n", accountID)
		} else {
			return fmt.Errorf("multiple accounts found %v — specify one with --account-id", accounts)
		}
	}

	secretsPath := filepath.Join(dataDir, "secrets.json")
	secrets, err := server.SecretsFromFile(secretsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if secrets == nil {
		secrets = &server.Secrets{}
	}

	secrets.IBKR = &ibkr.Credentials{
		GatewayURL:   c.gatewayURL,
		AccountID:    accountID,
		PaperTrading: c.paperTrading,
	}

	js, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(secretsPath, js, os.FileMode(0600)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "credentials written to %s\n", secretsPath)
	return nil
}
