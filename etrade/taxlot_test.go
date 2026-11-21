// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"fmt"
	"testing"

	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

const testLotURL = "https://api.etrade.com/v1/accounts/KEY/portfolio/42"

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestParseTaxLotRef(t *testing.T) {
	for _, tc := range []struct {
		id                   string
		orderNo, positionLot int64
		qty                  string
		bad                  bool
	}{
		{id: "?AccountID=1&OrderNo=7&Symbol=AAPL", orderNo: 7, qty: "0"},
		{id: "?AccountID=1&PositionLotID=99&Symbol=AAPL", positionLot: 99, qty: "0"},
		{id: "?AccountID=1&PositionLotID=99&Qty=25&Symbol=AAPL", positionLot: 99, qty: "25"},
		{id: "?AccountID=1&OrderNo=7&Qty=10.5&Symbol=AAPL", orderNo: 7, qty: "10.5"},
		{id: "?AccountID=1&Symbol=AAPL", bad: true},
		{id: "?AccountID=1&OrderNo=0&Symbol=AAPL", bad: true},
		{id: "?AccountID=1&PositionLotID=0&Symbol=AAPL", bad: true},
		{id: "?AccountID=1&OrderNo=7&Qty=0&Symbol=AAPL", bad: true},
		{id: "?AccountID=1&OrderNo=7&Qty=x&Symbol=AAPL", bad: true},
	} {
		r, err := parseTaxLotRef(gobs.TaxLotID(testLotURL + tc.id))
		if tc.bad {
			if err == nil {
				t.Errorf("%s: no error", tc.id)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.id, err)
			continue
		}
		if r.accountID != "1" || r.symbol != "AAPL" || r.lotDetailURL != testLotURL ||
			r.orderNo != tc.orderNo || r.positionLotID != tc.positionLot || !r.qty.Equal(d(tc.qty)) {
			t.Errorf("%s: got %+v", tc.id, r)
		}
	}
	if _, err := parseTaxLotRef("https://example.com/x?OrderNo=1"); err == nil {
		t.Error("no error for a foreign prefix")
	}
}

func TestTaxLotRefMatches(t *testing.T) {
	byOrder := &taxLotRef{orderNo: 7}
	byLot := &taxLotRef{positionLotID: 99}
	a := &positionLot{PositionLotID: 99, OrderNo: 0}
	b := &positionLot{PositionLotID: 98, OrderNo: 7}
	if byOrder.matches(a) || !byOrder.matches(b) || !byLot.matches(a) || byLot.matches(b) {
		t.Error("wrong matches")
	}
}

func group(qty string, remaining ...string) *taxLotGroup {
	g := &taxLotGroup{ref: &taxLotRef{qty: d(qty)}}
	for i, r := range remaining {
		g.lots = append(g.lots, &positionLot{PositionLotID: int64(len(remaining)*10 + i), RemainingQty: d(r)})
	}
	return g
}

func drawsString(plots []*placeOrderLot) string {
	var s string
	for _, p := range plots {
		s += fmt.Sprintf("%d:%s ", p.ID, p.Size)
	}
	return s
}

func TestPlanLotDraws(t *testing.T) {
	for _, tc := range []struct {
		what   string
		groups []*taxLotGroup
		size   string
		sum    string
		draws  string
	}{
		{"whole lots", []*taxLotGroup{group("0", "10", "15")}, "25", "25", "20:10 21:15 "},
		// Without a Qty, nothing is trimmed: the caller sees the mismatch.
		{"whole lots too big", []*taxLotGroup{group("0", "10", "15")}, "20", "25", "20:10 21:15 "},
		{"lots short", []*taxLotGroup{group("0", "10")}, "25", "10", "10:10 "},
		{"part of a lot", []*taxLotGroup{group("25", "100")}, "25", "25", "10:25 "},
		{"part of a partly sold lot", []*taxLotGroup{group("25", "10")}, "25", "10", "10:10 "},
		// A partly filled sell of 15 left names its 25-share part.
		{"trimmed", []*taxLotGroup{group("25", "90")}, "15", "15", "10:15 "},
		{"cap across lots", []*taxLotGroup{group("30", "20", "20")}, "30", "30", "20:20 21:10 "},
		{"trim drops a draw", []*taxLotGroup{group("0", "5"), group("10", "10")}, "5", "5", "10:5 "},
		{"empty lot skipped", []*taxLotGroup{group("0", "0", "25")}, "25", "25", "21:25 "},
	} {
		plots, sum := planLotDraws(tc.groups, d(tc.size))
		if !sum.Equal(d(tc.sum)) || drawsString(plots) != tc.draws {
			t.Errorf("%s: draws %q sum %s, want %q %s", tc.what, drawsString(plots), sum, tc.draws, tc.sum)
		}
	}
}

func TestUpdatePortfolioKeepsLotsWithoutOrders(t *testing.T) {
	c := &Client{}
	c.updatePortfolio([]*positionLot{
		{LotsDetails: testLotURL, AccountID: "1", Symbol: "AAPL", PositionLotID: 1, OrderNo: 7, RemainingQty: d("25")},
		{LotsDetails: testLotURL, AccountID: "1", Symbol: "AAPL", PositionLotID: 2, OrderNo: 0, RemainingQty: d("100")},
	})
	lots, ok := c.lotDetailMap.Load(testLotURL)
	if !ok || len(lots) != 2 {
		t.Fatalf("cached lots = %v", lots)
	}
	if _, err := c.resolveLotsFromCache("1", "AAPL", []int64{7}); err != nil {
		t.Errorf("order 7 doesn't resolve: %v", err)
	}
}
