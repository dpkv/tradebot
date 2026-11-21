// Copyright (c) 2023 BVK Chaitanya

package gobs

import (
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type RemoteTime struct {
	time.Time
}

type TaxLotID string

const (
	ETradeTaxLotIDPrefix = "https://api.etrade.com/v1/accounts/"
)

// Equal reports whether v and other name the same lot. For E*TRADE IDs it
// compares the account's lot path and the AccountID, Symbol, OrderNo and
// PositionLotID values, and ignores Qty, so parts of one lot are equal.
func (v TaxLotID) Equal(other TaxLotID) bool {
	if v == other {
		return true
	}
	if strings.HasPrefix(string(v), ETradeTaxLotIDPrefix) &&
		strings.HasPrefix(string(other), ETradeTaxLotIDPrefix) {
		a, err1 := url.Parse(string(v))
		b, err2 := url.Parse(string(other))
		if err1 == nil && err2 == nil {
			aq, bq := a.Query(), b.Query()
			if aq != nil && bq != nil {
				return a.Scheme == b.Scheme &&
					a.Host == b.Host && a.Path == b.Path &&
					aq.Get("AccountID") == bq.Get("AccountID") &&
					aq.Get("Symbol") == bq.Get("Symbol") &&
					aq.Get("OrderNo") == bq.Get("OrderNo") &&
					aq.Get("PositionLotID") == bq.Get("PositionLotID")
			}
		}
	}
	return false
}

// WithQty is v capped to qty shares, for a sell that takes only part of
// the lot. Only E*TRADE IDs carry a cap; others come back unchanged.
func (v TaxLotID) WithQty(qty decimal.Decimal) TaxLotID {
	if !strings.HasPrefix(string(v), ETradeTaxLotIDPrefix) {
		return v
	}
	u, err := url.Parse(string(v))
	if err != nil {
		return v
	}
	q := u.Query()
	q.Set("Qty", qty.String())
	u.RawQuery = q.Encode()
	return TaxLotID(u.String())
}

type Order struct {
	ServerOrderID string
	ClientOrderID string

	CreateTime RemoteTime
	FinishTime RemoteTime

	Side   string
	Status string

	FilledFee   decimal.Decimal
	FilledSize  decimal.Decimal
	FilledPrice decimal.Decimal

	Done       bool
	DoneReason string
}

type Candle struct {
	StartTime RemoteTime
	Duration  time.Duration

	Low  decimal.Decimal
	High decimal.Decimal

	Open  decimal.Decimal
	Close decimal.Decimal

	Volume decimal.Decimal
}

type Candles struct {
	Candles []*Candle
}

type Product struct {
	ProductID string
	Status    string

	Price decimal.Decimal

	BaseName          string
	BaseCurrencyID    string
	BaseDisplaySymbol string
	BaseMinSize       decimal.Decimal
	BaseMaxSize       decimal.Decimal
	BaseIncrement     decimal.Decimal

	QuoteName          string
	QuoteCurrencyID    string
	QuoteDisplaySymbol string
	QuoteMinSize       decimal.Decimal
	QuoteMaxSize       decimal.Decimal
	QuoteIncrement     decimal.Decimal
}

type Account struct {
	Timestamp time.Time

	Name       string
	CurrencyID string

	Available decimal.Decimal
	Hold      decimal.Decimal
}

type Accounts struct {
	Accounts []*Account
}

type OptionContract struct {
	// ContractID uniquely identifies the contract: the OSI option symbol
	// without its space padding, e.g. "AAPL261218C00200000".
	ContractID string

	Underlying string
	OptionType string // "CALL" or "PUT"

	Strike decimal.Decimal
	Expiry time.Time

	// ContractSize is the number of underlying shares per contract (typically 100).
	ContractSize decimal.Decimal

	// Snapshot pricing — populated by GetOptionsChain / GetOptionsProduct.
	Price          decimal.Decimal
	Bid            decimal.Decimal
	Ask            decimal.Decimal
	Volume         decimal.Decimal
	OpenInterest   decimal.Decimal
	ImpliedVolatility decimal.Decimal
}
