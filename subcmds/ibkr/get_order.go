// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	ibkrpkg "github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type GetOrder struct {
	secretsPath string
}

func (c *GetOrder) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("get-order", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	return "get-order", fset, cli.CmdFunc(c.run)
}

func (c *GetOrder) Purpose() string {
	return "Fetch an open order by its server-assigned order ID from IBKR."
}

func (c *GetOrder) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		return fmt.Errorf("order ID argument is required")
	}
	orderID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid order ID %q: %w", args[0], err)
	}

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

	orders, err := client.GetOrders(ctx)
	if err != nil {
		return err
	}

	for _, o := range orders {
		if o.OrderID == orderID {
			js, _ := json.MarshalIndent(o, "", "  ")
			fmt.Printf("%s\n", js)
			return nil
		}
	}
	return fmt.Errorf("order %d not found in live orders", orderID)
}
