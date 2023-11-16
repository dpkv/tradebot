// Copyright (c) 2023 BVK Chaitanya

package waller

import (
	"context"
	"flag"
	"fmt"

	"github.com/bvk/tradebot/cli"
	"github.com/bvk/tradebot/subcmds/db"
	"github.com/bvk/tradebot/waller"
	"github.com/bvkgo/kv"
)

type Upgrade struct {
	db.Flags
}

func (c *Upgrade) Run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("this command takes one (key) argument")
	}

	upgrader := func(ctx context.Context, rw kv.ReadWriter) error {
		for _, arg := range args {
			w, err := waller.Load(ctx, arg, rw)
			if err != nil {
				return fmt.Errorf("could not load waller at key %q: %w", arg, err)
			}
			if err := w.Save(ctx, rw); err != nil {
				return fmt.Errorf("could not save waller at key %q: %w", arg, err)
			}
		}
		return nil
	}

	db, err := c.Flags.GetDatabase(ctx)
	if err != nil {
		return err
	}
	if err := kv.WithReadWriter(ctx, db, upgrader); err != nil {
		return err
	}
	return nil
}

func (c *Upgrade) Command() (*flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	c.Flags.SetFlags(fset)
	return fset, cli.CmdFunc(c.Run)
}

func (c *Upgrade) Synopsis() string {
	return "Upgrades one or more Waller's persistent state"
}