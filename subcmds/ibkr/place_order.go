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
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

type PlaceOrder struct {
	secretsPath string

	symbol        string
	side          string
	qty           string
	limitPrice    string
	clientOrderID string
}

func (c *PlaceOrder) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("place-order", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.StringVar(&c.symbol, "symbol", "", "stock ticker symbol (e.g. AAPL)")
	fset.StringVar(&c.side, "side", "", "order side: BUY or SELL")
	fset.StringVar(&c.qty, "qty", "", "number of shares")
	fset.StringVar(&c.limitPrice, "limit-price", "", "limit price per share")
	fset.StringVar(&c.clientOrderID, "client-order-id", "", "idempotency UUID (generated if not set)")
	return "place-order", fset, cli.CmdFunc(c.run)
}

func (c *PlaceOrder) Purpose() string {
	return "Place a GTC limit buy or sell order on IBKR."
}

func (c *PlaceOrder) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(c.symbol) == 0 {
		return fmt.Errorf("--symbol is required")
	}
	if c.side != "BUY" && c.side != "SELL" {
		return fmt.Errorf("--side must be BUY or SELL")
	}
	if len(c.qty) == 0 {
		return fmt.Errorf("--qty is required")
	}
	if len(c.limitPrice) == 0 {
		return fmt.Errorf("--limit-price is required")
	}

	qty, err := decimal.NewFromString(c.qty)
	if err != nil {
		return fmt.Errorf("invalid --qty %q: %w", c.qty, err)
	}
	price, err := decimal.NewFromString(c.limitPrice)
	if err != nil {
		return fmt.Errorf("invalid --limit-price %q: %w", c.limitPrice, err)
	}

	var clientID uuid.UUID
	if len(c.clientOrderID) > 0 {
		clientID, err = uuid.Parse(c.clientOrderID)
		if err != nil {
			return fmt.Errorf("invalid --client-order-id %q: %w", c.clientOrderID, err)
		}
	} else {
		clientID = uuid.New()
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

	conid, err := client.ResolveConid(ctx, c.symbol)
	if err != nil {
		return fmt.Errorf("could not resolve conid for %q: %w", c.symbol, err)
	}

	orderID, err := client.PlaceOrder(ctx, conid, c.side, qty, price, clientID.String())
	if err != nil {
		return err
	}

	result := map[string]any{
		"server_order_id": orderID,
		"client_order_id": clientID.String(),
		"symbol":          c.symbol,
		"side":            c.side,
		"qty":             qty.String(),
		"limit_price":     price.String(),
	}
	js, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}
