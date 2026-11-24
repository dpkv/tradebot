// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"strings"
	"testing"
	"time"
)

// TestMatchAssignedLot uses the first real assignment: a 167.5 NVDA put
// sold for 1.9 with a 0.52 fee, assigned on 2026-11-23, whose lot the
// portfolio showed at 165.6052 acquired at midnight New York time.
func TestMatchAssignedLot(t *testing.T) {
	assigned := &positionLot{Symbol: "NVDA", PositionLotID: 1232387065363, OrderNo: 743,
		OriginalQty: d("100"), RemainingQty: d("100"), Price: d("165.6052"), AcquiredDate: 1795410000000}
	at := time.UnixMilli(1795507338000) // the "Option Assigned" transaction
	basis := d("167.5").Sub(d("190").Sub(d("0.52")).Div(d("100")))
	if !basis.Equal(d("165.6052")) {
		t.Fatalf("basis = %s", basis)
	}

	buy := &positionLot{Symbol: "NVDA", PositionLotID: 1, OrderNo: 700, OriginalQty: d("25"), RemainingQty: d("25"), Price: d("166"), AcquiredDate: 1795410000000}
	other := &positionLot{Symbol: "AAPL", PositionLotID: 2, OrderNo: 701, OriginalQty: d("100"), RemainingQty: d("100"), Price: d("165.6052"), AcquiredDate: 1795410000000}
	old := &positionLot{Symbol: "NVDA", PositionLotID: 3, OrderNo: 600, OriginalQty: d("100"), RemainingQty: d("100"), Price: d("165.6052"), AcquiredDate: 1795410000000 - 10*86400000}
	sold := &positionLot{Symbol: "NVDA", PositionLotID: 4, OrderNo: 601, OriginalQty: d("100"), RemainingQty: d("0"), Price: d("165.6052"), AcquiredDate: 1795410000000}
	pricey := &positionLot{Symbol: "NVDA", PositionLotID: 5, OrderNo: 602, OriginalQty: d("100"), RemainingQty: d("100"), Price: d("167.5"), AcquiredDate: 1795410000000}

	l, err := matchAssignedLot([]*positionLot{buy, other, old, sold, pricey, assigned}, "NVDA", d("100"), at, basis)
	if err != nil || l != assigned {
		t.Fatalf("match = %+v, %v", l, err)
	}

	if _, err := matchAssignedLot([]*positionLot{buy, pricey}, "NVDA", d("100"), at, basis); err == nil || !strings.Contains(err.Error(), "no NVDA lot") {
		t.Errorf("no match: %v", err)
	}
	twin := *assigned
	twin.PositionLotID = 9
	if _, err := matchAssignedLot([]*positionLot{assigned, &twin}, "NVDA", d("100"), at, basis); err == nil || !strings.Contains(err.Error(), "2 NVDA lots") {
		t.Errorf("two matches: %v", err)
	}
	// A cent of rounding is allowed.
	if _, err := matchAssignedLot([]*positionLot{assigned}, "NVDA", d("100"), at, basis.Add(d("0.01"))); err != nil {
		t.Errorf("within tolerance: %v", err)
	}
}
