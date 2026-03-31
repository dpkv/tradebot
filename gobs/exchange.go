// Copyright (c) 2023 BVK Chaitanya

package gobs

import (
	"time"

	"github.com/shopspring/decimal"
)

type RemoteTime struct {
	time.Time
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
	// Symbol is the OCC option symbol, e.g. "AAPL261218C00200000".
	Symbol string

	// ContractID is the exchange-native identifier for this contract.
	ContractID string

	Underlying string
	OptionType string // "CALL" or "PUT"

	Strike decimal.Decimal
	Expiry time.Time

	// ContractSize is the number of underlying shares per contract (typically 100).
	ContractSize decimal.Decimal

	// Snapshot pricing — populated by GetOptionChain / GetOptionsProduct.
	Price             decimal.Decimal
	Bid               decimal.Decimal
	Ask               decimal.Decimal
	Volume            decimal.Decimal
	OpenInterest      decimal.Decimal
	ImpliedVolatility decimal.Decimal
}
