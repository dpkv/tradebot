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
	"syscall"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type OptionQuote struct {
	secretsPath string
}

func (c *OptionQuote) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("option-quote", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	return "option-quote", fset, cli.CmdFunc(c.run)
}

func (c *OptionQuote) Purpose() string {
	return "Prints live quotes and greeks for option contract IDs, e.g. AAPL261218P00200000."
}

func (c *OptionQuote) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		return fmt.Errorf("one or more option contract id arguments are required")
	}
	var symbols []string
	for _, id := range args {
		sym, err := etrade.ContractQuoteSymbol(id)
		if err != nil {
			return fmt.Errorf("invalid option contract id %q: %w", id, err)
		}
		symbols = append(symbols, sym)
	}

	client, err := newCommandClient(ctx, c.secretsPath)
	if err != nil {
		return err
	}
	defer client.Close()

	quotes, err := client.GetOptionQuotes(ctx, symbols)
	if err != nil {
		return fmt.Errorf("could not get option quotes %v: %w", args, err)
	}

	js, _ := json.MarshalIndent(quotes, "", "  ")
	fmt.Printf("%s\n", js)
	return nil
}
