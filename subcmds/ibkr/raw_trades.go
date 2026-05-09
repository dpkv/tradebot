// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	ibkrpkg "github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type RawTrades struct {
	secretsPath string
	days        int
}

func (c *RawTrades) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("raw-trades", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.IntVar(&c.days, "days", 7, "number of days of trade history to fetch (1-7)")
	return "raw-trades", fset, cli.CmdFunc(c.run)
}

func (c *RawTrades) Purpose() string {
	return "Print the raw JSON response from the IBKR trades/executions API endpoint."
}

func (c *RawTrades) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	secrets, err := server.SecretsFromFile(c.secretsPath)
	if err != nil {
		return err
	}
	if secrets.IBKR == nil {
		return fmt.Errorf("secrets file has no ibkr credentials")
	}

	client, err := ibkrpkg.New(ctx, secrets.IBKR, nil)
	if err != nil {
		return fmt.Errorf("could not create ibkr client: %w", err)
	}
	defer client.Close()

	raw, err := client.GetTradesRaw(ctx, c.days)
	if err != nil {
		return err
	}

	var pretty json.RawMessage
	if err := json.Unmarshal(raw, &pretty); err != nil {
		fmt.Printf("%s\n", raw)
		return nil
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Printf("%s\n", out)
	return nil
}
