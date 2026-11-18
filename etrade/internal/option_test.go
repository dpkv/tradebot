// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// The fixtures are shaped like the E*TRADE API documentation's examples,
// trimmed to the fields in use.

const chainJSON = `{"OptionChainResponse": {
  "timeStamp": 1763424000, "quoteType": "DELAYED", "nearPrice": 200.0,
  "OptionPair": [{
    "Call": {"optionCategory": "STANDARD", "optionRootSymbol": "AAPL", "optionType": "CALL",
      "symbol": "AAPL", "strikePrice": 200.0, "bid": 5.1, "ask": 5.3, "lastPrice": 5.2,
      "volume": 120, "openInterest": 3400, "adjustedFlag": false, "timeStamp": 1763424000,
      "osiKey": "AAPL--261218C00200000",
      "OptionGreeks": {"rho": 0.1, "vega": 0.2, "theta": -0.03, "delta": 0.52, "gamma": 0.04, "iv": 0.27, "currentValue": true}},
    "Put": {"optionCategory": "STANDARD", "optionRootSymbol": "AAPL", "optionType": "PUT",
      "symbol": "AAPL", "strikePrice": 200.0, "bid": 4.8, "ask": 5.0, "lastPrice": 4.9,
      "volume": 90, "openInterest": 2100, "adjustedFlag": false, "timeStamp": 1763424000,
      "OptionGreeks": {"delta": -0.48, "iv": 0.28}}
  }, {
    "Put": {"optionCategory": "STANDARD", "optionRootSymbol": "AAPL1", "optionType": "PUT",
      "symbol": "AAPL", "strikePrice": 195.0, "bid": 1.0, "ask": 1.2, "adjustedFlag": true}
  }],
  "SelectedED": {"month": 12, "year": 2026, "day": 18}
}}`

func TestNewOptionQuotesFromChain(t *testing.T) {
	var wrapper struct {
		OptionChainResponse APIOptionChainResponse
	}
	if err := json.Unmarshal([]byte(chainJSON), &wrapper); err != nil {
		t.Fatal(err)
	}
	quotes := NewOptionQuotesFromChain(&wrapper.OptionChainResponse)
	if len(quotes) != 3 {
		t.Fatalf("got %d quotes, want 3", len(quotes))
	}
	c := quotes[0]
	if c.Symbol != "AAPL" || c.CallPut != "CALL" || c.Year != 2026 || c.Month != 12 || c.Day != 18 || !c.Strike.Equal(d("200")) {
		t.Errorf("call contract = %+v", c)
	}
	if !c.Bid.Equal(d("5.1")) || !c.Ask.Equal(d("5.3")) || !c.Volume.Equal(d("120")) || !c.OpenInterest.Equal(d("3400")) || !c.IV.Equal(d("0.27")) || !c.Delta.Equal(d("0.52")) {
		t.Errorf("call pricing = %+v", c)
	}
	if price, _ := c.PricePoint(); !price.Equal(d("5.2")) {
		t.Errorf("call PricePoint = %s, want the mid 5.2", price)
	}
	if p := quotes[1]; p.CallPut != "PUT" || !p.Delta.Equal(d("-0.48")) || p.Adjusted {
		t.Errorf("put = %+v", p)
	}
	if a := quotes[2]; !a.Adjusted || a.Category != "STANDARD" {
		t.Errorf("adjusted put = %+v", a)
	}
}

const optionQuoteJSON = `{"QuoteResponse": {"QuoteData": [{
  "dateTimeUTC": 1763424000, "quoteStatus": "REALTIME",
  "Product": {"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
    "expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": 200},
  "Option": {"ask": 5.0, "bid": 4.8, "lastTrade": 0, "openInterest": 2100,
    "optionMultiplier": 100, "osiKey": "AAPL--261218P00200000",
    "OptionGreeks": {"rho": -0.1, "vega": 0.2, "theta": -0.03, "delta": -0.48, "gamma": 0.04, "iv": 0.28}}
}, {
  "dateTimeUTC": 1763424000,
  "Product": {"symbol": "AAPL", "securityType": "EQ"},
  "All": {"bid": 199.9, "ask": 200.1, "lastTrade": 200.0}
}]}}`

func TestNewOptionQuoteFromAPI(t *testing.T) {
	var wrapper struct {
		QuoteResponse struct {
			QuoteData []*APIQuoteData
		}
	}
	if err := json.Unmarshal([]byte(optionQuoteJSON), &wrapper); err != nil {
		t.Fatal(err)
	}
	q := NewOptionQuoteFromAPI(wrapper.QuoteResponse.QuoteData[0])
	if q == nil {
		t.Fatal("option quote is nil")
	}
	if q.Symbol != "AAPL" || q.CallPut != "PUT" || q.Day != 18 || !q.Strike.Equal(d("200")) || !q.Bid.Equal(d("4.8")) || !q.OpenInterest.Equal(d("2100")) {
		t.Errorf("option quote = %+v", q)
	}
	if g := q.Greeks(); !g.Delta.Equal(d("-0.48")) || !g.IV.Equal(d("0.28")) {
		t.Errorf("greeks = %+v", g)
	}
	if price, _ := q.PricePoint(); !price.Equal(d("4.9")) {
		t.Errorf("PricePoint = %s, want the mid 4.9", price)
	}
	if q := NewOptionQuoteFromAPI(wrapper.QuoteResponse.QuoteData[1]); q != nil {
		t.Errorf("equity quote converted to an option quote: %+v", q)
	}
}

const transactionsJSON = `{"TransactionListResponse": {
  "Transaction": [{
    "transactionId": 26112100001, "accountId": "12345678", "transactionDate": 1766120400000,
    "amount": 0, "description": "OPTION ASSIGNMENT PUT AAPL 12/18/26 200",
    "transactionType": "Option Assignment",
    "brokerage": {"product": {"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
      "expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": 200},
      "quantity": 1, "price": 0, "fee": 0.15}
  }, {
    "transactionId": 26112100002, "accountId": "12345678", "transactionDate": 1766120400000,
    "amount": -20000, "description": "BOUGHT AAPL ASSIGNED", "transactionType": "Bought",
    "brokerage": {"product": {"symbol": "AAPL", "securityType": "EQ"}, "quantity": 100, "price": 200, "fee": 0}
  }],
  "marker": "abc", "moreTransactions": true, "transactionCount": 2, "totalCount": 5
}}`

func TestNewTransactionFromAPI(t *testing.T) {
	var wrapper struct {
		TransactionListResponse APITransactionListResponse
	}
	if err := json.Unmarshal([]byte(transactionsJSON), &wrapper); err != nil {
		t.Fatal(err)
	}
	resp := wrapper.TransactionListResponse
	if !resp.MoreTransactions || resp.Marker != "abc" || len(resp.Transaction) != 2 {
		t.Fatalf("response = %+v", resp)
	}
	tx := NewTransactionFromAPI(resp.Transaction[0])
	if tx.ID != 26112100001 || tx.Type != "Option Assignment" || tx.SecurityType != "OPTN" || tx.CallPut != "PUT" || tx.Year != 2026 || !tx.Strike.Equal(d("200")) || !tx.Quantity.Equal(d("1")) || !tx.Fee.Equal(d("0.15")) {
		t.Errorf("option transaction = %+v", tx)
	}
	if eq := NewTransactionFromAPI(resp.Transaction[1]); eq.SecurityType != "EQ" || eq.CallPut != "" || !eq.Quantity.Equal(d("100")) {
		t.Errorf("equity transaction = %+v", eq)
	}
}

func TestExpireDates(t *testing.T) {
	data := `{"OptionExpireDateResponse": {"ExpirationDate": [
	  {"year": 2026, "month": 11, "day": 20, "expiryType": "MONTHLY"},
	  {"year": 2026, "month": 11, "day": 27, "expiryType": "WEEKLY"}]}}`
	var wrapper struct {
		OptionExpireDateResponse APIOptionExpireDateResponse
	}
	if err := json.Unmarshal([]byte(data), &wrapper); err != nil {
		t.Fatal(err)
	}
	dates := wrapper.OptionExpireDateResponse.ExpirationDate
	if len(dates) != 2 || dates[1].Day != 27 || dates[1].ExpiryType != "WEEKLY" {
		t.Errorf("dates = %+v", dates)
	}
}
