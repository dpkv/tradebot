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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ORDER_ID\tCLIENT_ID\tSEC_TYPE\tSYMBOL\tSIDE\tSTATUS\tLIMIT_PRICE\tORDERED_QTY\tFILLED_QTY\tAVG_FILL_PRICE\tLAST_EXEC_TIME")
	for _, o := range orders {
		if secType != "" && strings.ToUpper(o.SecType) != secType {
			continue
		}
		if symbol != "" && strings.ToUpper(o.Symbol) != symbol {
			continue
		}
		if side != "" && strings.ToUpper(o.Side) != side {
			continue
		}
		if status != "" && strings.ToLower(o.Status) != status {
			continue
		}
		var ts string
		if o.LastExecutionTimeMilli != 0 {
			ts = time.UnixMilli(o.LastExecutionTimeMilli).Format(time.DateTime)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			o.OrderID, o.ClientOrderID, o.SecType, o.Symbol, o.Side, o.Status,
			o.LimitPrice, o.OrderedQty, o.FilledQty, o.AvgFillPrice, ts)
	}
	return w.Flush()
}
