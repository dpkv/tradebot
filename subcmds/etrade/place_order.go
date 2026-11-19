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
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

type PlaceOrder struct {
	secretsPath   string
	accountID     string
	symbol        string
	contract      string
	side          string
	qty           string
	limitPrice    string
	orderTerm     string
	marketSession string
	clientOrderID string
	preview       bool

	// fset is kept so the checks can tell which flags were set explicitly.
	fset *flag.FlagSet
}

func (c *PlaceOrder) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("place-order", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	fset.StringVar(&c.symbol, "symbol", "", "equity ticker symbol (e.g. AAPL)")
	fset.StringVar(&c.contract, "contract", "", "option contract ID (e.g. AAPL261218P00200000), to place an option order instead of an equity order")
	fset.StringVar(&c.side, "side", "", "order side: BUY or SELL for equities; BUY_OPEN, SELL_OPEN, BUY_CLOSE or SELL_CLOSE for options")
	fset.StringVar(&c.qty, "qty", "", "quantity to order (whole contracts for options)")
	fset.StringVar(&c.limitPrice, "limit-price", "", "limit price (per share for options)")
	fset.StringVar(&c.orderTerm, "order-term", "GOOD_UNTIL_CANCEL", "order term: GOOD_UNTIL_CANCEL or GOOD_FOR_DAY (equities only; options are always GOOD_FOR_DAY)")
	fset.StringVar(&c.marketSession, "market-session", "REGULAR", "market session: REGULAR or EXTENDED (equities only; options are always REGULAR)")
	fset.StringVar(&c.clientOrderID, "client-order-id", "", "client order ID (numeric string); omitted if not set")
	fset.StringVar(&c.accountID, "account-id", "", "E*TRADE account ID (picked automatically if there is only one account)")
	fset.BoolVar(&c.preview, "preview", false, "only preview the order with E*TRADE and print its estimate; nothing is placed")
	c.fset = fset
	return "place-order", fset, cli.CmdFunc(c.run)
}

func (c *PlaceOrder) Purpose() string {
	return "Place an equity or option limit order on E*TRADE."
}

func (c *PlaceOrder) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.secretsPath == "" {
		return fmt.Errorf("--secrets-file flag is required")
	}
	if err := c.check(); err != nil {
		return err
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
	if len(secrets.ETrade.Accounts) == 0 {
		return fmt.Errorf("etrade has no trading accounts")
	}
	if c.accountID == "" {
		if len(secrets.ETrade.Accounts) != 1 {
			return fmt.Errorf("etrade account id must be specified; it has multiple accounts")
		}
		c.accountID = secrets.ETrade.Accounts[0].AccountID
	}

	opts := etrade.CommandOptions()
	opts.Sandbox = secrets.ETrade.Sandbox
	client, err := etrade.New(ctx, secrets.ETrade, opts)
	if err != nil {
		return fmt.Errorf("could not create etrade client: %w", err)
	}
	defer client.Close()

	clientOrderID := c.clientOrderID
	if clientOrderID == "" {
		clientOrderID = fmt.Sprintf("%d", time.Now().UnixMilli())
	}

	if c.preview {
		var preview *etrade.OrderPreview
		if c.contract != "" {
			preview, err = client.PreviewOptionOrder(ctx, c.accountID, c.contract, strings.ToUpper(c.side), qty, limitPrice, clientOrderID)
		} else {
			preview, err = client.PreviewLimitOrder(ctx, c.accountID, c.symbol, c.side, qty, limitPrice, clientOrderID, c.orderTerm, c.marketSession)
		}
		if err != nil {
			return fmt.Errorf("could not preview order: %w", err)
		}
		js, _ := json.MarshalIndent(preview, "", "  ")
		fmt.Printf("%s\n", js)
		return nil
	}

	var orderID int64
	if c.contract != "" {
		orderID, err = client.PlaceOptionOrder(ctx, c.accountID, c.contract, strings.ToUpper(c.side), qty, limitPrice, clientOrderID)
	} else {
		orderID, err = client.PlaceLimitOrder(ctx, c.accountID, c.symbol, c.side, qty, limitPrice, clientOrderID, c.orderTerm, c.marketSession, nil)
	}
	if err != nil {
		return fmt.Errorf("could not place order: %w", err)
	}

	order, err := client.GetOrder(ctx, c.accountID, orderID)
	if err != nil {
		// Still print the ID even if we can't fetch the full order.
		fmt.Printf("{\"orderId\": %d}\n", orderID)
		return nil
	}

	js, _ := json.MarshalIndent(order, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}

// optionActions are the --side values of an option order.
var optionActions = []string{etrade.OptionBuyOpen, etrade.OptionSellOpen, etrade.OptionBuyClose, etrade.OptionSellClose}

func isOptionAction(side string) bool {
	for _, a := range optionActions {
		if strings.EqualFold(side, a) {
			return true
		}
	}
	return false
}

// check validates the flags that pick and shape the order, before anything
// is read or sent.
func (c *PlaceOrder) check() error {
	switch {
	case c.symbol == "" && c.contract == "":
		return fmt.Errorf("one of --symbol (equity) or --contract (option) is required")
	case c.symbol != "" && c.contract != "":
		return fmt.Errorf("--symbol and --contract cannot be used together")
	}
	if c.side == "" {
		return fmt.Errorf("--side flag is required")
	}
	if c.qty == "" {
		return fmt.Errorf("--qty flag is required")
	}
	if c.limitPrice == "" {
		return fmt.Errorf("--limit-price flag is required")
	}

	if c.symbol != "" {
		if isOptionAction(c.side) {
			return fmt.Errorf("--side %s is for option orders; use --contract instead of --symbol", c.side)
		}
		return nil
	}

	if _, err := etrade.ContractQuoteSymbol(c.contract); err != nil {
		return fmt.Errorf("invalid --contract value %q: %w", c.contract, err)
	}
	if !isOptionAction(c.side) {
		return fmt.Errorf("--side for an option order must be one of %s", strings.Join(optionActions, ", "))
	}
	var bad []string
	if c.fset != nil {
		c.fset.Visit(func(f *flag.Flag) {
			if f.Name == "order-term" || f.Name == "market-session" {
				bad = append(bad, "--"+f.Name)
			}
		})
	}
	if len(bad) != 0 {
		return fmt.Errorf("%s cannot be used with --contract: option orders are always GOOD_FOR_DAY in the REGULAR session", strings.Join(bad, " and "))
	}
	return nil
}
