package backtest

import (
	"context"
	"flag"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/bvk/tradebot/coinbase"
	"github.com/bvk/tradebot/datafeed"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/mockexchange"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv/kvmemdb"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
	"github.com/shopspring/decimal"
)

const dateFormat = "2006-01-02"

// BacktestFlags holds flags shared across all backtest subcommands.
type BacktestFlags struct {
	product      string
	exchangeName string
	begin        string
	end          string

	feed     string // "coinbase" or "csv"
	dataDir  string // path to DB with candle data (coinbase feed)
	csvFile  string // path to CSV file (csv feed)
	expander string // "open-low-high-close" or "open-high-low-close"

	baseBalance  float64
	quoteBalance float64
	baseMinSize  float64
	feeRate      float64
}

func (f *BacktestFlags) SetFlags(fset *flag.FlagSet) {
	fset.StringVar(&f.product, "product", "", "product id (e.g. BTC-USD)")
	fset.StringVar(&f.exchangeName, "exchange", "coinbase", "exchange name")
	fset.StringVar(&f.begin, "begin", "", "backtest start date (YYYY-MM-DD)")
	fset.StringVar(&f.end, "end", "", "backtest end date (YYYY-MM-DD)")
	fset.StringVar(&f.feed, "feed", "coinbase", "data feed type: coinbase or csv")
	fset.StringVar(&f.dataDir, "data-dir", "", "path to database with candle data (for coinbase feed)")
	fset.StringVar(&f.csvFile, "csv-file", "", "path to CSV file (for csv feed)")
	fset.StringVar(&f.expander, "expander", "open-low-high-close", "candle expander: open-low-high-close or open-high-low-close")
	fset.Float64Var(&f.baseBalance, "base-balance", 0, "starting base currency balance (e.g. BTC amount)")
	fset.Float64Var(&f.quoteBalance, "quote-balance", 0, "starting quote currency balance (e.g. USD amount)")
	fset.Float64Var(&f.baseMinSize, "base-min-size", 0, "minimum base order size")
	fset.Float64Var(&f.feeRate, "fee-rate", 0.006, "exchange fee rate (e.g. 0.006 for 0.6%)")
}

func (f *BacktestFlags) check() error {
	if f.product == "" {
		return fmt.Errorf("--product is required")
	}
	if !strings.Contains(f.product, "-") {
		return fmt.Errorf("--product must be in BASE-QUOTE format (e.g. BTC-USD)")
	}
	if f.begin == "" || f.end == "" {
		return fmt.Errorf("--begin and --end are required")
	}
	if f.quoteBalance <= 0 && f.baseBalance <= 0 {
		return fmt.Errorf("at least one of --base-balance or --quote-balance must be positive")
	}
	switch f.feed {
	case "coinbase":
		if f.dataDir == "" {
			return fmt.Errorf("--data-dir is required for coinbase feed")
		}
	case "csv":
		if f.csvFile == "" {
			return fmt.Errorf("--csv-file is required for csv feed")
		}
	default:
		return fmt.Errorf("unknown --feed %q: must be coinbase or csv", f.feed)
	}
	switch f.expander {
	case "open-low-high-close", "open-high-low-close":
	default:
		return fmt.Errorf("unknown --expander %q", f.expander)
	}
	return nil
}

func (f *BacktestFlags) productDef() *gobs.Product {
	parts := strings.SplitN(f.product, "-", 2)
	return &gobs.Product{
		ProductID:       f.product,
		Status:          "ONLINE",
		BaseCurrencyID:  parts[0],
		QuoteCurrencyID: parts[1],
		BaseMinSize:     decimal.NewFromFloat(f.baseMinSize),
	}
}

func (f *BacktestFlags) buildFeed(ctx context.Context, begin, end time.Time) (datafeed.DataFeed, error) {
	var expander datafeed.CandleExpander
	if f.expander == "open-low-high-close" {
		expander = datafeed.OpenLowHighClose
	} else {
		expander = datafeed.OpenHighLowClose
	}

	switch f.feed {
	case "coinbase":
		isGoodKey := func(k string) bool { return path.IsAbs(k) && k == path.Clean(k) }
		bopts := badger.DefaultOptions(f.dataDir).WithLogger(nil)
		bdb, err := badger.Open(bopts)
		if err != nil {
			return nil, fmt.Errorf("could not open data-dir %q: %w", f.dataDir, err)
		}
		liveDB := kvbadger.New(bdb, isGoodKey)
		ds := coinbase.NewDatastore(liveDB)
		feed, err := datafeed.NewCoinbaseDBFeed(ctx, ds, f.product, begin, end, expander)
		if err != nil {
			bdb.Close()
			return nil, fmt.Errorf("could not create coinbase feed: %w", err)
		}
		return feed, nil

	case "csv":
		feed, err := datafeed.NewCSVFeed(f.csvFile, f.product, time.RFC3339, time.Minute, expander)
		if err != nil {
			return nil, fmt.Errorf("could not create csv feed: %w", err)
		}
		return feed, nil
	}
	return nil, fmt.Errorf("unknown feed type %q", f.feed)
}

type noopMessenger struct{}

func (m *noopMessenger) SendMessage(_ context.Context, _ time.Time, _ string, _ ...interface{}) {}

// runBacktest sets up the mock exchange, feed, and engine, runs the strategy,
// and prints a P&L summary when the feed is exhausted.
func runBacktest(ctx context.Context, f *BacktestFlags, t trader.Trader) error {
	if err := f.check(); err != nil {
		return err
	}

	begin, err := time.Parse(dateFormat, f.begin)
	if err != nil {
		return fmt.Errorf("could not parse --begin: %w", err)
	}
	end, err := time.Parse(dateFormat, f.end)
	if err != nil {
		return fmt.Errorf("could not parse --end: %w", err)
	}

	feed, err := f.buildFeed(ctx, begin, end)
	if err != nil {
		return err
	}
	defer feed.Close()

	productDef := f.productDef()
	base, quote := productDef.BaseCurrencyID, productDef.QuoteCurrencyID
	balances := map[string]decimal.Decimal{
		base:  decimal.NewFromFloat(f.baseBalance),
		quote: decimal.NewFromFloat(f.quoteBalance),
	}
	mockEx := mockexchange.NewExchange(f.exchangeName, decimal.NewFromFloat(f.feeRate), balances, []*gobs.Product{productDef})
	defer mockEx.Close()

	ep, err := mockEx.OpenSpotProduct(ctx, f.product)
	if err != nil {
		return fmt.Errorf("could not open mock product: %w", err)
	}
	mockProduct := ep.(*mockexchange.Product)
	engine := mockexchange.NewEngine(feed, mockProduct)

	rt := &trader.Runtime{
		Exchange:  mockEx,
		Database:  kvmemdb.New(),
		Product:   ep,
		Messenger: &noopMessenger{},
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	errCh := make(chan error, 1)
	go func() { errCh <- t.Run(ctx, rt) }()

	// Engine blocks until feed is exhausted, then we stop the strategy.
	cancel(engine.Run(ctx))
	<-errCh

	// Print P&L summary.
	total, available := mockEx.Balances()
	startQuote := decimal.NewFromFloat(f.quoteBalance)
	endQuote := total[quote]
	pnl := endQuote.Sub(startQuote)
	pnlPct := pnl.Div(startQuote).Mul(decimal.NewFromInt(100))

	fmt.Printf("\n=== Backtest: %s  %s → %s ===\n", f.product, f.begin, f.end)
	fmt.Printf("Starting:  %s=%.4f  %s=%.2f\n", base, f.baseBalance, quote, f.quoteBalance)
	fmt.Printf("Ending:    %s=%s  %s=%s\n", base, total[base].StringFixed(4), quote, total[quote].StringFixed(2))
	fmt.Printf("Available: %s=%s  %s=%s\n", base, available[base].StringFixed(4), quote, available[quote].StringFixed(2))
	fmt.Printf("P&L:       %s (%s%%)\n", pnl.StringFixed(2), pnlPct.StringFixed(2))
	return nil
}
