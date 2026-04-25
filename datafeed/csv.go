package datafeed

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CSVFeed is a DataFeed that reads OHLC candle data from a CSV file.
//
// The CSV must have a header row with columns: time, open, low, high, close
// (case-insensitive, in any order). Additional columns are ignored.
// Time values must be formatted according to the timeFormat parameter.
// Candle duration must be supplied explicitly since it is not stored in the file.
type CSVFeed struct {
	productID string
	expander  CandleExpander
	candles   []csvCandle
	candleIdx int
	ticks     []Tick
	tickIdx   int
}

type csvCandle struct {
	t                      time.Time
	duration               time.Duration
	open, low, high, close decimal.Decimal
}

// NewCSVFeed parses a CSV file and returns a feed for the given productID.
// timeFormat is the Go time layout used to parse the time column (e.g. time.RFC3339).
// duration is the candle interval (e.g. time.Minute for 1-minute candles).
func NewCSVFeed(path, productID, timeFormat string, duration time.Duration, expander CandleExpander) (*CSVFeed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open csv file: %w", err)
	}
	defer f.Close()

	r := csv.NewReader(f)

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("could not read csv header: %w", err)
	}
	colIndex, err := csvColumnIndex(header)
	if err != nil {
		return nil, err
	}

	var candles []csvCandle
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("could not read csv row: %w", err)
		}
		t, err := time.Parse(timeFormat, row[colIndex["time"]])
		if err != nil {
			return nil, fmt.Errorf("could not parse time %q: %w", row[colIndex["time"]], err)
		}
		open, err := decimal.NewFromString(row[colIndex["open"]])
		if err != nil {
			return nil, fmt.Errorf("could not parse open price: %w", err)
		}
		low, err := decimal.NewFromString(row[colIndex["low"]])
		if err != nil {
			return nil, fmt.Errorf("could not parse low price: %w", err)
		}
		high, err := decimal.NewFromString(row[colIndex["high"]])
		if err != nil {
			return nil, fmt.Errorf("could not parse high price: %w", err)
		}
		close, err := decimal.NewFromString(row[colIndex["close"]])
		if err != nil {
			return nil, fmt.Errorf("could not parse close price: %w", err)
		}
		candles = append(candles, csvCandle{t: t, duration: duration, open: open, low: low, high: high, close: close})
	}

	if len(candles) == 0 {
		return nil, fmt.Errorf("no candle rows found in %s", path)
	}
	return &CSVFeed{
		productID: productID,
		expander:  expander,
		candles:   candles,
	}, nil
}

func (f *CSVFeed) ProductID() string { return f.productID }

// Next returns the next tick. Returns io.EOF when all candles are exhausted.
func (f *CSVFeed) Next(ctx context.Context) (Tick, error) {
	for {
		if f.tickIdx < len(f.ticks) {
			t := f.ticks[f.tickIdx]
			f.tickIdx++
			return t, nil
		}
		if f.candleIdx >= len(f.candles) {
			return Tick{}, io.EOF
		}
		c := f.candles[f.candleIdx]
		f.candleIdx++
		f.ticks = f.expander(c.t, c.duration, c.open, c.low, c.high, c.close)
		f.tickIdx = 0
	}
}

func (f *CSVFeed) Close() error {
	f.candles = nil
	f.ticks = nil
	return nil
}

func csvColumnIndex(header []string) (map[string]int, error) {
	required := []string{"time", "open", "low", "high", "close"}
	idx := make(map[string]int, len(required))
	for i, col := range header {
		idx[strings.ToLower(strings.TrimSpace(col))] = i
	}
	for _, col := range required {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("csv missing required column %q", col)
		}
	}
	return idx, nil
}
