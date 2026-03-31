// Copyright (c) 2026 Deepak Vankadaru

package ibkr

import (
	"context"
	"encoding/json"
	"fmt"
	"path"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/ibkr/internal"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
)

const Keyspace = "/ibkr/"

type Datastore struct {
	db kv.Database
}

func NewDatastore(db kv.Database) *Datastore {
	return &Datastore{db: db}
}

// SaveOrder persists an order to the datastore, keyed by ClientOrderID.
// Overwrites any previously saved order with the same ClientOrderID.
func (ds *Datastore) SaveOrder(ctx context.Context, order *internal.Order) error {
	js, err := json.Marshal(order)
	if err != nil {
		return fmt.Errorf("ibkr: could not marshal order %s: %w", order.ClientOrderID, err)
	}
	value := &gobs.IBKROrder{
		ClientOrderID: order.ClientOrderID,
		Order:         json.RawMessage(js),
	}
	key := path.Join(Keyspace, "orders", order.ClientOrderID)
	return kvutil.SetDB(ctx, ds.db, key, value)
}

// LoadOrders loads all persisted orders from the datastore.
func (ds *Datastore) LoadOrders(ctx context.Context) ([]*internal.Order, error) {
	begin, end := kvutil.PathRange(path.Join(Keyspace, "orders"))
	var orders []*internal.Order
	fn := func(ctx context.Context, r kv.Reader, key string, value *gobs.IBKROrder) error {
		order := new(internal.Order)
		if err := json.Unmarshal([]byte(value.Order), order); err != nil {
			return fmt.Errorf("ibkr: could not unmarshal order at key %s: %w", key, err)
		}
		orders = append(orders, order)
		return nil
	}
	if err := kvutil.AscendDB(ctx, ds.db, begin, end, fn); err != nil {
		return nil, err
	}
	return orders, nil
}
