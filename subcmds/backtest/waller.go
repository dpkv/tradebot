package backtest

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/point"
	wallersubcmd "github.com/bvk/tradebot/subcmds/waller"
	"github.com/bvk/tradebot/waller"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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
	if err := runBacktest(ctx, &c.flags, w); err != nil {
		return err
	}
	quote := strings.SplitN(c.flags.product, "-", 2)[1]
	printWallerPairs(w.Actions(), quote)
	return nil
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

func printWallerPairs(actions []*gobs.Action, quote string) {
	if len(actions) == 0 {
		return
	}

	type looperGroup struct {
		key   string
		buys  []*gobs.Action
		sells []*gobs.Action
	}
	groupMap := make(map[string]*looperGroup)
	var groupOrder []string
	for _, a := range actions {
		g, ok := groupMap[a.PairingKey]
		if !ok {
			g = &looperGroup{key: a.PairingKey}
			groupMap[a.PairingKey] = g
			groupOrder = append(groupOrder, a.PairingKey)
		}
		p := point.Point(a.Point)
		if p.Side() == "BUY" {
			g.buys = append(g.buys, a)
		} else {
			g.sells = append(g.sells, a)
		}
	}

	fmt.Printf("\n=== Buy/Sell Pairs ===\n")
	for _, key := range groupOrder {
		g := groupMap[key]
		n := min(len(g.buys), len(g.sells))
		buyPrice := g.buys[0].Point.Price.StringFixed(2)
		sellPrice := g.sells[0].Point.Price.StringFixed(2)
		fmt.Printf("\nBUY@%s / SELL@%s — %d completed\n", buyPrice, sellPrice, n)
		if n == 0 {
			continue
		}
		fmt.Printf("  %-4s  %-19s  %-19s  %s\n", "#", "Buy Time", "Sell Time", "Gain ("+quote+")")
		for i := range n {
			buy, sell := g.buys[i], g.sells[i]
			gain := actionValue(sell.Orders).Sub(actionFees(sell.Orders)).
				Sub(actionValue(buy.Orders)).Sub(actionFees(buy.Orders))
			fmt.Printf("  %-4d  %-19s  %-19s  %s\n",
				i+1,
				actionFinishTime(buy.Orders).Format("2006-01-02 15:04:05"),
				actionFinishTime(sell.Orders).Format("2006-01-02 15:04:05"),
				gain.StringFixed(2))
		}
		if len(g.buys) > n {
			fmt.Printf("  (%d buys pending sell)\n", len(g.buys)-n)
		}
	}
}

func actionFinishTime(orders []*gobs.Order) time.Time {
	var t time.Time
	for _, o := range orders {
		if o.FinishTime.Time.After(t) {
			t = o.FinishTime.Time
		}
	}
	return t
}

func actionValue(orders []*gobs.Order) decimal.Decimal {
	var sum decimal.Decimal
	for _, o := range orders {
		sum = sum.Add(o.FilledSize.Mul(o.FilledPrice))
	}
	return sum
}

func actionFees(orders []*gobs.Order) decimal.Decimal {
	var sum decimal.Decimal
	for _, o := range orders {
		sum = sum.Add(o.FilledFee)
	}
	return sum
}
