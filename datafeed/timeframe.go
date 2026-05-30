package datafeed

import (
	"fmt"
	"strings"
	"time"
)

// Timeframe represents a candle interval.
type Timeframe int

const (
	TimeframeMinute Timeframe = iota
	TimeframeHourly
	TimeframeDaily
)

func (tf Timeframe) Duration() time.Duration {
	switch tf {
	case TimeframeMinute:
		return time.Minute
	case TimeframeHourly:
		return time.Hour
	case TimeframeDaily:
		return 24 * time.Hour
	}
	return 0
}

func (tf Timeframe) String() string {
	switch tf {
	case TimeframeMinute:
		return "minute"
	case TimeframeHourly:
		return "hourly"
	case TimeframeDaily:
		return "daily"
	}
	return "unknown"
}

func ParseTimeframe(s string) (Timeframe, error) {
	switch strings.ToLower(s) {
	case "minute":
		return TimeframeMinute, nil
	case "hourly":
		return TimeframeHourly, nil
	case "daily":
		return TimeframeDaily, nil
	}
	return 0, fmt.Errorf("unknown timeframe %q: must be minute, hourly, or daily", s)
}
