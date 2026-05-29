// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/bvk/tradebot/etrade"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/server"
	"github.com/bvk/tradebot/subcmds/defaults"
	"github.com/visvasity/cli"
)

type GetLots struct {
	secretsPath string
	orderIDs    string // comma-separated order IDs
}

func (c *GetLots) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("get-lots", flag.ContinueOnError)
	fset.StringVar(&c.secretsPath, "secrets-file", filepath.Join(defaults.DataDir(), "secrets.json"), "path to secrets.json file")
	fset.StringVar(&c.orderIDs, "order-ids", "", "comma-separated E*TRADE order IDs; omit to return all lots")
	return "get-lots", fset, cli.CmdFunc(c.run)
}

func (c *GetLots) Purpose() string {
	return "Fetch tax lots from E*TRADE portfolio; optionally filter by buy order IDs."
}

func (c *GetLots) run(ctx context.Context, args []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	var orderIDs []string
	if c.orderIDs != "" {
		for id := range strings.SplitSeq(c.orderIDs, ",") {
			if id = strings.TrimSpace(id); id != "" {
				orderIDs = append(orderIDs, id)
			}
		}
	}
	orderIDs = append(orderIDs, args...)

	var lots []exchange.Lot
	if len(orderIDs) == 0 {
		lots, err = client.GetAllLots(ctx)
	} else {
		lots, err = client.GetLotsForOrders(ctx, orderIDs)
	}
	if err != nil {
		return fmt.Errorf("could not get lots: %w", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "LOT-ID\tORIG-QTY\tREM-QTY\tCOST-BASIS\tACQUIRED\n")
	for _, lot := range lots {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			lot.ID,
			lot.OriginalSize.StringFixed(3),
			lot.RemainingSize.StringFixed(3),
			lot.CostBasis.StringFixed(2),
			lot.AcquiredDate.Format("2006-01-02"),
		)
	}
	tw.Flush()
	return nil
}
