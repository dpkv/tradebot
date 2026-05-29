package backtest

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
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
	feePct          decimal.Decimal // derived from strategy's fee-pct, not a CLI flag
	maxTickProgress float64

	debug bool
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
	fset.Float64Var(&f.maxTickProgress, "max-tick-progress", 0, "max price change per tick; engine inserts sub-ticks when exceeded (0 = unlimited)")
	fset.BoolVar(&f.debug, "debug", false, "enable debug logging")
}

func (f *BacktestFlags) check() error {
	if f.product == "" {
		return fmt.Errorf("--product is required")
	}
	if !strings.Contains(f.product, "-") {
		return fmt.Errorf("--product must be in BASE-QUOTE format (e.g. BTC-USD)")
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
		feed, err := datafeed.NewCSVFeed(f.csvFile, f.product, time.RFC3339, time.Minute, begin, end, expander)
		if err != nil {
			return nil, fmt.Errorf("could not create csv feed: %w", err)
		}
		return feed, nil
	}
	return nil, fmt.Errorf("unknown feed type %q", f.feed)
}

type stdoutMessenger struct{}

func (m *stdoutMessenger) SendMessage(_ context.Context, t time.Time, format string, args ...interface{}) {
	fmt.Printf("[%s] %s\n", t.Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// runBacktest sets up the mock exchange, feed, and engine, runs the strategy,
// and prints a P&L summary when the feed is exhausted.
func runBacktest(ctx context.Context, f *BacktestFlags, t trader.Trader) (*BacktestSummary, error) {
	if err := f.check(); err != nil {
		return nil, err
	}
	if f.debug {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	var begin, end time.Time
	if f.begin != "" {
		v, err := time.Parse(dateFormat, f.begin)
		if err != nil {
			return nil, fmt.Errorf("could not parse --begin: %w", err)
		}
		begin = v
	}
	if f.end != "" {
		v, err := time.Parse(dateFormat, f.end)
		if err != nil {
			return nil, fmt.Errorf("could not parse --end: %w", err)
		}
		end = v
	}

	feed, err := f.buildFeed(ctx, begin, end)
	if err != nil {
		return nil, err
	}
	defer feed.Close()

	productDef := f.productDef()
	base, quote := productDef.BaseCurrencyID, productDef.QuoteCurrencyID
	balances := map[string]decimal.Decimal{
		base:  decimal.NewFromFloat(f.baseBalance),
		quote: decimal.NewFromFloat(f.quoteBalance),
	}
	mockEx := mockexchange.NewExchange(f.exchangeName, f.feePct, balances, []*gobs.Product{productDef})
	defer mockEx.Close()

	ep, err := mockEx.OpenSpotProduct(ctx, f.product)
	if err != nil {
		return nil, fmt.Errorf("could not open mock product: %w", err)
	}
	mockProduct := ep.(*mockexchange.Product)
	syncer := trader.NewSyncer()
	engine := mockexchange.NewEngine(feed, mockProduct)
	engine.MaxTickProgress = decimal.NewFromFloat(f.maxTickProgress)
	engine.Syncer = syncer

	rt := &trader.Runtime{
		Exchange:  mockEx,
		Database:  kvmemdb.New(),
		Product:   ep,
		Messenger: &stdoutMessenger{},
		Syncer:    syncer,
	}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	errCh := make(chan error, 1)
	go func() {
		err := t.Run(ctx, rt)
		if err != nil && ctx.Err() == nil {
			cancel(err) // unblock the engine on strategy failure
		}
		errCh <- err
	}()

	// Engine blocks until feed is exhausted, then we stop the strategy.
	cancel(engine.Run(ctx))
	<-errCh

	total, _ := mockEx.Balances()
	lastPrice := mockProduct.LastTickPrice()
	startQuote := decimal.NewFromFloat(f.quoteBalance)
	endQuote := total[quote].Add(total[base].Mul(lastPrice))
	pnl := endQuote.Sub(startQuote)
	pnlPct := pnl.Div(startQuote).Mul(decimal.NewFromInt(100))
	feedBegin, feedEnd := feed.DateRange()
	days := feedEnd.Sub(feedBegin).Hours() / 24
	years := days / 365.25
	var annualizedPct decimal.Decimal
	if years > 0 {
		growth, _ := pnl.Add(startQuote).Div(startQuote).Float64()
		annualized := (math.Pow(growth, 1/years) - 1) * 100
		annualizedPct = decimal.NewFromFloat(annualized)
	}
	s := &BacktestSummary{
		Product:        f.product,
		Begin:          feedBegin.Format(dateFormat),
		End:            feedEnd.Format(dateFormat),
		Base:           base,
		Quote:          quote,
		BaseBalance:    f.baseBalance,
		QuoteBalance:   f.quoteBalance,
		TotalBase:      total[base],
		TotalQuote:     total[quote],
		LastPrice:      lastPrice,
		NLV:            endQuote,
		PeakNLV:        engine.PeakNLV(),
		MinNLV:         engine.MinNLV(),
		MaxDrawdownPct: engine.MaxDrawdownPct(),
		PnL:            pnl,
		PnLPct:         pnlPct,
		AnnualizedPct:  annualizedPct,
	}
	return s, nil
}

// BacktestSummary holds computed results from a backtest run.
type BacktestSummary struct {
	Product, Begin, End   string
	Base, Quote           string
	BaseBalance           float64
	QuoteBalance          float64
	TotalBase, TotalQuote decimal.Decimal
	LastPrice             decimal.Decimal
	NLV                   decimal.Decimal
	PeakNLV, MinNLV      decimal.Decimal
	MaxDrawdownPct        decimal.Decimal
	PnL, PnLPct           decimal.Decimal
	AnnualizedPct         decimal.Decimal
}

func (s *BacktestSummary) Print() {
	startQuote := decimal.NewFromFloat(s.QuoteBalance)
	maxDDFromBudget := startQuote.Sub(s.MinNLV).Div(startQuote).Mul(decimal.NewFromInt(100))
	dollarLoss := s.MinNLV.Sub(startQuote)
	fmt.Printf("\n=== Backtest: %s  %s → %s ===\n", s.Product, s.Begin, s.End)
	fmt.Printf("Starting:            %s=%.4f  %s=%.2f\n", s.Base, s.BaseBalance, s.Quote, s.QuoteBalance)
	fmt.Printf("Ending:              %s=%s  %s=%s  (last price: %s)\n", s.Base, s.TotalBase.StringFixed(4), s.Quote, s.TotalQuote.StringFixed(2), s.LastPrice.StringFixed(2))
	fmt.Printf("Net Liquidation:     %s %s\n", s.NLV.StringFixed(2), s.Quote)
	fmt.Printf("Max Drawdown:        %s%% from peak  |  %s%% from budget (%s %s)  |  NLV range: %s → %s %s\n",
		s.MaxDrawdownPct.StringFixed(2),
		maxDDFromBudget.StringFixed(2), dollarLoss.StringFixed(2), s.Quote,
		s.PeakNLV.StringFixed(2), s.MinNLV.StringFixed(2), s.Quote)
	fmt.Printf("P&L:                 %s (%s%%)  annualized: %s%%\n", s.PnL.StringFixed(2), s.PnLPct.StringFixed(2), s.AnnualizedPct.StringFixed(2))
}
