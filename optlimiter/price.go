// Copyright (c) 2026 Deepak Vankadaru

package optlimiter

import (
	"fmt"
	"time"
	_ "time/tzdata" // the regular session is defined in New York time

	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

var (
	pennyTick  = decimal.NewFromFloat(0.01)
	nickelTick = decimal.NewFromFloat(0.05)
	three      = decimal.NewFromInt(3)
	two        = decimal.NewFromInt(2)
)

// tickSize returns the minimum price increment for an option premium, using
// the penny-program convention: $0.01 below $3, $0.05 at or above.
func tickSize(price decimal.Decimal) decimal.Decimal {
	if price.LessThan(three) {
		return pennyTick
	}
	return nickelTick
}

// ceilTick rounds a sell price up to the tick, so rounding never sells
// below the price it was asked for.
func (v *OptLimiter) ceilTick(price decimal.Decimal) decimal.Decimal {
	tick := v.tickSize(price)
	return price.Div(tick).Ceil().Mul(tick)
}

// nextPrice returns the limit price for the k-th order of a session (k
// counts from zero). last is the previous order's price this session, or
// zero for the first one.
//
//	price = max(mid − k × step × spread, bid, MinPremium)
//
// rounded up to the tick and at least one tick below last, but never below
// the bid or the floor. When it can't go lower it equals the bid or floor
// and the caller leaves the resting order alone.
func (v *OptLimiter) nextPrice(quote *gobs.OptionContract, k int, last decimal.Decimal) (decimal.Decimal, error) {
	bid, ask := quote.Bid, quote.Ask
	if bid.IsNegative() || !ask.IsPositive() || ask.LessThan(bid) {
		return decimal.Zero, fmt.Errorf("invalid quote for %s: bid %s ask %s", v.contractID, bid, ask)
	}
	mid := bid.Add(ask).Div(two)
	spread := ask.Sub(bid)

	lowest := v.ceilTick(decimal.Max(bid, v.minPremium))

	price := mid.Sub(spread.Mul(v.repriceStep).Mul(decimal.NewFromInt(int64(k))))
	price = v.ceilTick(price)
	if last.IsPositive() {
		if below := last.Sub(v.tickSize(last)); price.GreaterThan(below) {
			price = below
		}
	}
	if price.LessThan(lowest) {
		price = lowest
	}
	return price, nil
}

var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

// RegularSession reports whether t is inside the US options regular
// session (9:30–16:00 New York time, Monday to Friday) and when that next
// changes: the close if open, else the next open. Exchange holidays aren't
// known here; on one, broker calls fail and Run returns the error.
func RegularSession(t time.Time) (bool, time.Time) {
	nt := t.In(newYork)
	y, m, d := nt.Date()
	open := time.Date(y, m, d, 9, 30, 0, 0, newYork)
	close := time.Date(y, m, d, 16, 0, 0, 0, newYork)
	if isWeekday(nt.Weekday()) && !nt.Before(open) && nt.Before(close) {
		return true, close
	}
	next := open
	if !nt.Before(open) {
		next = time.Date(y, m, d+1, 9, 30, 0, 0, newYork)
	}
	for !isWeekday(next.Weekday()) {
		y, m, d = next.Date()
		next = time.Date(y, m, d+1, 9, 30, 0, 0, newYork)
	}
	return false, next
}

func isWeekday(d time.Weekday) bool {
	return d != time.Saturday && d != time.Sunday
}
