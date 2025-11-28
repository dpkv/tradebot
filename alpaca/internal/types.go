// Copyright (c) 2025 Deepak Vankadaru

package internal

import (
	alpacaclient "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Order wraps the Alpaca Order to implement exchange interfaces
type Order struct {
	*alpacaclient.Order
}

var _ exchange.Order = &Order{}
var _ exchange.OrderUpdate = &Order{}
var _ exchange.OrderDetail = &Order{}

func (v *Order) ServerID() string {
	return v.ID
}

func (v *Order) ClientID() uuid.UUID {
	if v.ClientOrderID == "" {
		return uuid.Nil
	}
	// Try to parse as UUID, if it fails return Nil
	cuuid, err := uuid.Parse(v.ClientOrderID)
	if err != nil {
		return uuid.Nil
	}
	return cuuid
}

func (v *Order) OrderSide() string {
	return string(v.Side)
}

func (v *Order) CreatedAt() gobs.RemoteTime {
	return gobs.RemoteTime{Time: v.Order.CreatedAt}
}

func (v *Order) UpdatedAt() gobs.RemoteTime {
	return gobs.RemoteTime{Time: v.Order.UpdatedAt}
}

func (v *Order) FinishedAt() gobs.RemoteTime {
	// Check various finish times
	if v.FilledAt != nil {
		return gobs.RemoteTime{Time: *v.FilledAt}
	}
	if v.CanceledAt != nil {
		return gobs.RemoteTime{Time: *v.CanceledAt}
	}
	if v.ExpiredAt != nil {
		return gobs.RemoteTime{Time: *v.ExpiredAt}
	}
	if v.FailedAt != nil {
		return gobs.RemoteTime{Time: *v.FailedAt}
	}
	return gobs.RemoteTime{}
}

func (v *Order) ExecutedFee() decimal.Decimal {
	// Alpaca doesn't provide fee in the Order struct directly
	// This would need to be fetched from account activities
	// For now, return zero
	// TODO: Implement fee fetching from account activities
	return decimal.Zero
}

func (v *Order) ExecutedSize() decimal.Decimal {
	return v.FilledQty
}

func (v *Order) ExecutedValue() decimal.Decimal {
	if v.FilledAvgPrice == nil {
		return decimal.Zero
	}
	return v.FilledQty.Mul(*v.FilledAvgPrice)
}

func (v *Order) IsDone() bool {
	// Alpaca order statuses: new, accepted, pending_new, pending_replace, pending_cancel,
	// filled, partially_filled, canceled, expired, rejected, pending_cancel, replaced
	status := v.OrderStatus()
	return status == "filled" || status == "canceled" || status == "expired" || status == "rejected" || status == "replaced"
}

func (v *Order) OrderStatus() string {
	return v.Status
}
