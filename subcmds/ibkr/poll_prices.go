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

type PollPrices struct {
	secretsPath string
}

func (c *PollPrices) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("poll-prices", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	return "poll-prices", fset, cli.CmdFunc(c.run)
}

func (c *PollPrices) Purpose() string {
	return "Stream live price updates for a symbol from IBKR (Ctrl+C to stop)."
}

func (c *PollPrices) run(ctx context.Context, args []string) error {
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

	sub, err := product.GetPriceUpdates()
	if err != nil {
		return fmt.Errorf("could not subscribe to price updates: %w", err)
	}
	defer sub.Close()

	fmt.Fprintf(os.Stderr, "polling prices for %s (Ctrl+C to stop)...\n", symbol)
	for ctx.Err() == nil {
		update, err := sub.Receive()
		if err != nil {
			return nil // context cancelled or topic closed
		}
		price, ts := update.PricePoint()
		fmt.Printf("%s  price=%s\n", ts.Time.Format("15:04:05.000"), price.StringFixed(4))
	}
	return nil
}
