// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
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

// GetOptionsProduct returns a metadata snapshot for the option contract
// identified by its OCC Symbol. It calls secdef/info to resolve the exact
// IBKR conid, which is stored in ContractID. Analogous to GetSpotProduct.
func (v *Exchange) GetOptionsProduct(ctx context.Context, contractID string) (*gobs.OptionContract, error) {
	symbol, right, expiry, strike, err := parseOCCContractID(contractID)
	if err != nil {
		return nil, err
	}

	underlyingConid, err := v.resolveConid(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("ibkr: GetOptionsProduct: could not resolve conid for %q: %w", symbol, err)
	}

	// Convert expiry date to the IBKR month code format (e.g. "DEC26").
	month := strings.ToUpper(expiry.Format("Jan06"))

	infoPath := fmt.Sprintf(
		"/v1/api/iserver/secdef/info?conid=%d&sectype=OPT&month=%s&right=%s&strike=%s&exchange=SMART",
		underlyingConid, month, right, strike.String(),
	)
	var entries []*apiSecDefInfoEntry
	if err := v.client.doRequest(ctx, "GET", infoPath, nil, &entries); err != nil {
		return nil, fmt.Errorf("ibkr: secdef/info for %q: %w", contractID, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("ibkr: no contract found for %q: %w", contractID, os.ErrNotExist)
	}
	e := entries[0]

	multiplier := decimal.NewFromInt(100)
	if e.Multiplier != "" {
		if m, err2 := decimal.NewFromString(e.Multiplier); err2 == nil {
			multiplier = m
		}
	}

	optType := "CALL"
	if right == "P" {
		optType = "PUT"
	}

	return &gobs.OptionContract{
		Symbol:     contractID,
		ContractID: strconv.Itoa(e.Conid),
		Underlying: symbol,
		OptionType:         optType,
		Strike:             strike,
		Expiry:             expiry,
		ContractSize:       multiplier,
	}, nil
}
