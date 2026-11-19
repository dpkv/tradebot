// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/subcmds/cmdutil"
	"github.com/google/uuid"
	"github.com/visvasity/cli"
)

type GreelerAdd struct {
	cmdutil.ClientFlags

	config ConfigFlags

	dryRun  bool
	name    string
	options optionsValue
}

func (c *GreelerAdd) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	if c.name == "" {
		return fmt.Errorf("job name must be specified with --name")
	}
	cfg, err := c.config.Config()
	if err != nil {
		return err
	}

	if c.dryRun {
		// Options are checked the way the server sets them.
		v, err := greeler.New(uuid.NewString(), cfg)
		if err != nil {
			return err
		}
		for k, val := range c.options {
			if _, err := v.SetOption(k, val); err != nil {
				return fmt.Errorf("could not set option %s=%q: %w", k, val, err)
			}
		}
		printLevels("", cfg)
		return nil
	}

	req1 := &api.GreelRequest{Config: cfg, Options: c.options}
	resp1, err := cmdutil.Post[api.GreelResponse](ctx, &c.ClientFlags, api.GreelPath, req1)
	if err != nil {
		return err
	}

	req2 := &api.SetJobNameRequest{UID: resp1.UID, JobName: c.name}
	if _, err := cmdutil.Post[api.SetJobNameResponse](ctx, &c.ClientFlags, api.SetJobNamePath, req2); err != nil {
		log.Printf("job is created, but could not set the job name (ignored): %v", err)
	}

	jsdata, _ := json.MarshalIndent(resp1, "", "  ")
	fmt.Printf("%s\n", jsdata)
	return nil
}

func (c *GreelerAdd) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("add", flag.ContinueOnError)
	c.ClientFlags.SetFlags(fset)
	c.config.SetFlags(fset)
	c.options = make(optionsValue)
	fset.BoolVar(&c.dryRun, "dry-run", false, "when true, only checks the config and prints the grid levels")
	fset.StringVar(&c.name, "name", "", "a name for the greeler job (required)")
	fset.Var(c.options, "option", "a job option as key=value, set before the job first runs (repeatable)")
	return "add", fset, cli.CmdFunc(c.Run)
}

func (c *GreelerAdd) Purpose() string {
	return "Creates a new greeler job over a price range"
}

func (c *GreelerAdd) Description() string {
	return `

Command "add" creates one greeler: a grid of buy-and-sell levels over a price
range, built exactly as "waller add" builds it, that writes a cash-secured put
when the price is far above the levels and a covered call when it is far
below them. The account must support options.

Every zone and wheel flag must be given, even when its value is 0, so no
setting is a default nobody chose. Use --dry-run to check the config and see
the grid levels without creating anything.

EXAMPLE

    $ tradebot greeler add --name aapl-greel \
        --account etrade/main --product AAPL \
        --begin-price 150 --end-price 200 --buy-interval-pct 2 \
        --profit-margin-pct 3 --buy-size 50 \
        --grid-pct 5 --far-pct 12 --hysteresis-pct 2 --dwell-time 48h \
        --target-delta 0.3 --min-dte 21 --max-dte 45 \
        --min-premium-yield-pct 0.5 --min-open-interest 100 --max-spread-pct 10 \
        --reprice-step 0.25 --reprice-interval 5m

`
}
