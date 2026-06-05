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
	"strings"
	"syscall"
	"time"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

type PlaceOrder struct {
	secretsPath   string
	symbol        string
	side          string
	qty           string
	limitPrice    string
	orderTerm     string
	clientOrderID string
	lots          string // comma-separated lotID:size pairs, e.g. "12345:1,67890:0.5"
}

func (c *PlaceOrder) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("place-order", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	fset.StringVar(&c.symbol, "symbol", "", "equity ticker symbol (e.g. AAPL)")
	fset.StringVar(&c.side, "side", "", "order side: BUY or SELL")
	fset.StringVar(&c.qty, "qty", "", "quantity to order")
	fset.StringVar(&c.limitPrice, "limit-price", "", "limit price")
	fset.StringVar(&c.orderTerm, "order-term", "GOOD_UNTIL_CANCEL", "order term: GOOD_UNTIL_CANCEL or GOOD_FOR_DAY")
	fset.StringVar(&c.clientOrderID, "client-order-id", "", "client order ID (numeric string); omitted if not set")
	fset.StringVar(&c.lots, "lots", "", "tax lots to sell from, as comma-separated lotID:size pairs (e.g. 12345:1,67890:0.5)")
	return "place-order", fset, cli.CmdFunc(c.run)
}

func (c *PlaceOrder) Purpose() string {
	return "Place a limit order on E*TRADE."
}

func (c *PlaceOrder) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.secretsPath == "" {
		return fmt.Errorf("--secrets-file flag is required")
	}
	if c.symbol == "" {
		return fmt.Errorf("--symbol flag is required")
	}
	if c.side == "" {
		return fmt.Errorf("--side flag is required (BUY or SELL)")
	}
	if c.qty == "" {
		return fmt.Errorf("--qty flag is required")
	}
	if c.limitPrice == "" {
		return fmt.Errorf("--limit-price flag is required")
	}

	qty, err := decimal.NewFromString(c.qty)
	if err != nil {
		return fmt.Errorf("invalid --qty value %q: %w", c.qty, err)
	}
	limitPrice, err := decimal.NewFromString(c.limitPrice)
	if err != nil {
		return fmt.Errorf("invalid --limit-price value %q: %w", c.limitPrice, err)
	}

	secrets, err := server.SecretsFromFile(c.secretsPath)
	if err != nil {
		return err
	}
	if secrets.ETrade == nil {
		return fmt.Errorf("secrets file has no etrade credentials")
	}

	opts := &etrade.Options{Sandbox: secrets.ETrade.Sandbox}
	client, err := etrade.New(ctx, secrets.ETrade, opts)
	if err != nil {
		return fmt.Errorf("could not create etrade client: %w", err)
	}
	defer client.Close()

	var lots []exchange.Lot
	if c.lots != "" {
		for _, entry := range strings.Split(c.lots, ",") {
			parts := strings.SplitN(entry, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid -lots entry %q: want lotID:size", entry)
			}
			size, err := decimal.NewFromString(parts[1])
			if err != nil {
				return fmt.Errorf("invalid -lots entry %q: bad size: %w", entry, err)
			}
			lots = append(lots, exchange.Lot{ID: parts[0], RemainingSize: size})
		}
	}

	clientOrderID := c.clientOrderID
	if clientOrderID == "" {
		clientOrderID = fmt.Sprintf("%d", time.Now().UnixMilli())
	}
	orderID, err := client.PlaceLimitOrder(ctx, c.symbol, c.side, qty, limitPrice, clientOrderID, c.orderTerm, lots)
	if err != nil {
		return fmt.Errorf("could not place order: %w", err)
	}

	order, err := client.GetOrder(ctx, orderID)
	if err != nil {
		// Still print the ID even if we can't fetch the full order.
		fmt.Printf("{\"orderId\": %d}\n", orderID)
		return nil
	}

	js, _ := json.MarshalIndent(order, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}
