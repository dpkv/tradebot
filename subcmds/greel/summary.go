// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/greelladder"
	"github.com/bvk/tradebot/job"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/namer"
	"github.com/bvk/tradebot/subcmds/cmdutil"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

// SummaryItem is one greeler's or ladder's summary row.
type SummaryItem struct {
	UID  string
	Name string

	LowestBuyPrice   decimal.Decimal
	HighestSellPrice decimal.Decimal

	Profit    decimal.Decimal
	ReturnPct decimal.Decimal
	APR       decimal.Decimal
	NumDays   decimal.Decimal
	FeePct    decimal.Decimal

	*gobs.JobData
	*gobs.Summary
}

// summaryKind is what differs between the greeler and ladder summaries.
type summaryKind struct {
	name     string // "greeler" or "greelladder"
	keyspace string

	// summary loads a job's summary and its grid's price bounds.
	summary func(ctx context.Context, r kv.Reader, uid string, period *timerange.Range) (*gobs.Summary, decimal.Decimal, decimal.Decimal, error)
}

var greelerKind = summaryKind{
	name:     "greeler",
	keyspace: greeler.DefaultKeyspace,
	summary: func(ctx context.Context, r kv.Reader, uid string, period *timerange.Range) (*gobs.Summary, decimal.Decimal, decimal.Decimal, error) {
		v, gv, err := loadGreeler(ctx, r, uid)
		if err != nil {
			return nil, decimal.Zero, decimal.Zero, err
		}
		low, high := priceBounds(gv.V1.Config)
		return v.GetSummary(period), low, high, nil
	},
}

var ladderKind = summaryKind{
	name:     "greelladder",
	keyspace: greelladder.DefaultKeyspace,
	summary: func(ctx context.Context, r kv.Reader, uid string, period *timerange.Range) (*gobs.Summary, decimal.Decimal, decimal.Decimal, error) {
		v, err := greelladder.Load(ctx, uid, r)
		if err != nil {
			return nil, decimal.Zero, decimal.Zero, fmt.Errorf("could not load greel ladder %q: %w", uid, err)
		}
		var low, high decimal.Decimal
		for i, g := range v.Greelers() {
			gv, err := kvutil.Get[gobs.GreelerState](ctx, r, path.Join(greeler.DefaultKeyspace, g.UID()))
			if err != nil {
				return nil, decimal.Zero, decimal.Zero, fmt.Errorf("could not load greeler %q state: %w", g.UID(), err)
			}
			l, h := priceBounds(gv.V1.Config)
			if i == 0 || l.LessThan(low) {
				low = l
			}
			high = decimal.Max(high, h)
		}
		return v.GetSummary(period), low, high, nil
	},
}

func priceBounds(cfg *gobs.GreelConfig) (low, high decimal.Decimal) {
	low = cfg.GridLevels[0].Buy.Price
	for _, p := range cfg.GridLevels {
		high = decimal.Max(high, p.Sell.Price)
	}
	return low, high
}

type summaryCmd struct {
	cmdutil.DBFlags

	kind summaryKind

	beginTime, endTime string

	states string

	format string

	templateForm string

	tableColumns      string
	tableTotalColumns string

	skipUninteresting bool
}

func (c *summaryCmd) Purpose() string {
	if c.kind.name == "greelladder" {
		return "Prints summary of all or selected greel ladders"
	}
	return "Prints summary of all or selected greelers"
}

func (c *summaryCmd) Description() string {
	return fmt.Sprintf(`

This "summary" subcommand prints the summary of one or more or all %[1]s
jobs, in the same formats and with the same flags as "waller summary". Jobs
can be selected by name or uid as arguments; all are selected when no
arguments are given. Use -f with one of json|table|template to pick the
output format. PremiumValue is all option premium collected;
OpenPremiumValue is the part whose position hasn't settled yet, which
doesn't count toward profit until it does.

EXAMPLES

    $ tradebot %[1]s summary -f table
    $ tradebot %[1]s summary -f json -begin-time 2026-01-01
    $ tradebot %[1]s summary -f template --template-form '{{.Profit}} -- {{.Name}}'

`, c.kind.name)
}

func (c *summaryCmd) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("summary", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	fset.StringVar(&c.beginTime, "begin-time", "", "Begin time for summary time period")
	fset.StringVar(&c.endTime, "end-time", "", "End time for summary time period")
	fset.StringVar(&c.states, "states", "", "When non-empty filters out jobs by their state")
	fset.StringVar(&c.format, "f", "table", "Printed output format (table|json|template)")
	fset.StringVar(&c.templateForm, "template-form", "", "Golang text/template for printing summary")
	fset.StringVar(&c.tableColumns, "table-columns", "Name,State,ProductID,BeginAt,EndAt,Budget,Profit,ReturnPct,NumDays,APR,NumBuys,NumSells,PremiumValue,OpenPremiumValue,BoughtFees,BoughtValue,SoldFees,SoldValue,UnsoldFees,UnsoldSize,UnsoldValue", "Columns to print in the table form")
	fset.StringVar(&c.tableTotalColumns, "table-total-columns", "Profit,PremiumValue,OpenPremiumValue,BoughtFees,BoughtValue,SoldFees,SoldValue,UnsoldFees,UnsoldValue", "Columns to print in the table totals line")
	fset.BoolVar(&c.skipUninteresting, "skip-uninteresting", true, "When false, summary info is printed for all jobs")
	return "summary", fset, cli.CmdFunc(c.run)
}

func parsePeriod(now time.Time, beginTime, endTime string) (*timerange.Range, error) {
	parseTime := func(s string) (time.Time, error) {
		if d, err := time.ParseDuration(s); err == nil {
			return now.Add(d), nil
		}
		if v, err := time.Parse("2006-01-02", s); err == nil {
			return v, nil
		}
		return time.Parse(time.RFC3339, s)
	}
	if beginTime == "" && endTime == "" {
		return nil, nil
	}
	period := &timerange.Range{End: now}
	if beginTime != "" {
		v, err := parseTime(beginTime)
		if err != nil {
			return nil, err
		}
		period.Begin = v
	}
	if endTime != "" {
		v, err := parseTime(endTime)
		if err != nil {
			return nil, err
		}
		period.End = v
	}
	return period, nil
}

// topLevelUIDs lists the jobs under keyspace, leaving out a ladder's
// greelers.
func topLevelUIDs(ctx context.Context, r kv.Reader, keyspace string) ([]string, error) {
	beg, end := kvutil.PathRange(keyspace)
	it, err := r.Ascend(ctx, beg, end)
	if err != nil {
		return nil, err
	}
	defer kv.Close(it)

	var uids []string
	for k, _, err := it.Fetch(ctx, false); err == nil; k, _, err = it.Fetch(ctx, true) {
		uid := strings.TrimPrefix(k, keyspace)
		if _, err := uuid.Parse(uid); err != nil {
			continue
		}
		uids = append(uids, uid)
	}
	if _, _, err := it.Fetch(ctx, false); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return uids, nil
}

func (c *summaryCmd) run(ctx context.Context, args []string) error {
	now := time.Now()
	period, err := parsePeriod(now, c.beginTime, c.endTime)
	if err != nil {
		return err
	}

	var states []gobs.State
	if len(c.states) != 0 {
		valid := []gobs.State{gobs.PAUSED, gobs.RUNNING, gobs.COMPLETED, gobs.CANCELED, gobs.FAILED}
		for _, s := range strings.Split(c.states, ",") {
			state := gobs.State(strings.ToUpper(s))
			if !slices.Contains(valid, state) {
				return fmt.Errorf("invalid job state %q", s)
			}
			states = append(states, state)
		}
	}

	db, closer, err := c.DBFlags.GetDatabase(ctx)
	if err != nil {
		return err
	}
	defer closer()

	runner := job.NewRunner(db)

	var items []*SummaryItem
	collect := func(ctx context.Context, r kv.Reader) error {
		uids, err := topLevelUIDs(ctx, r, c.kind.keyspace)
		if err != nil {
			return err
		}
		for _, uid := range uids {
			jd, err := runner.Get(ctx, r, uid)
			if err != nil {
				return fmt.Errorf("could not determine job state for %s %q: %w", c.kind.name, uid, err)
			}
			if len(states) != 0 && !slices.Contains(states, jd.State) {
				continue
			}
			name, _, _, err := namer.Resolve(ctx, r, uid)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				name = uid
			}
			if len(args) != 0 && !slices.Contains(args, name) && !slices.Contains(args, uid) {
				continue
			}

			sum, low, high, err := c.kind.summary(ctx, r, uid, period)
			if err != nil {
				return err
			}
			if period != nil {
				if sum.BeginAt.IsZero() {
					sum.BeginAt = period.Begin
				}
				if sum.EndAt.IsZero() || jd.State.IsRunning() {
					sum.EndAt = period.End
				}
			}
			if jd.State.IsRunning() && sum.EndAt.IsZero() {
				sum.EndAt = now
			}
			if sum.IsZero() && c.skipUninteresting && len(states) == 0 && !jd.State.IsRunning() {
				continue
			}

			items = append(items, &SummaryItem{
				UID:              uid,
				Name:             name,
				LowestBuyPrice:   low,
				HighestSellPrice: high,
				Profit:           sum.Profit(),
				ReturnPct:        sum.ReturnPct(),
				APR:              sum.AnnualPct(),
				NumDays:          sum.NumDays(),
				FeePct:           sum.FeePct(),
				JobData:          jd,
				Summary:          sum,
			})
		}
		return nil
	}
	if err := kv.WithReader(ctx, db, collect); err != nil {
		return err
	}

	// Order by job state, product, begin time and name, as waller does.
	order := []gobs.State{gobs.RUNNING, gobs.PAUSED, gobs.COMPLETED, gobs.FAILED, gobs.CANCELED}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if x, y := slices.Index(order, a.State), slices.Index(order, b.State); x != y {
			return x < y
		}
		if a.ProductID != b.ProductID {
			return a.ProductID < b.ProductID
		}
		if !a.BeginAt.Equal(b.BeginAt) {
			return a.BeginAt.Before(b.BeginAt)
		}
		return a.Name < b.Name
	})

	switch strings.ToLower(c.format) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	case "template":
		return c.printTemplate(os.Stdout, items)
	case "table":
		c.printTable(os.Stdout, items)
		return nil
	default:
		return fmt.Errorf("unknown/invalid print format %q", c.format)
	}
}

func (c *summaryCmd) printTemplate(w io.Writer, items []*SummaryItem) error {
	format := "{{.}}"
	if len(c.templateForm) != 0 {
		format = c.templateForm
	}
	tmpl, err := template.New("print").Parse(format + "\n")
	if err != nil {
		return fmt.Errorf("could not parse print-template: %w", err)
	}
	for _, item := range items {
		if err := tmpl.Execute(w, item); err != nil {
			return fmt.Errorf("could not execute the format template: %v", err)
		}
	}
	return nil
}

// printTable prints the chosen columns in their given order, with a
// totals line, as waller summary does.
func (c *summaryCmd) printTable(w io.Writer, items []*SummaryItem) {
	cols := strings.Split(c.tableColumns, ",")
	totalCols := strings.Split(c.tableTotalColumns, ",")

	var fields []reflect.StructField
	for _, f := range reflect.VisibleFields(reflect.TypeFor[SummaryItem]()) {
		if !f.Anonymous && f.IsExported() && slices.Contains(cols, f.Name) {
			fields = append(fields, f)
		}
	}
	slices.SortStableFunc(fields, func(a, b reflect.StructField) int {
		return cmp.Compare(slices.Index(cols, a.Name), slices.Index(cols, b.Name))
	})

	tw := tabwriter.NewWriter(w, 0, 0, 1, ' ', tabwriter.AlignRight)
	for _, f := range fields {
		fmt.Fprintf(tw, "%s\t", f.Name)
	}
	fmt.Fprintln(tw)
	for _, item := range items {
		for _, f := range fields {
			fmt.Fprintf(tw, "%s\t", formatValue(reflect.ValueOf(item).Elem().FieldByIndex(f.Index)))
		}
		fmt.Fprintln(tw)
	}
	if len(items) > 1 {
		for _, f := range fields {
			if !slices.Contains(totalCols, f.Name) {
				fmt.Fprintf(tw, "\t")
				continue
			}
			var total decimal.Decimal
			for _, item := range items {
				if x, ok := reflect.ValueOf(item).Elem().FieldByIndex(f.Index).Interface().(decimal.Decimal); ok {
					total = total.Add(x)
				}
			}
			fmt.Fprintf(tw, "%s\t", total.StringFixed(3))
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()
}

func formatValue(v reflect.Value) string {
	switch x := v.Interface().(type) {
	case decimal.Decimal:
		return x.StringFixed(3)
	case time.Time:
		return x.Format("2006-01-02")
	default:
		return fmt.Sprint(x)
	}
}

type GreelerSummary struct{ summaryCmd }

func (c *GreelerSummary) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	c.kind = greelerKind
	return c.summaryCmd.Command()
}

type LadderSummary struct{ summaryCmd }

func (c *LadderSummary) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	c.kind = ladderKind
	return c.summaryCmd.Command()
}

func (c *GreelerSummary) Purpose() string {
	c.kind = greelerKind
	return c.summaryCmd.Purpose()
}

func (c *GreelerSummary) Description() string {
	c.kind = greelerKind
	return c.summaryCmd.Description()
}

func (c *LadderSummary) Purpose() string {
	c.kind = ladderKind
	return c.summaryCmd.Purpose()
}

func (c *LadderSummary) Description() string {
	c.kind = ladderKind
	return c.summaryCmd.Description()
}
