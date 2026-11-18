// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bvk/tradebot/greelladder"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// etrade doesn't import greelladder; the ladder finds holdings by asserting
// rt.Account to this interface.
var _ greelladder.StockHoldings = &ExchangeAccount{}

func newTestAccount(t *testing.T, c *Client, db kv.Database) *ExchangeAccount {
	t.Helper()
	ex := &Exchange{db: db, client: c}
	t.Cleanup(func() { ex.Close() })
	return &ExchangeAccount{exchange: ex, account: c.creds.Load().Accounts[0]}
}

func TestGetOptionsChainContracts(t *testing.T) {
	var expiryCalls, chainCalls atomic.Int32
	c := newTestClient(t, optionChainHandler(t, &expiryCalls, &chainCalls, nil))
	ea := newTestAccount(t, c, newTestDB(t))

	chain, err := ea.GetOptionsChain(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 4 {
		t.Fatalf("got %d contracts, want 4", len(chain))
	}
	k := chain[1]
	want := time.Date(2026, 12, 18, 23, 59, 0, 0, newYork)
	if k.ContractID != "AAPL261218P00200000" || k.Underlying != "AAPL" || k.OptionType != "PUT" ||
		!k.Expiry.Equal(want) || !k.ContractSize.Equal(decimal.NewFromInt(100)) ||
		!k.Price.Equal(decimal.RequireFromString("1.2")) || !k.ImpliedVolatility.Equal(decimal.RequireFromString("0.25")) {
		t.Fatalf("contract = %+v", k)
	}
}

func TestGetOptionsProduct(t *testing.T) {
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	ea := newTestAccount(t, c, newTestDB(t))
	k, err := ea.GetOptionsProduct(context.Background(), testContractID)
	if err != nil {
		t.Fatal(err)
	}
	if k.ContractID != testContractID || !k.Price.Equal(decimal.RequireFromString("1.1")) {
		t.Fatalf("contract = %+v", k)
	}
}

func TestOpenOptionsProductShared(t *testing.T) {
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	ea := newTestAccount(t, c, newTestDB(t))
	ctx := context.Background()

	p1, err := ea.OpenOptionsProduct(ctx, testContractID)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ea.OpenOptionsProduct(ctx, testContractID)
	if err != nil {
		t.Fatal(err)
	}
	if p1.(*optionProductHandle).OptionProduct != p2.(*optionProductHandle).OptionProduct {
		t.Fatal("two opens of one contract in one account did not share the product")
	}

	// Closing one open, even twice, leaves the other working.
	p1.Close()
	p1.Close()
	r, err := p2.GetPriceUpdates()
	if err != nil {
		t.Fatalf("price updates after the other open closed: %v", err)
	}
	r.Close()
	if ea.exchange.getOpenOptionProduct(testAccountID, testContractID) == nil {
		t.Fatal("product closed while still open")
	}

	p2.Close()
	if ea.exchange.getOpenOptionProduct(testAccountID, testContractID) != nil || c.getOptionPriceTopic(testContractID) != nil {
		t.Fatal("product or price watch still open after every open closed")
	}
}

func TestGetOptionsOrderByClientIDAdopts(t *testing.T) {
	b := newFakeBroker(t)
	c := newTestClient(t, b.handler())
	db := newTestDB(t)
	ea := newTestAccount(t, c, db)
	ctx := context.Background()

	p, err := ea.OpenOptionsProduct(ctx, testContractID)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	// A placement whose server ID was never saved.
	now := time.Now().UnixMilli()
	uid := uuid.New()
	setOptionOrderEntry(ctx, db, uid, &optionOrderEntry{AccountID: testAccountID, ContractID: testContractID,
		Placement: orderPlacementInfo{RequestTimeMilli: now, Side: OptionSellOpen, Price: decimal.RequireFromString("1.1"),
			Qty: decimal.NewFromInt(1), PriceType: "LIMIT"}})
	b.add(&fakeOrder{id: 42, action: "SELL_OPEN", strike: 200, qty: "1", price: "1.1", placed: now, status: "OPEN"})

	d, err := ea.GetOptionsOrderByClientID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if d.ServerID() != "42" || d.ClientID() != uid {
		t.Fatalf("order = %s/%s", d.ServerID(), d.ClientID())
	}
	// The open product now restores the client ID on the order.
	got, err := p.Get(ctx, "42")
	if err != nil || got.ClientID() != uid {
		t.Fatalf("product Get = %v, %v; want client id %s", got, err, uid)
	}
}

type fakeMessenger struct {
	mu   sync.Mutex
	msgs []string
}

func (m *fakeMessenger) SendMessage(_ context.Context, _ time.Time, f string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, fmt.Sprintf(f, args...))
}

func settlementHandler(txs func() []any) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/accounts/"+testAccountIDKey+"/transactions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"TransactionListResponse": map[string]any{"Transaction": txs()}})
	})
	return mux
}

func settlementTx(id int64, typ string, strike, qty int, dayMilli int64) map[string]any {
	return map[string]any{"transactionId": id, "transactionDate": dayMilli, "transactionType": typ,
		"brokerage": map[string]any{"quantity": qty, "fee": 0.5, "product": map[string]any{
			"symbol": "AAPL", "securityType": "OPTN", "callPut": "PUT",
			"expiryYear": 2026, "expiryMonth": 12, "expiryDay": 18, "strikePrice": strike}}}
}

func TestGetOptionsSettlement(t *testing.T) {
	day := time.Date(2026, 12, 21, 0, 0, 0, 0, newYork).UnixMilli()
	for _, tc := range []struct {
		name      string
		txs       []any
		status    string
		key       string
		contracts int64
		alerts    int
	}{
		{"open", []any{settlementTx(1, "Sold Short", 200, -2, day)}, "open", "", 0, 0},
		{"assigned", []any{settlementTx(1, "Sold Short", 200, -2, day), settlementTx(2, "Option Assignment", 200, 2, day)}, "assigned", "2", 2, 0},
		{"expired", []any{settlementTx(3, "Option Expiration", 200, 2, day)}, "expired", "3", 2, 0},
		{"partly assigned", []any{settlementTx(4, "Expired", 200, 1, day), settlementTx(5, "Assigned", 200, 1, day+1)}, "assigned", "5", 1, 0},
		{"other contract", []any{settlementTx(6, "Option Assignment", 205, 2, day)}, "open", "", 0, 0},
		{"unknown type", []any{settlementTx(7, "Journal", 200, 2, day)}, "open", "", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, settlementHandler(func() []any { return tc.txs }))
			m := new(fakeMessenger)
			c.opts.Messenger = m
			ea := newTestAccount(t, c, newTestDB(t))

			for i := 0; i < 2; i++ {
				s, err := ea.GetOptionsSettlement(context.Background(), testContractID)
				if err != nil {
					t.Fatal(err)
				}
				if s.Status != tc.status || s.Key != tc.key || !s.Contracts.Equal(decimal.NewFromInt(tc.contracts)) {
					t.Fatalf("settlement = %+v", s)
				}
				if tc.status != "open" && !s.At.Equal(time.UnixMilli(day)) && !s.At.Equal(time.UnixMilli(day+1)) {
					t.Fatalf("settlement at = %v", s.At)
				}
			}
			// An unknown type alerts once, however often it is checked.
			if len(m.msgs) != tc.alerts {
				t.Fatalf("alerts = %q, want %d", m.msgs, tc.alerts)
			}
		})
	}
}

func TestGetStockHolding(t *testing.T) {
	base := "/v1/accounts/" + testAccountIDKey + "/portfolio"
	var fetches atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(base, func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		writeJSON(w, map[string]any{"PortfolioResponse": map[string]any{"AccountPortfolio": []any{map[string]any{
			"Position": []any{
				map[string]any{"positionId": 1, "quantity": 100, "lotsDetails": "http://x" + base + "/lots/1",
					"Product": map[string]any{"symbol": "AAPL", "securityType": "EQ"}},
				map[string]any{"positionId": 2, "quantity": 50, "lotsDetails": "http://x" + base + "/lots/2",
					"Product": map[string]any{"symbol": "AAPL", "securityType": "EQ"}},
				map[string]any{"positionId": 3, "quantity": -1, "lotsDetails": "http://x" + base + "/lots/3",
					"Product": map[string]any{"symbol": "AAPL", "securityType": "OPTN"}},
			},
		}}}})
	})
	mux.HandleFunc(base+"/lots/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"PositionLotsResponse": map[string]any{}})
	})
	c := newTestClient(t, mux)
	ea := newTestAccount(t, c, newTestDB(t))
	ctx := context.Background()

	// No snapshot yet: fetched once, then served from the snapshot.
	for i := 0; i < 2; i++ {
		n, err := ea.GetStockHolding(ctx, "aapl")
		if err != nil {
			t.Fatal(err)
		}
		if !n.Equal(decimal.NewFromInt(150)) {
			t.Fatalf("holding = %s, want 150 (the short put is not stock)", n)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("portfolio fetches = %d, want 1", n)
	}
	if n, err := ea.GetStockHolding(ctx, "MSFT"); err != nil || !n.IsZero() {
		t.Fatalf("MSFT holding = %s, %v; want zero", n, err)
	}
}
