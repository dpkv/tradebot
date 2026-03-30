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
	secType     string
}

func (c *PollOrders) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("poll-orders", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.StringVar(&c.secType, "sec-type", "STK", "security type: STK or OPT")
	return "poll-orders", fset, cli.CmdFunc(c.run)
}

func (c *PollOrders) Purpose() string {
	return "Stream live order updates for a symbol from IBKR (Ctrl+C to stop)."
}

func (c *PollOrders) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.secType != "STK" && c.secType != "OPT" {
		return fmt.Errorf("--sec-type must be STK or OPT")
	}
	if len(args) == 0 {
		return fmt.Errorf("symbol argument is required (ticker for STK, OCC symbol for OPT)")
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

	fmt.Fprintf(os.Stderr, "polling orders for %s (Ctrl+C to stop)...\n", symbol)

	if c.secType == "STK" {
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

		for ctx.Err() == nil {
			update, err := sub.Receive()
			if err != nil {
				return nil
			}
			fmt.Printf("serverID=%-12s clientID=%s status=%-12s filled=%s value=%s fee=%s done=%v\n",
				update.ServerID(), update.ClientID(), update.OrderStatus(),
				update.ExecutedSize().StringFixed(4), update.ExecutedValue().StringFixed(4),
				update.ExecutedFee().StringFixed(4), update.IsDone(),
			)
		}
	} else {
		product, err := exch.OpenOptionsProduct(ctx, symbol)
		if err != nil {
			return fmt.Errorf("could not open options contract %q: %w", symbol, err)
		}
		defer product.Close()

		sub, err := product.GetOrderUpdates()
		if err != nil {
			return fmt.Errorf("could not subscribe to order updates: %w", err)
		}
		defer sub.Close()

		for ctx.Err() == nil {
			update, err := sub.Receive()
			if err != nil {
				return nil
			}
			fmt.Printf("serverID=%-12s clientID=%s status=%-12s filled=%s value=%s fee=%s done=%v\n",
				update.ServerID(), update.ClientID(), update.OrderStatus(),
				update.ExecutedSize().StringFixed(4), update.ExecutedValue().StringFixed(4),
				update.ExecutedFee().StringFixed(4), update.IsDone(),
			)
		}
	}
	return nil
}
