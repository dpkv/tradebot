// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/greelladder"
	"github.com/bvk/tradebot/point"
	"github.com/bvk/tradebot/waller"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

var one = decimal.NewFromInt(1)

// greelFacts is what one greel needs and where its zones lie, worked out
// the way the greeler decides them (greeler/run.go).
type greelFacts struct {
	levels      int
	shares      decimal.Decimal
	budget      decimal.Decimal
	bottom, top decimal.Decimal

	putExposure decimal.Decimal

	wheelPutAt, wheelCallAt   decimal.Decimal
	gridFromPut, gridFromCall decimal.Decimal
}

func analyzeGreel(cfg *gobs.GreelConfig, feePct decimal.Decimal) *greelFacts {
	var pairs []*point.Pair
	f := &greelFacts{levels: len(cfg.GridLevels)}
	for _, gp := range cfg.GridLevels {
		p := point.NewPairFromGobPair(gp)
		pairs = append(pairs, p)
		f.shares = f.shares.Add(p.Buy.Size)
		f.top = decimal.Max(f.top, p.Sell.Price)
	}
	f.bottom = pairs[0].Buy.Price
	f.budget = waller.Analyze(pairs, feePct).Budget()
	f.putExposure = f.bottom.Mul(greeler.ContractShares)

	far := cfg.FarPct.Div(d100)
	near := cfg.FarPct.Sub(cfg.HysteresisPct).Div(d100)
	// wheel-put once top <= spot·(1−far); back once top > spot·(1−near).
	f.wheelPutAt = f.top.Div(one.Sub(far))
	f.gridFromPut = f.top.Div(one.Sub(near))
	// wheel-call once bottom >= spot·(1+far); back once bottom < spot·(1+near).
	f.wheelCallAt = f.bottom.Div(one.Add(far))
	f.gridFromCall = f.bottom.Div(one.Add(near))
	return f
}

// printQuery prints each greel's facts, then totals when there is more
// than one.
func printQuery(w io.Writer, cfg *gobs.GreelConfig, bands []*gobs.GreelConfig, feePct decimal.Decimal) {
	fmt.Fprintf(w, "Fee percentage: %s%%\n", feePct)
	fmt.Fprintf(w, "Grid zone: limiters start for levels within %s%% of spot\n", cfg.GridPct)
	fmt.Fprintf(w, "Each greel writes one contract (%s shares); a put strikes at or below its lowest buy, a call at or above its highest sell.\n", greeler.ContractShares)
	fmt.Fprintln(w)

	var budget, exposure decimal.Decimal
	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "Greel\tLevels\tShares\tLowestBuy\tHighestSell\tBudget\tPutExposure\tPutAtSpot>=\tGridBelow\tCallAtSpot<=\tGridAbove\t\n")
	for i, b := range bands {
		f := analyzeGreel(b, feePct)
		budget = budget.Add(f.budget)
		exposure = exposure.Add(f.putExposure)
		fmt.Fprintf(tw, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", i, f.levels, f.shares.StringFixed(2),
			f.bottom.StringFixed(2), f.top.StringFixed(2), f.budget.StringFixed(2), f.putExposure.StringFixed(2),
			f.wheelPutAt.StringFixed(2), f.gridFromPut.StringFixed(2), f.wheelCallAt.StringFixed(2), f.gridFromCall.StringFixed(2))
	}
	tw.Flush()

	if len(bands) > 1 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Greels: %d\n", len(bands))
		fmt.Fprintf(w, "Total budget: %s\n", budget.StringFixed(2))
		fmt.Fprintf(w, "Put exposure if every greel writes a put: %s\n", exposure.StringFixed(2))
	}

	fmt.Fprintln(w)
	printLevelsTo(w, cfg, bands)
}

// printLevelsTo prints the grid levels, per greel when there are several.
func printLevelsTo(w io.Writer, cfg *gobs.GreelConfig, bands []*gobs.GreelConfig) {
	if len(bands) == 1 {
		fprintLevels(w, "", bands[0])
		return
	}
	for i, b := range bands {
		fprintLevels(w, fmt.Sprintf("greel-%d ", i), b)
	}
}

type GreelerQuery struct {
	config ConfigFlags
}

func (c *GreelerQuery) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	cfg, err := c.config.Config()
	if err != nil {
		return err
	}
	printQuery(os.Stdout, cfg, []*gobs.GreelConfig{cfg}, c.config.FeePct())
	return nil
}

func (c *GreelerQuery) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("query", flag.ContinueOnError)
	c.config.SetFlags(fset)
	return "query", fset, cli.CmdFunc(c.Run)
}

func (c *GreelerQuery) Purpose() string {
	return "Prints what a greeler job would need, without creating it"
}

func (c *GreelerQuery) Description() string {
	return `

Command "query" takes the same flags as "greeler add" and prints, without a
server, the grid levels, the stock budget, the put exposure and the spot
prices where the greeler would flip to a put or a call and back to the grid.

`
}

type LadderQuery struct {
	config ConfigFlags
}

func (c *LadderQuery) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	cfg, err := c.config.Config()
	if err != nil {
		return err
	}
	bands, err := greelladder.Cut(cfg)
	if err != nil {
		return err
	}
	if err := greelladder.CheckBands(cfg.AccountName, cfg.ProductID, bands); err != nil {
		return err
	}
	printQuery(os.Stdout, cfg, bands, c.config.FeePct())
	return nil
}

func (c *LadderQuery) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("query", flag.ContinueOnError)
	c.config.SetFlags(fset)
	return "query", fset, cli.CmdFunc(c.Run)
}

func (c *LadderQuery) Purpose() string {
	return "Prints what a greel ladder job would need, without creating it"
}

func (c *LadderQuery) Description() string {
	return `

Command "query" takes the same config flags as "greelladder add", cuts the
range into greels as "add" would, and prints, without a server, each greel's
levels, stock budget, put exposure and flip prices, then the ladder's totals.

`
}
