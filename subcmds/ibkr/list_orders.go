// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
}

func (c *ListOrders) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list-orders", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", defaults.SecretsPath(), "path to secrets JSON file")
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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ORDER_ID\tCLIENT_ID\tSYMBOL\tSIDE\tSTATUS\tLIMIT_PRICE\tORDERED_QTY\tFILLED_QTY\tAVG_FILL_PRICE\tLAST_EXEC_TIME")
	for _, o := range orders {
		var ts string
		if o.LastExecutionTimeMilli != 0 {
			ts = time.UnixMilli(o.LastExecutionTimeMilli).Format(time.DateTime)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			o.OrderID, o.ClientOrderID, o.Symbol, o.Side, o.Status,
			o.LimitPrice, o.OrderedQty, o.FilledQty, o.AvgFillPrice, ts)
	}
	return w.Flush()
}
