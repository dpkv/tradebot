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
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/subcmds/cmdutil"
	"github.com/bvkgo/kv"
	"github.com/visvasity/cli"
)

type GreelerList struct {
	cmdutil.DBFlags

	all bool
}

func (c *GreelerList) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	db, closer, err := c.DBFlags.GetDatabase(ctx)
	if err != nil {
		return err
	}
	defer closer()

	// A ladder's greelers are keyed <ladder-uid>/greeler-NNNNNN.
	keep := func(k string) bool {
		return c.all || !strings.Contains(strings.TrimPrefix(k, greeler.DefaultKeyspace), "/")
	}
	return kv.WithReader(ctx, db, func(ctx context.Context, r kv.Reader) error {
		return listKeys(ctx, r, greeler.DefaultKeyspace, keep)
	})
}

func (c *GreelerList) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("list", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	fset.BoolVar(&c.all, "all", false, "when true, also lists the greelers that belong to ladders")
	return "list", fset, cli.CmdFunc(c.Run)
}

func (c *GreelerList) Purpose() string {
	return "Lists greeler jobs"
}

type GreelerGet struct {
	cmdutil.DBFlags
}

func (c *GreelerGet) Run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("this command takes one greeler argument")
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
		v, gv, err := loadGreeler(ctx, r, uid)
		if err != nil {
			return err
		}
		printSummary(os.Stdout, v.Status(nil))
		fmt.Println()
		printGreelerState(os.Stdout, time.Now(), v, gv)
		return nil
	})
}

func (c *GreelerGet) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("get", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	return "get", fset, cli.CmdFunc(c.Run)
}

func (c *GreelerGet) Purpose() string {
	return "Prints a greeler's state"
}

type GreelerPrint struct {
	cmdutil.DBFlags
}

func (c *GreelerPrint) Run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("this command takes one greeler argument")
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
		return printJSON[gobs.GreelerState](ctx, r, path.Join(greeler.DefaultKeyspace, uid))
	})
}

func (c *GreelerPrint) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("print", flag.ContinueOnError)
	c.DBFlags.SetFlags(fset)
	return "print", fset, cli.CmdFunc(c.Run)
}

func (c *GreelerPrint) Purpose() string {
	return "Prints a greeler gob in JSON format"
}
