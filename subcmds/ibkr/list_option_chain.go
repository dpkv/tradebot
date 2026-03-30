// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	ibkrpkg "github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type ListOptionChain struct {
	secretsPath string
	optionType  string
}

func (c *ListOptionChain) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list-option-chain", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.StringVar(&c.optionType, "option-type", "", "filter by option type: CALL or PUT (all if not set)")
	return "list-option-chain", fset, cli.CmdFunc(c.run)
}

func (c *ListOptionChain) Purpose() string {
	return "List the options chain for an underlying symbol from IBKR."
}

func (c *ListOptionChain) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		return fmt.Errorf("underlying ticker argument is required (e.g. AAPL)")
	}
	underlying := args[0]

	optionType := strings.ToUpper(c.optionType)
	if optionType != "" && optionType != "CALL" && optionType != "PUT" {
		return fmt.Errorf("--option-type must be CALL or PUT")
	}

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

	contracts, err := exch.GetOptionChain(ctx, underlying)
	if err != nil {
		return fmt.Errorf("could not get option chain for %q: %w", underlying, err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SYMBOL\tTYPE\tEXPIRY\tSTRIKE\tCONTRACT_SIZE")
	for _, c := range contracts {
		if optionType != "" && c.OptionType != optionType {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			c.Symbol, c.OptionType, c.Expiry.Format("2006-01-02"),
			c.Strike.String(), c.ContractSize.String(),
		)
	}
	return w.Flush()
}
