// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"strings"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// The following nested types mirror the E*TRADE JSON responses for option
// expiry dates (GET /v1/market/optionexpiredate), option chains (GET
// /v1/market/optionchains), option quotes (GET /v1/market/quote with
// detailFlag=OPTIONS) and account transactions (GET
// /v1/accounts/{accountIdKey}/transactions). The flat OptionQuote and
// Transaction types below are derived from them.

// APIExpirationDate is one entry of OptionExpireDateResponse.
type APIExpirationDate struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`

	// Unused: "WEEKLY", "MONTHLY", "QUARTERLY", etc.
	ExpiryType string `json:"expiryType"`
}

// APIOptionExpireDateResponse is the body of OptionExpireDateResponse.
type APIOptionExpireDateResponse struct {
	ExpirationDate []*APIExpirationDate `json:"ExpirationDate"`
}

type apiOptionGreeks struct {
	Rho   decimal.Decimal `json:"rho"`
	Vega  decimal.Decimal `json:"vega"`
	Theta decimal.Decimal `json:"theta"`
	Delta decimal.Decimal `json:"delta"`
	Gamma decimal.Decimal `json:"gamma"`
	IV    decimal.Decimal `json:"iv"`

	// Unused: whether the greeks were computed from a current quote.
	CurrentValue bool `json:"currentValue"`
}

// apiOptionDetails is one side (Call or Put) of a chain's OptionPair. It
// carries no expiry date; the chain's SelectedED does.
type apiOptionDetails struct {
	OptionCategory string          `json:"optionCategory"` // "STANDARD", "ALL", "MINI"
	OptionType     string          `json:"optionType"`     // "CALL" or "PUT"
	Symbol         string          `json:"symbol"`         // underlying
	StrikePrice    decimal.Decimal `json:"strikePrice"`
	Bid            decimal.Decimal `json:"bid"`
	Ask            decimal.Decimal `json:"ask"`
	LastPrice      decimal.Decimal `json:"lastPrice"`
	Volume         decimal.Decimal `json:"volume"`
	OpenInterest   decimal.Decimal `json:"openInterest"`
	AdjustedFlag   bool            `json:"adjustedFlag"`
	TimeStamp      int64           `json:"timeStamp"` // Unix seconds
	OptionGreeks   apiOptionGreeks `json:"OptionGreeks"`

	// Unused: root symbol (differs from symbol for adjusted contracts),
	// human-readable symbol, OSI key and quote details URL.
	OptionRootSymbol string `json:"optionRootSymbol"`
	DisplaySymbol    string `json:"displaySymbol"`
	OsiKey           string `json:"osiKey"`
	QuoteDetail      string `json:"quoteDetail"`
	// Unused: sizes at the best bid and ask, change, and moneyness ("y"/"n").
	BidSize    int64           `json:"bidSize"`
	AskSize    int64           `json:"askSize"`
	NetChange  decimal.Decimal `json:"netChange"`
	InTheMoney string          `json:"inTheMoney"`
}

type apiOptionPair struct {
	Call *apiOptionDetails `json:"Call"`
	Put  *apiOptionDetails `json:"Put"`

	// Unused: "CALLPUT", "CALL" or "PUT", as requested.
	PairType string `json:"pairType"`
}

type apiSelectedED struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

// APIOptionChainResponse is the body of OptionChainResponse: every pair for
// one expiry date.
type APIOptionChainResponse struct {
	OptionPair []*apiOptionPair `json:"OptionPair"`
	SelectedED apiSelectedED    `json:"SelectedED"`

	// Unused: response time, quote type and the underlying's near price.
	TimeStamp int64           `json:"timeStamp"`
	QuoteType string          `json:"quoteType"`
	NearPrice decimal.Decimal `json:"nearPrice"`
}

// apiQuoteOption is the Option detail of a quote fetched with
// detailFlag=OPTIONS.
type apiQuoteOption struct {
	Bid              decimal.Decimal `json:"bid"`
	Ask              decimal.Decimal `json:"ask"`
	LastTrade        decimal.Decimal `json:"lastTrade"`
	OpenInterest     decimal.Decimal `json:"openInterest"`
	OptionMultiplier decimal.Decimal `json:"optionMultiplier"`
	OptionGreeks     apiOptionGreeks `json:"OptionGreeks"`

	// Unused: sizes, previous bid/ask, OSI key, value split and description.
	BidSize                int64           `json:"bidSize"`
	AskSize                int64           `json:"askSize"`
	OptionPreviousBidPrice decimal.Decimal `json:"optionPreviousBidPrice"`
	OptionPreviousAskPrice decimal.Decimal `json:"optionPreviousAskPrice"`
	OsiKey                 string          `json:"osiKey"`
	IntrinsicValue         decimal.Decimal `json:"intrinsicValue"`
	TimePremium            decimal.Decimal `json:"timePremium"`
	DaysToExpiration       int64           `json:"daysToExpiration"`
	SymbolDescription      string          `json:"symbolDescription"`
}

// OptionQuote is a flat snapshot of one option contract, from a chain or a
// quote. It names the contract by E*TRADE product fields; the caller turns
// those into a contract ID. It implements exchange.PriceUpdate.
type OptionQuote struct {
	Symbol  string // underlying
	CallPut string // "CALL" or "PUT"
	Year    int
	Month   int
	Day     int
	Strike  decimal.Decimal

	Category string // "STANDARD" for chain entries; empty for quotes
	Adjusted bool   // non-standard deliverable after a corporate action

	Bid          decimal.Decimal
	Ask          decimal.Decimal
	LastTrade    decimal.Decimal
	Volume       decimal.Decimal // zero for quotes, which don't carry it
	OpenInterest decimal.Decimal

	Delta decimal.Decimal
	Gamma decimal.Decimal
	Theta decimal.Decimal
	Vega  decimal.Decimal
	Rho   decimal.Decimal
	IV    decimal.Decimal

	DateTimeUTC int64 // Unix seconds
}

var _ exchange.PriceUpdate = &OptionQuote{}

func setGreeks(q *OptionQuote, g *apiOptionGreeks) {
	q.Delta, q.Gamma, q.Theta, q.Vega, q.Rho, q.IV = g.Delta, g.Gamma, g.Theta, g.Vega, g.Rho, g.IV
}

// NewOptionQuotesFromChain flattens a chain response into one OptionQuote
// per call and put, each dated by the chain's SelectedED.
func NewOptionQuotesFromChain(a *APIOptionChainResponse) []*OptionQuote {
	var quotes []*OptionQuote
	for _, pair := range a.OptionPair {
		for _, d := range []*apiOptionDetails{pair.Call, pair.Put} {
			if d == nil {
				continue
			}
			q := &OptionQuote{
				Symbol:       strings.ToUpper(d.Symbol),
				CallPut:      strings.ToUpper(d.OptionType),
				Year:         a.SelectedED.Year,
				Month:        a.SelectedED.Month,
				Day:          a.SelectedED.Day,
				Strike:       d.StrikePrice,
				Category:     strings.ToUpper(d.OptionCategory),
				Adjusted:     d.AdjustedFlag,
				Bid:          d.Bid,
				Ask:          d.Ask,
				LastTrade:    d.LastPrice,
				Volume:       d.Volume,
				OpenInterest: d.OpenInterest,
				DateTimeUTC:  d.TimeStamp,
			}
			setGreeks(q, &d.OptionGreeks)
			quotes = append(quotes, q)
		}
	}
	return quotes
}

// NewOptionQuoteFromAPI converts a quote fetched with detailFlag=OPTIONS.
// Returns nil if the quote isn't for an option.
func NewOptionQuoteFromAPI(a *APIQuoteData) *OptionQuote {
	if a.Option == nil || !strings.EqualFold(a.Product.SecurityType, "OPTN") {
		return nil
	}
	q := &OptionQuote{
		Symbol:       strings.ToUpper(a.Product.Symbol),
		CallPut:      strings.ToUpper(a.Product.CallPut),
		Year:         a.Product.ExpiryYear,
		Month:        a.Product.ExpiryMonth,
		Day:          a.Product.ExpiryDay,
		Strike:       a.Product.StrikePrice,
		Bid:          a.Option.Bid,
		Ask:          a.Option.Ask,
		LastTrade:    a.Option.LastTrade,
		OpenInterest: a.Option.OpenInterest,
		DateTimeUTC:  a.DateTimeUTC,
	}
	setGreeks(q, &a.Option.OptionGreeks)
	return q
}

// PricePoint returns the mid of the best bid and ask, or the last trade when
// either side is zero, like Quote.PricePoint.
func (q *OptionQuote) PricePoint() (decimal.Decimal, gobs.RemoteTime) {
	price := q.LastTrade
	if !q.Bid.IsZero() && !q.Ask.IsZero() {
		price = q.Bid.Add(q.Ask).Div(two)
	}
	return price, gobs.RemoteTime{Time: time.Unix(q.DateTimeUTC, 0)}
}

// Greeks returns the option sensitivities.
func (q *OptionQuote) Greeks() *exchange.Greeks {
	return &exchange.Greeks{Delta: q.Delta, Gamma: q.Gamma, Theta: q.Theta, Vega: q.Vega, Rho: q.Rho, IV: q.IV}
}

type apiTransactionProduct struct {
	Symbol       string          `json:"symbol"`
	SecurityType string          `json:"securityType"`
	CallPut      string          `json:"callPut"`
	ExpiryYear   int             `json:"expiryYear"`
	ExpiryMonth  int             `json:"expiryMonth"`
	ExpiryDay    int             `json:"expiryDay"`
	StrikePrice  decimal.Decimal `json:"strikePrice"`
}

type apiTransactionBrokerage struct {
	Product  apiTransactionProduct `json:"product"`
	Quantity decimal.Decimal       `json:"quantity"`
	Price    decimal.Decimal       `json:"price"`
	Fee      decimal.Decimal       `json:"fee"`

	// Unused: currencies, display symbol and settlement date.
	SettlementCurrency string `json:"settlementCurrency"`
	PaymentCurrency    string `json:"paymentCurrency"`
	DisplaySymbol      string `json:"displaySymbol"`
	SettlementDate     int64  `json:"settlementDate"`
}

// APITransaction is one entry of TransactionListResponse.
type APITransaction struct {
	TransactionID   int64                   `json:"transactionId"`
	AccountID       string                  `json:"accountId"`
	TransactionDate int64                   `json:"transactionDate"` // Unix milliseconds
	TransactionType string                  `json:"transactionType"`
	Description     string                  `json:"description"`
	Amount          decimal.Decimal         `json:"amount"`
	Brokerage       apiTransactionBrokerage `json:"brokerage"`

	// Unused: post date, memo, image flag and institution type.
	PostDate  int64  `json:"postDate"`
	Memo      string `json:"memo"`
	ImageFlag bool   `json:"imageFlag"`
	InstType  string `json:"instType"`
}

// APITransactionListResponse is the body of TransactionListResponse.
// Marker, when MoreTransactions is set, fetches the next page.
type APITransactionListResponse struct {
	Transaction      []*APITransaction `json:"Transaction"`
	Marker           string            `json:"marker"`
	MoreTransactions bool              `json:"moreTransactions"`

	// Unused: counts for this page and the whole range.
	TransactionCount int64 `json:"transactionCount"`
	TotalCount       int64 `json:"totalCount"`
}

// Transaction is a flat account transaction. The product fields are set for
// brokerage transactions; for an option they name the contract.
type Transaction struct {
	ID          int64
	AccountID   string
	DateMilli   int64
	Type        string // E*TRADE transaction type, e.g. "Bought", "Sold"
	Description string

	SecurityType string // "EQ", "OPTN", ...
	Symbol       string
	CallPut      string
	Year         int
	Month        int
	Day          int
	Strike       decimal.Decimal

	Quantity decimal.Decimal // signed as E*TRADE reports it
	Price    decimal.Decimal
	Fee      decimal.Decimal
}

// NewTransactionFromAPI converts an E*TRADE transaction into a flat one.
func NewTransactionFromAPI(a *APITransaction) *Transaction {
	p := &a.Brokerage.Product
	return &Transaction{
		ID:           a.TransactionID,
		AccountID:    a.AccountID,
		DateMilli:    a.TransactionDate,
		Type:         a.TransactionType,
		Description:  a.Description,
		SecurityType: strings.ToUpper(p.SecurityType),
		Symbol:       strings.ToUpper(p.Symbol),
		CallPut:      strings.ToUpper(p.CallPut),
		Year:         p.ExpiryYear,
		Month:        p.ExpiryMonth,
		Day:          p.ExpiryDay,
		Strike:       p.StrikePrice,
		Quantity:     a.Brokerage.Quantity,
		Price:        a.Brokerage.Price,
		Fee:          a.Brokerage.Fee,
	}
}
