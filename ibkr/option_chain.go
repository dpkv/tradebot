// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// apiSecDefOPTResult is one entry returned by
// GET /v1/api/iserver/secdef/search?symbol=X&secType=OPT.
// Unlike apiSecDefResult (used for stocks), the OPT sections include Months
// and Exchange fields.
type apiSecDefOPTResult struct {
	ConID    string `json:"conid"`
	Symbol   string `json:"symbol"`
	Sections []struct {
		SecType  string `json:"secType"`
		Months   string `json:"months"`   // semicolon-separated, e.g. "APR25;MAY25;JUN25"
		Exchange string `json:"exchange"` // e.g. "SMART"
	} `json:"sections"`
}

// apiStrikesResponse is returned by
// GET /v1/api/iserver/secdef/strikes?conid=X&secType=OPT&month=X&exchange=X.
type apiStrikesResponse struct {
	Call []float64 `json:"call"`
	Put  []float64 `json:"put"`
}

// apiSecDefInfoEntry is one entry returned by
// GET /v1/api/iserver/secdef/info?conid=X&sectype=OPT&month=X&right=X&strike=X&exchange=X.
type apiSecDefInfoEntry struct {
	Conid        int     `json:"conid"`
	MaturityDate string  `json:"maturityDate"` // "YYYYMMDD"
	Strike       float64 `json:"strike"`
	Right        string  `json:"right"`      // "C" or "P"
	Multiplier   string  `json:"multiplier"` // typically "100"
}

// GetOptionChain returns all available option contracts for the given
// underlying symbol. It queries the CP Gateway in three stages:
//
//  1. secdef/search to discover available expiry months and the underlying conid.
//  2. secdef/strikes per month to get the call and put strike prices.
//  3. secdef/info for one representative strike per month to obtain the exact
//     maturity date (shared by all contracts expiring that month) and the
//     contract multiplier.
//
// The ContractID of each returned OptionContract is encoded as
// "{symbol}_{monthCode}_{C|P}_{strike}" so that GetOptionsProduct and
// OpenOptionsProduct can resolve the actual IBKR conid on demand without
// making N×strikes round trips here.
func (c *Client) GetOptionChain(ctx context.Context, symbol string) ([]*gobs.OptionContract, error) {
	// Stage 1: discover available months for this symbol's options.
	var searchResults []*apiSecDefOPTResult
	searchPath := "/v1/api/iserver/secdef/search?symbol=" + symbol + "&secType=OPT"
	if err := c.doRequest(ctx, "GET", searchPath, nil, &searchResults); err != nil {
		return nil, fmt.Errorf("ibkr: options search for %q: %w", symbol, err)
	}

	var underlyingConid string
	var months []string
	var exchange string
	for _, r := range searchResults {
		if r.Symbol != symbol {
			continue
		}
		for _, s := range r.Sections {
			if s.SecType == "OPT" && s.Months != "" {
				underlyingConid = r.ConID
				exchange = s.Exchange
				if exchange == "" {
					exchange = "SMART"
				}
				months = strings.Split(s.Months, ";")
				break
			}
		}
		if underlyingConid != "" {
			break
		}
	}
	if underlyingConid == "" {
		return nil, fmt.Errorf("ibkr: no options available for symbol %q", symbol)
	}

	var contracts []*gobs.OptionContract

	for _, month := range months {
		month = strings.TrimSpace(month)
		if month == "" {
			continue
		}

		// Stage 2: get the call and put strikes for this month.
		strikesPath := fmt.Sprintf(
			"/v1/api/iserver/secdef/strikes?conid=%s&secType=OPT&month=%s&exchange=%s",
			underlyingConid, month, exchange,
		)
		var strikes apiStrikesResponse
		if err := c.doRequest(ctx, "GET", strikesPath, nil, &strikes); err != nil {
			return nil, fmt.Errorf("ibkr: strikes for %s %s: %w", symbol, month, err)
		}

		// Stage 3: one secdef/info call to get the exact expiry date and multiplier
		// for this month. All contracts in the same expiry share these values.
		expiry, multiplier := c.resolveMonthExpiry(ctx, underlyingConid, month, exchange, strikes)

		for _, side := range []struct {
			right   string
			optType string
			strikes []float64
		}{
			{"C", "CALL", strikes.Call},
			{"P", "PUT", strikes.Put},
		} {
			for _, s := range side.strikes {
				strike := decimal.NewFromFloat(s)
				contracts = append(contracts, &gobs.OptionContract{
					Symbol:     occContractID(symbol, expiry, side.right, strike),
					Underlying: symbol,
					OptionType:   side.optType,
					Strike:       strike,
					Expiry:       expiry,
					ContractSize: multiplier,
				})
			}
		}
	}

	return contracts, nil
}

// resolveMonthExpiry calls secdef/info for one representative strike to obtain
// the exact maturity date and contract multiplier for the given expiry month.
// Returns zero values if the call fails — callers treat a zero Expiry as unknown.
func (c *Client) resolveMonthExpiry(ctx context.Context, underlyingConid, month, exchange string, strikes apiStrikesResponse) (time.Time, decimal.Decimal) {
	multiplier := decimal.NewFromInt(100) // standard US equity option default

	var strike float64
	var right string
	switch {
	case len(strikes.Call) > 0:
		strike = strikes.Call[0]
		right = "C"
	case len(strikes.Put) > 0:
		strike = strikes.Put[0]
		right = "P"
	default:
		return time.Time{}, multiplier
	}

	infoPath := fmt.Sprintf(
		"/v1/api/iserver/secdef/info?conid=%s&sectype=OPT&month=%s&right=%s&strike=%g&exchange=%s",
		underlyingConid, month, right, strike, exchange,
	)
	var entries []*apiSecDefInfoEntry
	if err := c.doRequest(ctx, "GET", infoPath, nil, &entries); err != nil || len(entries) == 0 {
		return time.Time{}, multiplier
	}
	e := entries[0]
	expiry, err := time.Parse("20060102", e.MaturityDate)
	if err != nil {
		return time.Time{}, multiplier
	}
	if e.Multiplier != "" {
		if m, err2 := decimal.NewFromString(e.Multiplier); err2 == nil {
			multiplier = m
		}
	}
	return expiry, multiplier
}
