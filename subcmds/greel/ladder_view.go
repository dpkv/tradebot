// Copyright (c) 2026 Deepak Vankadaru

package greel

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greelladder"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/subcmds/cmdutil"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
	"github.com/visvasity/cli"
)

type LadderList struct {
	cmdutil.DBFlags
}

func (c *LadderList) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	db, closer, err := c.DBFlags.GetDatabase(ctx)
	if err != nil {
		return err
	}
	defer closer()

	keep := func(k string) bool {
		return !strings.Contains(strings.TrimPrefix(k, greelladder.DefaultKeyspace), "/")
	}
	return kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		return listKeys(ctx, r, greelladder.DefaultKeyspace, keep)
	})
}

func (c *LadderList) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	return "list", fset, cli.CmdFunc(c.Run)
}

func (c *LadderList) Purpose() string {
	return "Lists greel ladder jobs"
}

type LadderGet struct {
	cmdutil.DBFlags
}

func (c *LadderGet) Run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("this command takes one greel ladder argument")
	}
	db, closer, err := c.DBFlags.GetDatabase(ctx)
	if err != nil {
		return err
	}
	defer closer()

	return kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		uid, err := resolveUID(ctx, r, args[0])
		if err != nil {
			return err
		}
		v, err := greelladder.Load(ctx, uid, r)
		if err != nil {
			return fmt.Errorf("could not load greel ladder %q: %w", uid, err)
		}
		gv, err := kvutil.Get[gobs.GreelLadderState](ctx, r, path.Join(greelladder.DefaultKeyspace, uid))
		if err != nil {
			return fmt.Errorf("could not load greel ladder %q state: %w", uid, err)
		}

		w := os.Stdout
		printSummary(w, v.Status(nil))
		fmt.Fprintln(w)

		// Gate usage, counted as the gates count it.
		contracts := 0
		var putExposure decimal.Decimal
		for _, g := range v.Greelers() {
			t, e := g.Commitment()
			if t == "" {
				continue
			}
			contracts++
			if t == "PUT" {
				putExposure = putExposure.Add(e)
			}
		}
		opts := gv.V1.Options
		limit := func(k string) string {
			if val := opts[k]; val != "" && val != "0" {
				return val
			}
			return "none"
		}
		fmt.Fprintln(w, "Greels", len(v.Greelers()))
		fmt.Fprintln(w, "Contracts", contracts, "limit", limit("max-contracts"))
		fmt.Fprintln(w, "PutExposure", putExposure.StringFixed(2), "limit", limit("max-put-exposure"))
		printOptions(w, opts)

		now := time.Now()
		for i, g := range v.Greelers() {
			gg, gs, err := loadGreeler(ctx, r, g.UID())
			if err != nil {
				return err
			}
			fmt.Fprintln(w)
			fmt.Fprintf(w, "=== greel-%d %s\n", i, g.UID())
			printGreelerState(w, now, gg, gs)
		}
		return nil
	})
}

func (c *LadderGet) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("get", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	return "get", fset, cli.CmdFunc(c.Run)
}

func (c *LadderGet) Purpose() string {
	return "Prints a greel ladder's state and each of its greels"
}

type LadderPrint struct {
	cmdutil.DBFlags
}

func (c *LadderPrint) Run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("this command takes one greel ladder argument")
	}
	db, closer, err := c.DBFlags.GetDatabase(ctx)
	if err != nil {
		return err
	}
	defer closer()

	return kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		uid, err := resolveUID(ctx, r, args[0])
		if err != nil {
			return err
		}
		return printJSON[gobs.GreelLadderState](ctx, r, path.Join(greelladder.DefaultKeyspace, uid))
	})
}

func (c *LadderPrint) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("print", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	return "print", fset, cli.CmdFunc(c.Run)
}

func (c *LadderPrint) Purpose() string {
	return "Prints a greel ladder gob in JSON format"
}
