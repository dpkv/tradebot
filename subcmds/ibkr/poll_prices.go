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
	secType     string
}

func (c *PollPrices) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("poll-prices", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.StringVar(&c.secType, "sec-type", "STK", "security type: STK or OPT")
	return "poll-prices", fset, cli.CmdFunc(c.run)
}

func (c *PollPrices) Purpose() string {
	return "Stream live price updates for a symbol from IBKR (Ctrl+C to stop)."
}

func (c *PollPrices) run(ctx context.Context, args []string) error {
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

	exch, err := ibkrpkg.NewExchange(ctx, nil, secrets.IBKR, nil)
	if err != nil {
		return fmt.Errorf("could not create ibkr exchange: %w", err)
	}
	defer exch.Close()

	fmt.Fprintf(os.Stderr, "polling prices for %s (Ctrl+C to stop)...\n", symbol)

	if c.secType == "STK" {
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

		for ctx.Err() == nil {
			update, err := sub.Receive()
			if err != nil {
				return nil
			}
			price, ts := update.PricePoint()
			fmt.Printf("%s  price=%s\n", ts.Time.Format("15:04:05.000"), price.StringFixed(4))
		}
	} else {
		product, err := exch.OpenOptionsProduct(ctx, symbol)
		if err != nil {
			return fmt.Errorf("could not open options contract %q: %w", symbol, err)
		}
		defer product.Close()

		sub, err := product.GetPriceUpdates()
		if err != nil {
			return fmt.Errorf("could not subscribe to price updates: %w", err)
		}
		defer sub.Close()

		for ctx.Err() == nil {
			update, err := sub.Receive()
			if err != nil {
				return nil
			}
			price, ts := update.PricePoint()
			fmt.Printf("%s  price=%s\n", ts.Time.Format("15:04:05.000"), price.StringFixed(4))
		}
	}
	return nil
}
