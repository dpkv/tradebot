// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type ListTransactions struct {
	secretsPath string
	accountID   string
	days        int
}

func (c *ListTransactions) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list-transactions", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	fset.StringVar(&c.accountID, "account-id", "", "E*TRADE account ID (picked automatically if there is only one account)")
	fset.IntVar(&c.days, "days", 45, "number of days of transactions to list, ending today")
	return "list-transactions", fset, cli.CmdFunc(c.run)
}

func (c *ListTransactions) Purpose() string {
	return "Prints an E*TRADE account's transactions, including the transaction types option settlement matches on."
}

func (c *ListTransactions) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.days <= 0 {
		return fmt.Errorf("--days must be positive")
	}
	if c.accountID == "" {
		secrets, err := server.SecretsFromFile(c.secretsPath)
		if err != nil {
			return err
		}
		if secrets.ETrade == nil || len(secrets.ETrade.Accounts) == 0 {
			return fmt.Errorf("etrade has no trading accounts")
		}
		if len(secrets.ETrade.Accounts) != 1 {
			return fmt.Errorf("etrade account id must be specified; it has multiple accounts")
		}
		c.accountID = secrets.ETrade.Accounts[0].AccountID
	}

	client, err := newCommandClient(ctx, c.secretsPath)
	if err != nil {
		return err
	}
	defer client.Close()

	end := time.Now()
	start := end.AddDate(0, 0, -c.days)
	txs, err := client.ListTransactions(ctx, c.accountID, start, end)
	if err != nil {
		return fmt.Errorf("could not list transactions: %w", err)
	}

	js, _ := json.MarshalIndent(txs, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}
