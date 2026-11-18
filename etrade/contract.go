// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // expiries are in New York time on any host

	"github.com/shopspring/decimal"
)

// contract identifies one listed option contract. Its ContractID is the OSI
// option symbol in compact form, without the space padding OSI uses to fill
// the root to six characters, e.g. "AAPL261218P00200000". E*TRADE never sees
// that ID: requests name the option by its product fields, and quotes take
// the colon form "AAPL:2026:12:18:PUT:200".
type contract struct {
	Root    string // underlying symbol, e.g. "AAPL"
	CallPut string // "CALL" or "PUT"
	Year    int
	Month   int
	Day     int
	Strike  decimal.Decimal
}

// osiSuffixLen is the fixed-width tail of an OSI symbol: YYMMDD, C|P and the
// strike times 1000 in eight digits.
const osiSuffixLen = 6 + 1 + 8

var thousand = decimal.NewFromInt(1000)

// maxStrike is the largest strike the eight OSI strike digits can hold.
var maxStrike = decimal.NewFromInt(99999999).Div(thousand)

// parseContractID parses a compact OSI symbol. It parses from the end, so a
// root ending in a digit (an adjusted contract such as "AAPL1") is still
// unambiguous. The space-padded OSI form is rejected rather than trimmed, so
// one contract has exactly one ID.
func parseContractID(id string) (*contract, error) {
	if len(id) <= osiSuffixLen {
		return nil, fmt.Errorf("etrade: option contract id %q is too short: %w", id, os.ErrInvalid)
	}
	root, suffix := id[:len(id)-osiSuffixLen], id[len(id)-osiSuffixLen:]
	for _, r := range root {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '.' {
			return nil, fmt.Errorf("etrade: option contract id %q has an invalid root %q: %w", id, root, os.ErrInvalid)
		}
	}
	if !isDigits(suffix[:6]) || !isDigits(suffix[7:]) {
		return nil, fmt.Errorf("etrade: option contract id %q has an invalid date or strike: %w", id, os.ErrInvalid)
	}
	yy, _ := strconv.Atoi(suffix[0:2])
	mm, _ := strconv.Atoi(suffix[2:4])
	dd, _ := strconv.Atoi(suffix[4:6])
	year := 2000 + yy
	if t := time.Date(year, time.Month(mm), dd, 0, 0, 0, 0, time.UTC); t.Year() != year || int(t.Month()) != mm || t.Day() != dd {
		return nil, fmt.Errorf("etrade: option contract id %q has an invalid date: %w", id, os.ErrInvalid)
	}
	var callPut string
	switch suffix[6] {
	case 'C':
		callPut = "CALL"
	case 'P':
		callPut = "PUT"
	default:
		return nil, fmt.Errorf("etrade: option contract id %q is neither a call nor a put: %w", id, os.ErrInvalid)
	}
	milli, _ := strconv.ParseInt(suffix[7:], 10, 64)
	if milli == 0 {
		return nil, fmt.Errorf("etrade: option contract id %q has a zero strike: %w", id, os.ErrInvalid)
	}
	return &contract{
		Root:    root,
		CallPut: callPut,
		Year:    year,
		Month:   mm,
		Day:     dd,
		Strike:  decimal.New(milli, -3),
	}, nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// newContract builds a contract from E*TRADE product fields, as they appear
// in chain, order, portfolio and transaction responses. callPut is matched
// case-insensitively.
func newContract(symbol, callPut string, year, month, day int, strike decimal.Decimal) (*contract, error) {
	c := &contract{
		Root:    strings.ToUpper(symbol),
		CallPut: strings.ToUpper(callPut),
		Year:    year,
		Month:   month,
		Day:     day,
		Strike:  strike,
	}
	// Checked before ID, which reads the first letter of CallPut.
	if c.CallPut != "CALL" && c.CallPut != "PUT" {
		return nil, fmt.Errorf("etrade: invalid option type %q: %w", callPut, os.ErrInvalid)
	}
	if _, err := parseContractID(c.ID()); err != nil {
		return nil, fmt.Errorf("etrade: invalid option product %s %s %04d-%02d-%02d %s: %w", symbol, callPut, year, month, day, strike, err)
	}
	if !strike.Mul(thousand).IsInteger() || !strike.IsPositive() || strike.GreaterThan(maxStrike) {
		return nil, fmt.Errorf("etrade: invalid option strike %s: %w", strike, os.ErrInvalid)
	}
	return c, nil
}

// ID returns the compact OSI symbol, the gobs.OptionContract.ContractID.
func (c *contract) ID() string {
	return fmt.Sprintf("%s%02d%02d%02d%c%08d", c.Root, c.Year%100, c.Month, c.Day, c.CallPut[0], c.Strike.Mul(thousand).IntPart())
}

// QuoteSymbol returns the colon form the quote endpoint takes, e.g.
// "AAPL:2026:12:18:PUT:200".
func (c *contract) QuoteSymbol() string {
	return fmt.Sprintf("%s:%04d:%02d:%02d:%s:%s", c.Root, c.Year, c.Month, c.Day, c.CallPut, c.Strike.String())
}

// Expiry returns 23:59 America/New_York on the expiry date. A contract trades
// until the close on that day and can settle later in the evening, so it
// counts as live until the day ends.
func (c *contract) Expiry() time.Time {
	return time.Date(c.Year, time.Month(c.Month), c.Day, 23, 59, 0, 0, newYork)
}

// newYork is the exchange time zone for E*TRADE option expiries.
var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(fmt.Sprintf("etrade: could not load the America/New_York time zone: %v", err))
	}
	return loc
}()

// ContractQuoteSymbol returns the quote-endpoint symbol for a contract ID,
// e.g. "AAPL:2026:12:18:PUT:200" for "AAPL261218P00200000".
func ContractQuoteSymbol(contractID string) (string, error) {
	k, err := parseContractID(contractID)
	if err != nil {
		return "", err
	}
	return k.QuoteSymbol(), nil
}
