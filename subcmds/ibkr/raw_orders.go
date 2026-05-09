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

// RawOrders prints the unmodified JSON response from the IBKR orders endpoint
// so that all fields — including any timestamp fields not yet captured in
// APIOrder — are visible for inspection.
type RawOrders struct {
	secretsPath string
}

func (c *RawOrders) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("raw-orders", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	return "raw-orders", fset, cli.CmdFunc(c.run)
}

func (c *RawOrders) Purpose() string {
	return "Print the raw JSON response from the IBKR orders API endpoint."
}

func (c *RawOrders) run(ctx context.Context, args []string) error {
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

	raw, err := client.GetOrdersRaw(ctx)
	if err != nil {
		return err
	}

	// Re-indent for readability.
	var pretty json.RawMessage
	if err := json.Unmarshal(raw, &pretty); err != nil {
		// Not valid JSON — print as-is.
		fmt.Printf("%s\n", raw)
		return nil
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Printf("%s\n", out)
	return nil
}
