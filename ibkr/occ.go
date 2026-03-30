// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// occContractID returns the compact OCC option symbol for the given contract,
// e.g. "AAPL261218C00200000". The suffix is always 15 chars: expiry as YYMMDD,
// right (C/P), strike × 1000 zero-padded to 8 digits.
func occContractID(symbol string, expiry time.Time, right string, strike decimal.Decimal) string {
	strikePennies := strike.Mul(decimal.NewFromInt(1000)).IntPart()
	return fmt.Sprintf("%s%s%s%08d", symbol, expiry.Format("060102"), right, strikePennies)
}

// parseOCCContractID parses a compact OCC option symbol back into its
// components. The suffix is always 15 chars, so the symbol is everything
// before that. Returns an error if the string is malformed.
func parseOCCContractID(contractID string) (symbol, right string, expiry time.Time, strike decimal.Decimal, err error) {
	const suffixLen = 15 // YYMMDD(6) + right(1) + strike(8)
	if len(contractID) <= suffixLen {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: too short", contractID)
	}
	split := len(contractID) - suffixLen
	symbol = contractID[:split]
	suffix := contractID[split:]
	expiry, err = time.Parse("060102", suffix[:6])
	if err != nil {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: bad expiry: %w", contractID, err)
	}
	right = string(suffix[6])
	if right != "C" && right != "P" {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: right must be C or P", contractID)
	}
	pennies, err := strconv.ParseInt(suffix[7:], 10, 64)
	if err != nil {
		return "", "", time.Time{}, decimal.Zero, fmt.Errorf("ibkr: invalid OCC symbol %q: bad strike: %w", contractID, err)
	}
	strike = decimal.NewFromInt(pennies).Div(decimal.NewFromInt(1000))
	return symbol, right, expiry, strike, nil
}
