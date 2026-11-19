// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greelladder"
	"github.com/bvk/tradebot/subcmds/cmdutil"
	"github.com/google/uuid"
	"github.com/visvasity/cli"
)

type LadderAdd struct {
	cmdutil.ClientFlags

	config ConfigFlags

	dryRun  bool
	name    string
	options optionsValue

	maxContracts   string
	maxPutExposure string
}

// bands cuts the configured range into one-contract bands and checks them
// as the server will.
func (c *LadderAdd) bands() ([]*gobs.GreelConfig, error) {
	cfg, err := c.config.Config()
	if err != nil {
		return nil, err
	}
	bands, err := greelladder.Cut(cfg)
	if err != nil {
		return nil, err
	}
	if err := greelladder.CheckBands(c.config.AccountName(), c.config.ProductID(), bands); err != nil {
		return nil, err
	}
	return bands, nil
}

// allOptions adds the gate flags to the --option values.
func (c *LadderAdd) allOptions() (map[string]string, error) {
	opts := make(map[string]string)
	for k, v := range c.options {
		opts[k] = v
	}
	for k, v := range map[string]string{"max-contracts": c.maxContracts, "max-put-exposure": c.maxPutExposure} {
		if v == "" {
			continue
		}
		if _, ok := opts[k]; ok {
			return nil, fmt.Errorf("%s is given both as a flag and as an --option", k)
		}
		opts[k] = v
	}
	return opts, nil
}

func (c *LadderAdd) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	if c.name == "" {
		return fmt.Errorf("job name must be specified with --name")
	}
	bands, err := c.bands()
	if err != nil {
		return err
	}
	opts, err := c.allOptions()
	if err != nil {
		return err
	}

	if c.dryRun {
		// Options are checked the way the server sets them.
		v, err := greelladder.New(uuid.NewString(), c.config.AccountName(), c.config.ProductID(), bands)
		if err != nil {
			return err
		}
		for k, val := range opts {
			if _, err := v.SetOption(k, val); err != nil {
				return fmt.Errorf("could not set option %s=%q: %w", k, val, err)
			}
		}
		for i, b := range bands {
			printLevels(fmt.Sprintf("greel-%d ", i), b)
		}
		return nil
	}

	req1 := &api.GreelLadderRequest{
		AccountName: c.config.AccountName(),
		ProductID:   c.config.ProductID(),
		Bands:       bands,
		Options:     opts,
	}
	resp1, err := cmdutil.Post[api.GreelLadderResponse](ctx, &c.ClientFlags, api.GreelLadderPath, req1)
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

func (c *LadderAdd) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("add", flag.ContinueOnError)
	c.ClientFlags.SetFlags(fset)
	c.config.SetFlags(fset)
	c.options = make(optionsValue)
	fset.BoolVar(&c.dryRun, "dry-run", false, "when true, only checks the config and prints each greel's levels")
	fset.StringVar(&c.name, "name", "", "a name for the ladder job (required)")
	fset.Var(c.options, "option", "a job option as key=value, set before the job first runs (repeatable)")
	fset.StringVar(&c.maxContracts, "max-contracts", "", "most greels writing an option at once; 0 means no limit")
	fset.StringVar(&c.maxPutExposure, "max-put-exposure", "", "most open put exposure (strike x 100 summed); 0 means no limit")
	return "add", fset, cli.CmdFunc(c.Run)
}

func (c *LadderAdd) Purpose() string {
	return "Creates a new greel ladder job over a price range"
}

func (c *LadderAdd) Description() string {
	return `

Command "add" creates a greel ladder. It builds the grid over the price range
exactly as "greeler add" would, then cuts it, from the lowest level up, into
greels of one contract each: each greel takes the fewest levels that hold 100
shares, and leftover levels at the top join the top greel. Every greel gets
the same zone and wheel settings and flips between grid and wheel on its own.

The ladder's risk limits, --max-contracts and --max-put-exposure, are set
before the ladder first runs. Use --dry-run to check the config and see each
greel's levels without creating anything.

EXAMPLE

    $ tradebot greelladder add --name aapl-ladder \
        --max-contracts 4 --max-put-exposure 80000 \
        --account etrade/main --product AAPL \
        --begin-price 150 --end-price 200 --buy-interval-pct 2 \
        --profit-margin-pct 3 --buy-size 50 \
        --grid-pct 5 --far-pct 12 --hysteresis-pct 2 --dwell-time 48h \
        --target-delta 0.3 --min-dte 21 --max-dte 45 \
        --min-premium-yield-pct 0.5 --min-open-interest 100 --max-spread-pct 10 \
        --reprice-step 0.25 --reprice-interval 5m

`
}
