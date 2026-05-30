package datafeed

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// PostgresFeed is a DataFeed that reads OHLC candle data from a PostgreSQL
// database (daily_candle, hourly_candle, or minute_candle tables).
type PostgresFeed struct {
	symbol     string
	expander   CandleExpander
	timeframe  Timeframe
	candles    []pgCandle
	candleIdx  int
	ticks      []Tick
	tickIdx    int
	rangeBegin time.Time
	rangeEnd   time.Time
}

type pgCandle struct {
	t                      time.Time
	open, low, high, close decimal.Decimal
}

// NewPostgresFeed connects to the given DSN, queries candles for symbol in the
// table corresponding to tf, and returns a feed that emits ticks via expander.
// Zero values for begin or end mean no lower/upper bound respectively.
func NewPostgresFeed(ctx context.Context, dsn, symbol string, tf Timeframe, begin, end time.Time, expander CandleExpander) (*PostgresFeed, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("could not connect to postgres: %w", err)
	}
	defer conn.Close(ctx)

	candles, err := queryCandles(ctx, conn, symbol, tf, begin, end)
	if err != nil {
		return nil, err
	}
	if len(candles) == 0 {
		if begin.IsZero() && end.IsZero() {
			return nil, fmt.Errorf("no %s candles found for %s", tf, symbol)
		}
		return nil, fmt.Errorf("no %s candles found for %s between %s and %s", tf, symbol, begin.Format("2006-01-02"), end.Format("2006-01-02"))
	}
	last := candles[len(candles)-1]
	return &PostgresFeed{
		symbol:     symbol,
		expander:   expander,
		timeframe:  tf,
		candles:    candles,
		rangeBegin: candles[0].t,
		rangeEnd:   last.t.Add(tf.Duration()),
	}, nil
}

func queryCandles(ctx context.Context, conn *pgx.Conn, symbol string, tf Timeframe, begin, end time.Time) ([]pgCandle, error) {
	var query string
	var args []any

	switch tf {
	case TimeframeDaily:
		query, args = buildDailyQuery(symbol, begin, end)
	case TimeframeHourly:
		query, args = buildQuery("hourly_candle", symbol, begin, end)
	case TimeframeMinute:
		query, args = buildQuery("minute_candle", symbol, begin, end)
	default:
		return nil, fmt.Errorf("unsupported timeframe %s", tf)
	}

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("could not query %s candles for %s: %w", tf, symbol, err)
	}
	defer rows.Close()

	var candles []pgCandle
	for rows.Next() {
		var t time.Time
		var open, high, low, close float64
		if err := rows.Scan(&t, &open, &high, &low, &close); err != nil {
			return nil, fmt.Errorf("could not scan candle row: %w", err)
		}
		candles = append(candles, pgCandle{
			t:     t,
			open:  decimal.NewFromFloat(open),
			high:  decimal.NewFromFloat(high),
			low:   decimal.NewFromFloat(low),
			close: decimal.NewFromFloat(close),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading candle rows: %w", err)
	}
	return candles, nil
}

// buildDailyQuery builds a query for daily_candle.
// date::timestamp treats the date as a local timestamp with no timezone, and
// AT TIME ZONE 'UTC' then interprets it as UTC — giving midnight UTC regardless
// of the server's timezone setting.
// The WHERE filters cast the timestamptz bounds to date for a timezone-safe comparison.
func buildDailyQuery(symbol string, begin, end time.Time) (string, []any) {
	args := []any{symbol}
	q := `SELECT date::timestamp AT TIME ZONE 'UTC', open, high, low, close FROM daily_candle WHERE symbol = $1`
	if !begin.IsZero() {
		args = append(args, begin)
		q += fmt.Sprintf(" AND date >= ($%d AT TIME ZONE 'UTC')::date", len(args))
	}
	if !end.IsZero() {
		args = append(args, end)
		q += fmt.Sprintf(" AND date < ($%d AT TIME ZONE 'UTC')::date", len(args))
	}
	q += " ORDER BY date"
	return q, args
}

func buildQuery(table, symbol string, begin, end time.Time) (string, []any) {
	args := []any{symbol}
	q := fmt.Sprintf(`SELECT timestamp, open, high, low, close FROM %s WHERE symbol = $1`, table)
	if !begin.IsZero() {
		args = append(args, begin)
		q += fmt.Sprintf(" AND timestamp >= $%d", len(args))
	}
	if !end.IsZero() {
		args = append(args, end)
		q += fmt.Sprintf(" AND timestamp < $%d", len(args))
	}
	q += " ORDER BY timestamp"
	return q, args
}

func (f *PostgresFeed) Symbol() string                    { return f.symbol }
func (f *PostgresFeed) DateRange() (time.Time, time.Time) { return f.rangeBegin, f.rangeEnd }

func (f *PostgresFeed) Next(ctx context.Context) (Tick, error) {
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
		f.ticks = f.expander(c.t, f.timeframe.Duration(), c.open, c.low, c.high, c.close)
		f.tickIdx = 0
	}
}

func (f *PostgresFeed) Close() error {
	f.candles = nil
	f.ticks = nil
	return nil
}
