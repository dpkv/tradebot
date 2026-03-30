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
	"time"

	ibkrpkg "github.com/bvk/tradebot/ibkr"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type ListOrders struct {
	secretsPath string
	secType     string
	symbol      string
	side        string
	status      string
}

func (c *ListOrders) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list-orders", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
	fset.StringVar(&c.secType, "sec-type", "", "filter by security type: STK or OPT (all if not set)")
	fset.StringVar(&c.symbol, "symbol", "", "filter by ticker symbol (case-insensitive)")
	fset.StringVar(&c.side, "side", "", "filter by side: BUY or SELL (case-insensitive)")
	fset.StringVar(&c.status, "status", "", "filter by status, e.g. Filled, Submitted (case-insensitive)")
	return "list-orders", fset, cli.CmdFunc(c.run)
}

func (c *ListOrders) Purpose() string {
	return "List all open orders from IBKR."
}

func (c *ListOrders) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	orders, err := client.GetOrders(ctx)
	if err != nil {
		return err
	}

	secType := strings.ToUpper(c.secType)
	symbol := strings.ToUpper(c.symbol)
	side := strings.ToUpper(c.side)
	status := strings.ToLower(c.status)

	// Resolve contract details for each unique OPT conid.
	type optInfo struct {
		occSymbol  string
		underlying string
		optType    string
		strike     string
		expiry     string
	}
	optInfoCache := map[int]*optInfo{}
	for _, o := range orders {
		if o.SecType != "OPT" {
			continue
		}
		if _, ok := optInfoCache[o.ConID]; ok {
			continue
		}
		contract, err := client.GetOptionContractInfo(ctx, o.ConID)
		if err != nil {
			// Non-fatal: display raw ticker if lookup fails.
			optInfoCache[o.ConID] = &optInfo{occSymbol: o.Symbol}
			continue
		}
		optInfoCache[o.ConID] = &optInfo{
			occSymbol:  contract.Symbol,
			underlying: contract.Underlying,
			optType:    contract.OptionType,
			strike:     contract.Strike.String(),
			expiry:     contract.Expiry.Format("2006-01-02"),
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ORDER_ID\tCLIENT_ID\tSEC_TYPE\tSYMBOL\tUNDERLYING\tTYPE\tSTRIKE\tEXPIRY\tSIDE\tSTATUS\tLIMIT_PRICE\tORDERED_QTY\tFILLED_QTY\tAVG_FILL_PRICE\tLAST_EXEC_TIME")
	for _, o := range orders {
		if secType != "" && strings.ToUpper(o.SecType) != secType {
			continue
		}
		if side != "" && strings.ToUpper(o.Side) != side {
			continue
		}
		if status != "" && strings.ToLower(o.Status) != status {
			continue
		}

		var displaySymbol, underlying, optType, strike, expiry string
		if o.SecType == "OPT" {
			if info, ok := optInfoCache[o.ConID]; ok {
				displaySymbol = info.occSymbol
				underlying = info.underlying
				optType = info.optType
				strike = info.strike
				expiry = info.expiry
			} else {
				displaySymbol = o.Symbol
			}
		} else {
			displaySymbol = o.Symbol
		}

		if symbol != "" && strings.ToUpper(displaySymbol) != symbol &&
			strings.ToUpper(underlying) != symbol {
			continue
		}

		var ts string
		if o.LastExecutionTimeMilli != 0 {
			ts = time.UnixMilli(o.LastExecutionTimeMilli).Format(time.DateTime)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			o.OrderID, o.ClientOrderID, o.SecType,
			displaySymbol, underlying, optType, strike, expiry,
			o.Side, o.Status,
			o.LimitPrice, o.OrderedQty, o.FilledQty, o.AvgFillPrice, ts)
	}
	return w.Flush()
}

