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
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

type PlaceOrder struct {
	secretsPath string

	secType       string
	symbol        string
	side          string
	qty           string
	limitPrice    string
	clientOrderID string
}

func (c *PlaceOrder) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("place-order", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.StringVar(&c.secType, "sec-type", "STK", "security type: STK or OPT")
	fset.StringVar(&c.symbol, "symbol", "", "ticker (STK) or OCC symbol (OPT), e.g. AAPL or AAPL261218C00200000")
	fset.StringVar(&c.side, "side", "", "order side: BUY or SELL")
	fset.StringVar(&c.qty, "qty", "", "number of shares (STK) or contracts (OPT)")
	fset.StringVar(&c.limitPrice, "limit-price", "", "limit price")
	fset.StringVar(&c.clientOrderID, "client-order-id", "", "idempotency UUID (generated if not set)")
	return "place-order", fset, cli.CmdFunc(c.run)
}

func (c *PlaceOrder) Purpose() string {
	return "Place a GTC limit buy or sell order on IBKR (stocks or options)."
}

func (c *PlaceOrder) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.secType != "STK" && c.secType != "OPT" {
		return fmt.Errorf("--sec-type must be STK or OPT")
	}
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

	var serverOrderID int64

	if c.secType == "STK" {
		conid, err := client.ResolveConid(ctx, c.symbol)
		if err != nil {
			return fmt.Errorf("could not resolve conid for %q: %w", c.symbol, err)
		}
		serverOrderID, err = client.PlaceOrder(ctx, c.symbol, conid, c.side, qty, price, clientID.String())
		if err != nil {
			return err
		}
	} else {
		contract, err := client.GetOptionsProduct(ctx, c.symbol)
		if err != nil {
			return fmt.Errorf("could not resolve options contract %q: %w", c.symbol, err)
		}
		conid, err := strconv.Atoi(contract.ContractID)
		if err != nil {
			return fmt.Errorf("invalid conid %q for %s: %w", contract.ContractID, c.symbol, err)
		}
		serverOrderID, err = client.PlaceOptionOrder(ctx, contract.Underlying, conid, c.side, qty, price, clientID.String())
		if err != nil {
			return err
		}
	}

	result := map[string]any{
		"server_order_id": serverOrderID,
		"client_order_id": clientID.String(),
		"sec_type":        c.secType,
		"symbol":          c.symbol,
		"side":            c.side,
		"qty":             qty.String(),
		"limit_price":     price.String(),
	}
	js, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}
