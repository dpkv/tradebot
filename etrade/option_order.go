// Copyright (c) 2026 Deepak Vankadaru

package etrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/bvk/tradebot/etrade/internal"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
)

// The client ID map for option orders (greel/etrade-options-story.md,
// scenario 5 and decision 7). E*TRADE's clientOrderId takes digits only and
// is missing from list and single-order responses, so the UUID to server
// order ID mapping is kept here, written before the order reaches the
// broker.

// optionOrderKeyPrefix is the KV directory of option order entries.
const optionOrderKeyPrefix = "/etrade/options/clientorder"

// optionOrderRetention is how long an entry is kept after its contract's
// expiry. Entries aren't deleted when the order finishes, because optlimiter
// looks up filled and canceled orders too.
const optionOrderRetention = 90 * 24 * time.Hour

// optionOrderMatchWindowMilli bounds the gap between our placement request
// and the broker's placed time when matching an order with no server ID.
const optionOrderMatchWindowMilli = 60_000

// optionOrderEntry is the value persisted for each option order, keyed by
// the client order UUID. Fields are exported for gob.
type optionOrderEntry struct {
	AccountID  string
	ContractID string

	// ServerOrderID is zero until the place call returns.
	ServerOrderID int64

	// Placement is the request, for matching when ServerOrderID is zero.
	// Placement.Side holds the E*TRADE order action, e.g. "SELL_OPEN".
	Placement orderPlacementInfo

	// Done is set once the order was seen in a terminal state, so opening the
	// contract again doesn't re-track it.
	Done bool
}

func optionOrderKey(uid uuid.UUID) string {
	return optionOrderKeyPrefix + "/" + uid.String()
}

func getOptionOrderEntry(ctx context.Context, db kv.Database, uid uuid.UUID) (*optionOrderEntry, error) {
	return kvutil.GetDB[optionOrderEntry](ctx, db, optionOrderKey(uid))
}

func setOptionOrderEntry(ctx context.Context, db kv.Database, uid uuid.UUID, e *optionOrderEntry) error {
	if err := kvutil.SetDB(ctx, db, optionOrderKey(uid), e); err != nil {
		return fmt.Errorf("etrade: could not persist option order entry %s: %w", uid, err)
	}
	return nil
}

// ascendOptionOrders calls fn for every option order entry.
func ascendOptionOrders(ctx context.Context, db kv.Database, fn func(uid uuid.UUID, e *optionOrderEntry) error) error {
	begin, end := kvutil.PathRange(optionOrderKeyPrefix)
	cb := func(ctx context.Context, _ kv.Reader, key string, e *optionOrderEntry) error {
		uid, err := uuid.Parse(strings.TrimPrefix(key, optionOrderKeyPrefix+"/"))
		if err != nil {
			return fmt.Errorf("etrade: invalid option order key %q: %w", key, err)
		}
		return fn(uid, e)
	}
	return kvutil.AscendDB(ctx, db, begin, end, cb)
}

// lookupOptionOrder finds the option order the account placed with the given
// client ID, in any status. Returns os.ErrNotExist when the account has no
// entry for it, or when its placement outcome is unknown and no broker order
// matches it (yet).
func lookupOptionOrder(ctx context.Context, db kv.Database, client *Client, accountID string, uid uuid.UUID) (*internal.Order, error) {
	entry, err := getOptionOrderEntry(ctx, db, uid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("etrade: no option order with client id %s: %w", uid, os.ErrNotExist)
		}
		return nil, err
	}
	// A lookup through one account never returns another account's order.
	if entry.AccountID != accountID {
		return nil, fmt.Errorf("etrade: no option order with client id %s in the account: %w", uid, os.ErrNotExist)
	}

	if entry.ServerOrderID != 0 {
		order, err := client.GetOrder(ctx, accountID, entry.ServerOrderID)
		if err != nil {
			return nil, err
		}
		order.ClientUUID = uid
		return order, nil
	}

	k, err := parseContractID(entry.ContractID)
	if err != nil {
		return nil, err
	}

	// Server order IDs other entries already own can't be this order.
	claimed := make(map[int64]bool)
	if err := ascendOptionOrders(ctx, db, func(other uuid.UUID, e *optionOrderEntry) error {
		if other != uid && e.AccountID == accountID && e.ServerOrderID != 0 {
			claimed[e.ServerOrderID] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}

	orders, err := client.ListOrders(ctx, accountID, &ListOrdersFilter{
		Symbol:       k.Root,
		SecurityType: "OPTN",
		FromDate:     time.UnixMilli(entry.Placement.RequestTimeMilli),
	})
	if err != nil {
		return nil, err
	}
	var matches []*internal.Order
	for _, o := range orders {
		if !claimed[o.OrderID] && matchOptionOrder(o, accountID, entry) {
			matches = append(matches, o)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("etrade: option order with client id %s not found at the broker: %w", uid, os.ErrNotExist)
	case 1:
	default:
		ids := make([]int64, 0, len(matches))
		for _, o := range matches {
			ids = append(ids, o.OrderID)
		}
		return nil, fmt.Errorf("etrade: option order with client id %s matches %d broker orders %v; refusing to guess", uid, len(matches), ids)
	}

	order := matches[0]
	entry.ServerOrderID = order.OrderID
	entry.Done = order.IsDone()
	if err := setOptionOrderEntry(ctx, db, uid, entry); err != nil {
		return nil, err
	}
	slog.Warn("etrade: recovered option order with unknown server id", "account-id", accountID, "contract", entry.ContractID, "client-id", uid, "order-id", order.OrderID)
	order.ClientUUID = uid
	return order, nil
}

// matchOptionOrder reports whether a broker order matches an entry's
// placement: same account and contract, action, quantity and limit price,
// placed within the match window of our request.
func matchOptionOrder(o *internal.Order, accountID string, entry *optionOrderEntry) bool {
	if o.AccountID != accountID {
		return false
	}
	if key, err := orderTopicKey(o); err != nil || key != entry.ContractID {
		return false
	}
	p := &entry.Placement
	if !strings.EqualFold(o.Side, p.Side) || !strings.EqualFold(o.PriceType, p.PriceType) {
		return false
	}
	if !o.OrderedQty.Equal(p.Qty) || !o.LimitPrice.Equal(p.Price) {
		return false
	}
	return absInt64(o.PlacedTimeMilli-p.RequestTimeMilli) <= optionOrderMatchWindowMilli
}

// pruneOptionOrders deletes entries whose contract expired more than
// optionOrderRetention before now. Returns the number deleted.
func pruneOptionOrders(ctx context.Context, db kv.Database, now time.Time) (int, error) {
	var stale []string
	if err := ascendOptionOrders(ctx, db, func(uid uuid.UUID, e *optionOrderEntry) error {
		k, err := parseContractID(e.ContractID)
		if err != nil {
			slog.Warn("etrade: option order entry has an invalid contract id (kept)", "client-id", uid, "contract", e.ContractID, "err", err)
			return nil
		}
		if now.Sub(k.Expiry()) > optionOrderRetention {
			stale = append(stale, optionOrderKey(uid))
		}
		return nil
	}); err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}
	err := kv.WithReadWriter(ctx, db, func(ctx context.Context, rw kv.ReadWriter) error {
		for _, key := range stale {
			if err := rw.Delete(ctx, key); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("etrade: could not prune option order entries: %w", err)
	}
	return len(stale), nil
}
