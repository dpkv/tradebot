// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
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

type PollOrders struct {
	secretsPath string
}

func (c *PollOrders) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("poll-orders", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	return "poll-orders", fset, cli.CmdFunc(c.run)
}

func (c *PollOrders) Purpose() string {
	return "Stream live order updates for a symbol from IBKR (Ctrl+C to stop)."
}

func (c *PollOrders) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		return fmt.Errorf("symbol argument is required")
	}
	symbol := args[0]

	secrets, err := server.SecretsFromFile(c.secretsPath)
	if err != nil {
		return err
	}
	if secrets.IBKR == nil {
		return fmt.Errorf("secrets file has no ibkr credentials")
	}

	exch, err := ibkrpkg.NewExchange(ctx, secrets.IBKR, nil)
	if err != nil {
		return fmt.Errorf("could not create ibkr exchange: %w", err)
	}
	defer exch.Close()

	product, err := exch.OpenSpotProduct(ctx, symbol)
	if err != nil {
		return fmt.Errorf("could not open product %q: %w", symbol, err)
	}
	defer product.Close()

	sub, err := product.GetOrderUpdates()
	if err != nil {
		return fmt.Errorf("could not subscribe to order updates: %w", err)
	}
	defer sub.Close()

	fmt.Fprintf(os.Stderr, "polling orders for %s (Ctrl+C to stop)...\n", symbol)
	for ctx.Err() == nil {
		update, err := sub.Receive()
		if err != nil {
			return nil // context cancelled or topic closed
		}
		fmt.Printf("serverID=%-12s clientID=%s status=%-12s filled=%s value=%s fee=%s done=%v\n",
			update.ServerID(),
			update.ClientID(),
			update.OrderStatus(),
			update.ExecutedSize().StringFixed(4),
			update.ExecutedValue().StringFixed(4),
			update.ExecutedFee().StringFixed(4),
			update.IsDone(),
		)
	}
	return nil
}
