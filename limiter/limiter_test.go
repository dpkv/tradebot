// Copyright (c) 2026 Deepak Vankadaru

package limiter

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/point"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// newTestLimiter is a buy of n shares at 100.
func newTestLimiter(t *testing.T, n int) *Limiter {
	t.Helper()
	l, err := New(uuid.NewString(), "fake", "AAPL", &point.Point{Size: decimal.NewFromInt(int64(n)), Price: d("100"), Cancel: d("105")})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// addOrder stores a new live buy order in l and returns a copy of it.
func addOrder(t *testing.T, l *Limiter, id string) exchange.SimpleOrder {
	t.Helper()
	o, err := exchange.NewSimpleOrder(id, uuid.New(), "BUY")
	if err != nil {
		t.Fatal(err)
	}
	o.CreateTime = gobs.RemoteTime{Time: time.Now()}
	l.orderMap.Store(id, o)
	return *o
}

// readUntil reads l the way a greeler does from its own goroutine, until
// done is closed. The race detector flags an order changed in place.
func readUntil(l *Limiter, done <-chan struct{}) {
	for {
		l.FilledSize()
		l.GetSummary(nil)
		l.Actions()
		select {
		case <-done:
			return
		default:
		}
	}
}

// TestUpdateOrderMapCopiesOrders: Run applies order updates while other
// goroutines read the limiter's fills. Run it with -race.
func TestUpdateOrderMapCopiesOrders(t *testing.T) {
	const n = 200
	l := newTestLimiter(t, n)
	o := addOrder(t, l, "order-0")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= n; i++ {
			update := o
			update.FilledSize = decimal.NewFromInt(int64(i))
			update.FilledPrice = d("100")
			update.Done = i == n
			if _, err := l.updateOrderMap(&update); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	readUntil(l, done)

	if got := l.FilledSize(); !got.Equal(decimal.NewFromInt(n)) {
		t.Errorf("FilledSize = %s, want %d", got, n)
	}
	if got, ok := l.orderMap.Load("order-0"); !ok || !got.Done {
		t.Errorf("order-0 = %+v, want done", got)
	}
}

// notFoundProduct is a product that has forgotten every order.
type notFoundProduct struct {
	exchange.Product // unused methods panic
}

func (p *notFoundProduct) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
	return nil, os.ErrNotExist
}

// TestFetchOrderMapCopiesNotFoundOrders: a live order the exchange no
// longer knows is marked done while other goroutines read the limiter.
func TestFetchOrderMapCopiesNotFoundOrders(t *testing.T) {
	const n = 200
	l := newTestLimiter(t, 1)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			addOrder(t, l, fmt.Sprintf("order-%d", i))
			if _, err := l.fetchOrderMap(ctx, &notFoundProduct{}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	readUntil(l, done)

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("order-%d", i)
		if o, ok := l.orderMap.Load(id); !ok || !o.Done || o.DoneReason != "NOTFOUND/CANCELED" {
			t.Fatalf("%s = %+v, want done as not found", id, o)
		}
	}
}

// finishedExchange reports every order finished at one time.
type finishedExchange struct {
	exchange.Exchange // unused methods panic
	at                time.Time
}

func (e *finishedExchange) GetOrder(ctx context.Context, productID, serverID string) (exchange.OrderDetail, error) {
	return &exchange.SimpleOrder{ServerOrderID: serverID, Done: true, FinishTime: gobs.RemoteTime{Time: e.at}}, nil
}

// TestUpdateActiveLimiterCopiesOrders: the background job fills in finish
// times while other goroutines read the limiter.
func TestUpdateActiveLimiterCopiesOrders(t *testing.T) {
	const n = 200
	l := newTestLimiter(t, n)
	ex := &finishedExchange{at: time.Now()}
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("order-%d", i)
			o := addOrder(t, l, id)
			o.FilledSize, o.FilledPrice, o.Done = d("1"), d("100"), true
			l.orderMap.Store(id, &o)
			if err := updateActiveLimiter(ctx, ex, l); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	readUntil(l, done)

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("order-%d", i)
		if o, ok := l.orderMap.Load(id); !ok || !o.FinishTime.Time.Equal(ex.at) {
			t.Fatalf("%s = %+v, want finished at %s", id, o, ex.at)
		}
	}
}
