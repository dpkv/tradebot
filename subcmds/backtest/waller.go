package backtest

import (
	"context"
	"flag"
	"fmt"

	wallersubcmd "github.com/bvk/tradebot/subcmds/waller"
	"github.com/bvk/tradebot/waller"
	"github.com/google/uuid"
	"github.com/visvasity/cli"
)

type Waller struct {
	flags BacktestFlags
	spec  wallersubcmd.Spec
}

func (c *Waller) Run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("this command takes no arguments")
	}
	if err := c.spec.Check(); err != nil {
		return err
	}
	pairs := c.spec.BuySellPairs()
	if len(pairs) == 0 {
		return fmt.Errorf("could not determine buy/sell pairs from spec")
	}
	uid := uuid.New().String()
	w, err := waller.New(uid, c.flags.exchangeName, c.flags.product, pairs)
	if err != nil {
		return fmt.Errorf("could not create waller: %w", err)
	}
	return runBacktest(ctx, &c.flags, w)
}

func (c *Waller) Command() (string, *flag.FlagSet, cli.CmdFunc) {
	fset := flag.NewFlagSet("waller", flag.ContinueOnError)
	c.flags.SetFlags(fset)
	c.spec.SetFlags(fset)
	return "waller", fset, cli.CmdFunc(c.Run)
}

func (c *Waller) Purpose() string {
	return "Backtest a waller strategy against historical price data"
}

func (c *Waller) Description() string {
	return `

Command "waller" runs a waller strategy against historical candle data
from a Coinbase database or CSV file and prints a P&L summary.

Example:
  tradebot backtest waller \
    --product=BTC-USD --begin=2024-01-01 --end=2024-06-01 \
    --feed=coinbase --data-dir=~/.tradebot \
    --quote-balance=10000 \
    --begin-price=60000 --end-price=70000 \
    --buy-interval=1000 --profit-margin=500 --buy-size=0.001

`
}
