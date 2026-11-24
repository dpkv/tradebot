// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

// What a put assignment's lot looks like in the portfolio, as seen on the
// first real one (greel/assignment-lots-story.md scenario 1): one lot of
// the assigned shares, acquired at midnight New York time on the day of the
// assignment, at the strike less the premium kept net of fees per share,
// and with E*TRADE's assignment order as its orderNo.
const (
	// assignedLotDays is how many days around the assignment's New York
	// date the lot's acquired date may be.
	assignedLotDays = 3
)

// assignedLotPriceTolerance is how far the lot's per-share cost may be
// from the expected basis, to absorb rounding.
var assignedLotPriceTolerance = decimal.NewFromFloat(0.01)

// newYorkDay is t's date in New York, as a day count.
func newYorkDay(t time.Time) int {
	y, m, d := t.In(newYork).Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// matchAssignedLot picks the one lot of symbol among lots that matches an
// assignment of shares at at with per-share cost basis.
func matchAssignedLot(lots []*positionLot, symbol string, shares decimal.Decimal, at time.Time, basis decimal.Decimal) (*positionLot, error) {
	day := newYorkDay(at)
	var found []*positionLot
	for _, l := range lots {
		if l.Symbol != symbol || !l.OriginalQty.Equal(shares) || !l.RemainingQty.IsPositive() {
			continue
		}
		if d := newYorkDay(time.UnixMilli(l.AcquiredDate)) - day; d < -assignedLotDays || d > assignedLotDays {
			continue
		}
		if l.Price.Sub(basis).Abs().GreaterThan(assignedLotPriceTolerance) {
			continue
		}
		found = append(found, l)
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("etrade: no %s lot of %s shares acquired near %s at %s per share", symbol, shares, at.In(newYork).Format(time.DateOnly), basis)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, l := range found {
		ids[i] = fmt.Sprint(l.PositionLotID)
	}
	return nil, fmt.Errorf("etrade: %d %s lots match an assignment of %s shares near %s at %s per share (lots %s)", len(found), symbol, shares, at.In(newYork).Format(time.DateOnly), basis, strings.Join(ids, ", "))
}

// FindAssignedLot refreshes the account's portfolio and names the lot a
// put assignment of shares at at, with per-share cost basis, brought in.
func (p *Product) FindAssignedLot(ctx context.Context, accountID string, shares decimal.Decimal, at time.Time, basis decimal.Decimal) (gobs.TaxLotID, error) {
	_, lots, err := p.client.fetchPortfolio(ctx, accountID)
	if err != nil {
		return "", err
	}
	p.client.updatePortfolio(lots)
	lot, err := matchAssignedLot(lots, p.symbol, shares, at, basis)
	if err != nil {
		return "", err
	}
	return p.client.makePositionTaxLotID(lot.AccountID, lot.Symbol, lot.LotsDetails, lot.PositionLotID)
}

var (
	_ exchange.AssignedLotFinder = &AccountProduct{}
	_ exchange.TaxLotChecker     = &AccountProduct{}
)
