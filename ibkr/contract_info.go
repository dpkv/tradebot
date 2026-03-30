// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// apiContractInfoResponse is the JSON structure returned by
// GET /v1/api/iserver/contract/{conid}/info.
type apiContractInfoResponse struct {
	Symbol       string `json:"symbol"`
	LocalSymbol  string `json:"local_symbol"`  // e.g. "AAPL  261218C00200000" — space-padded OCC
	InstrumentType string `json:"instrument_type"`
	Right        string `json:"right"`         // "CALL" or "PUT"
	Strike       string `json:"strike"`        // returned as a JSON string
	MaturityDate string `json:"maturity_date"` // "YYYYMMDD"
	Multiplier   string `json:"multiplier"`
}

func (r *apiContractInfoResponse) right() (string, error) {
	switch strings.ToUpper(r.Right) {
	case "C", "CALL":
		return "C", nil
	case "P", "PUT":
		return "P", nil
	default:
		return "", fmt.Errorf("ibkr: unexpected right value %q", r.Right)
	}
}

// GetOptionContractInfo fetches full contract details for the given option
// conid and returns them as a *gobs.OptionContract with Symbol (OCC format),
// Underlying, OptionType, Strike, Expiry, and ContractSize populated.
func (c *Client) GetOptionContractInfo(ctx context.Context, conid int) (*gobs.OptionContract, error) {
	path := fmt.Sprintf("/v1/api/iserver/contract/%d/info", conid)
	var info apiContractInfoResponse
	if err := c.doRequest(ctx, "GET", path, nil, &info); err != nil {
		return nil, fmt.Errorf("ibkr: contract info for conid %d: %w", conid, err)
	}
	right, err := info.right()
	if err != nil {
		return nil, fmt.Errorf("ibkr: conid %d: %w", conid, err)
	}

	if info.MaturityDate == "" {
		return nil, fmt.Errorf("ibkr: conid %d: no maturity_date in contract info response", conid)
	}
	expiry, err := time.Parse("20060102", info.MaturityDate)
	if err != nil {
		return nil, fmt.Errorf("ibkr: conid %d: unparseable maturity_date %q: %w", conid, info.MaturityDate, err)
	}

	if info.Symbol == "" {
		return nil, fmt.Errorf("ibkr: conid %d: no symbol in contract info response", conid)
	}

	strike, err := decimal.NewFromString(info.Strike)
	if err != nil {
		return nil, fmt.Errorf("ibkr: conid %d: unparseable strike %q: %w", conid, info.Strike, err)
	}

	multiplier := decimal.NewFromInt(100)
	if info.Multiplier != "" {
		if m, err2 := decimal.NewFromString(info.Multiplier); err2 == nil {
			multiplier = m
		}
	}

	optType := "CALL"
	if right == "P" {
		optType = "PUT"
	}

	// local_symbol is space-padded OCC (e.g. "AAPL  261218C00200000").
	// Strip spaces to get the compact OCC format we use throughout.
	occ := strings.ReplaceAll(info.LocalSymbol, " ", "")
	if occ == "" {
		occ = occContractID(info.Symbol, expiry, right, strike)
	}

	return &gobs.OptionContract{
		Symbol:       occ,
		ContractID:   strconv.Itoa(conid),
		Underlying:   info.Symbol,
		OptionType:   optType,
		Strike:       strike,
		Expiry:       expiry,
		ContractSize: multiplier,
	}, nil
}
