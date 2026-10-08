// Copyright (c) 2026 Deepak Vankadaru

package optlimiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/idgen"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const DefaultKeyspace = "/optlimiters/"

var (
	// DefaultRepriceStep is the fraction of the bid-ask spread each re-price
	// moves the price down when the caller passes zero.
	DefaultRepriceStep = decimal.NewFromFloat(0.2)

	// DefaultRepriceInterval is how long an order rests before it is re-priced
	// when the caller passes zero.
	DefaultRepriceInterval = 2 * time.Minute
)

// pollInterval is how often a canceled order is re-fetched until the broker
// confirms it is done.
const pollInterval = time.Second

// cancelTimeout bounds how long a cancel waits for the broker to confirm
// the order is done, so an outage can't hang Run.
const cancelTimeout = 2 * time.Minute

// absentSettle is how long a client ID must stay missing at the broker
// before it counts as never placed. A broker can be slow to list an order it
// accepted, so recovery looks an ID up again on each start until a lookup
// made absentSettle after the first miss still misses it.
const absentSettle = 10 * time.Minute

// ErrUnconfirmed is what a stopped Run returns, in place of the stop cause,
// while a placement that failed in this process isn't yet confirmed absent
// at the broker: its order may still be listed and live.
var ErrUnconfirmed = errors.New("a failed sell-to-open placement is not yet confirmed absent")

// absence is what recovery knows about a client ID the broker didn't list.
type absence struct {
	since   time.Time // the first miss, or the failed placement
	failed  bool      // its placement failed in this process
	settled bool      // missed again absentSettle after since; not looked up again
}

// OptLimiter is one sell-to-open order intent: it places a broker order
// and re-prices it toward the market until filled. Component, not a job —
// its owner (optpos) runs it.
type OptLimiter struct {
	// runMu serializes Run.
	runMu sync.Mutex

	// mu guards orders. Run and the owner's Save may run concurrently.
	mu sync.Mutex

	uid          string
	exchangeName string
	contractID   string
	contractSize decimal.Decimal
	numContracts decimal.Decimal
	minPremium   decimal.Decimal // per-share floor

	repriceStep     decimal.Decimal // fraction of the spread per step
	repriceInterval time.Duration

	idgen  *idgen.Generator                 // offset saved ahead of each order
	orders map[string]*exchange.SimpleOrder // server order ID -> order

	// absent holds the client IDs below the offset that the broker hasn't
	// listed; in memory only. Only Run uses it, under runMu.
	absent map[uuid.UUID]*absence

	// Overridable in tests.
	now           func() time.Time
	session       func(time.Time) (open bool, next time.Time)
	tickSize      func(price decimal.Decimal) decimal.Decimal
	pollInterval  time.Duration
	cancelTimeout time.Duration
}

// New creates a sell-to-open order for numContracts of contractID at a
// per-share premium no lower than minPremium. Zero repriceStep or
// repriceInterval select the defaults.
func New(uid, exchangeName, contractID string, contractSize, numContracts, minPremium, repriceStep decimal.Decimal, repriceInterval time.Duration) (*OptLimiter, error) {
	if repriceStep.IsZero() {
		repriceStep = DefaultRepriceStep
	}
	if repriceInterval == 0 {
		repriceInterval = DefaultRepriceInterval
	}
	v := &OptLimiter{
		uid:             uid,
		exchangeName:    exchangeName,
		contractID:      contractID,
		contractSize:    contractSize,
		numContracts:    numContracts,
		minPremium:      minPremium,
		repriceStep:     repriceStep,
		repriceInterval: repriceInterval,
		idgen:           idgen.New(uid, 0),
		orders:          make(map[string]*exchange.SimpleOrder),
		absent:          make(map[uuid.UUID]*absence),
	}
	v.setDefaults()
	if err := v.check(); err != nil {
		return nil, err
	}
	return v, nil
}

func (v *OptLimiter) setDefaults() {
	v.now = time.Now
	v.session = RegularSession
	v.tickSize = tickSize
	v.pollInterval = pollInterval
	v.cancelTimeout = cancelTimeout
}

func (v *OptLimiter) check() error {
	if len(v.uid) == 0 {
		return fmt.Errorf("optlimiter uid is empty")
	}
	if len(v.exchangeName) == 0 {
		return fmt.Errorf("optlimiter exchange name is empty")
	}
	if len(v.contractID) == 0 {
		return fmt.Errorf("optlimiter contract id is empty")
	}
	if !v.contractSize.IsPositive() {
		return fmt.Errorf("optlimiter contract size must be positive")
	}
	if !v.numContracts.IsPositive() || !v.numContracts.Equal(v.numContracts.Truncate(0)) {
		return fmt.Errorf("optlimiter number of contracts must be a positive integer")
	}
	if !v.minPremium.IsPositive() {
		return fmt.Errorf("optlimiter min premium must be positive")
	}
	if !v.repriceStep.IsPositive() || v.repriceStep.GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("optlimiter reprice step must be in (0, 1]")
	}
	if v.repriceInterval <= 0 {
		return fmt.Errorf("optlimiter reprice interval must be positive")
	}
	return nil
}

// SetSession replaces the regular-session calendar Run trades in; for tests.
// Call it before Run.
func (v *OptLimiter) SetSession(session func(time.Time) (open bool, next time.Time)) {
	v.session = session
}

// SetClock replaces the clock Run reads; for tests. Call it before Run.
func (v *OptLimiter) SetClock(now func() time.Time) {
	v.now = now
}

func (v *OptLimiter) String() string {
	return "optlimiter:" + v.uid
}

func (v *OptLimiter) LogValue() slog.Value {
	return slog.StringValue(v.uid)
}

func (v *OptLimiter) UID() string                   { return v.uid }
func (v *OptLimiter) ExchangeName() string          { return v.exchangeName }
func (v *OptLimiter) ContractID() string            { return v.contractID }
func (v *OptLimiter) ContractSize() decimal.Decimal { return v.contractSize }
func (v *OptLimiter) NumContracts() decimal.Decimal { return v.numContracts }
func (v *OptLimiter) MinPremium() decimal.Decimal   { return v.minPremium }

// FilledSize returns the number of contracts sold so far.
func (v *OptLimiter) FilledSize() decimal.Decimal {
	v.mu.Lock()
	defer v.mu.Unlock()

	var filled decimal.Decimal
	for _, order := range v.orders {
		filled = filled.Add(order.FilledSize)
	}
	return filled
}

// FilledValue returns the premium collected so far, per share: the sum of
// contracts times per-share fill price. Multiply by ContractSize for
// dollars.
func (v *OptLimiter) FilledValue() decimal.Decimal {
	v.mu.Lock()
	defer v.mu.Unlock()

	var value decimal.Decimal
	for _, order := range v.orders {
		value = value.Add(order.ExecutedValue())
	}
	return value
}

// FilledFee returns the fees charged so far.
func (v *OptLimiter) FilledFee() decimal.Decimal {
	v.mu.Lock()
	defer v.mu.Unlock()

	var fee decimal.Decimal
	for _, order := range v.orders {
		fee = fee.Add(order.Fee)
	}
	return fee
}

// PendingSize returns the number of contracts still to sell.
func (v *OptLimiter) PendingSize() decimal.Decimal {
	pending := v.numContracts.Sub(v.FilledSize())
	if pending.IsNegative() {
		return decimal.Zero
	}
	return pending
}

// IsDone returns true when every contract is sold.
func (v *OptLimiter) IsDone() bool {
	return v.PendingSize().IsZero()
}

// Orders returns every broker order placed so far, oldest first.
func (v *OptLimiter) Orders() []*gobs.Order {
	v.mu.Lock()
	defer v.mu.Unlock()

	orders := make([]*gobs.Order, 0, len(v.orders))
	for _, order := range v.orders {
		orders = append(orders, toGobOrder(order))
	}
	// An order has no create time until its first update arrives, and only
	// the newest order can lack one, so those go last.
	sort.Slice(orders, func(i, j int) bool {
		a, b := orders[i].CreateTime.Time, orders[j].CreateTime.Time
		if a.IsZero() != b.IsZero() {
			return b.IsZero()
		}
		if a.Equal(b) {
			return orders[i].ServerOrderID < orders[j].ServerOrderID
		}
		return a.Before(b)
	})
	return orders
}

func toGobOrder(order *exchange.SimpleOrder) *gobs.Order {
	return &gobs.Order{
		ServerOrderID: order.ServerOrderID,
		ClientOrderID: order.ClientUUID.String(),
		CreateTime:    gobs.RemoteTime{Time: order.CreateTime.Time},
		FinishTime:    gobs.RemoteTime{Time: order.FinishTime.Time},
		Side:          order.Side,
		Status:        order.Status,
		FilledFee:     order.Fee,
		FilledSize:    order.FilledSize,
		FilledPrice:   order.FilledPrice,
		Done:          order.Done,
		DoneReason:    order.DoneReason,
	}
}

func fromGobOrder(order *gobs.Order) (*exchange.SimpleOrder, error) {
	s, err := exchange.NewSimpleOrderFromGobOrder(order)
	if err != nil {
		return nil, err
	}
	s.DoneReason = order.DoneReason
	return s, nil
}

func (v *OptLimiter) Save(ctx context.Context, rw kv.ReadWriter) error {
	v.mu.Lock()
	gv := &gobs.OptLimiterState{
		V1: &gobs.OptLimiterStateV1{
			Config: &gobs.OptLimiterConfig{
				ExchangeName:    v.exchangeName,
				ContractID:      v.contractID,
				ContractSize:    v.contractSize,
				NumContracts:    v.numContracts,
				MinPremium:      v.minPremium,
				RepriceStep:     v.repriceStep,
				RepriceInterval: v.repriceInterval,
				ClientIDSeed:    v.idgen.Seed(),
			},
			Progress: &gobs.OptLimiterProgress{
				ClientIDOffset: v.idgen.Offset(),
				Orders:         make(map[string]*gobs.Order, len(v.orders)),
			},
		},
	}
	for id, order := range v.orders {
		gv.V1.Progress.Orders[id] = toGobOrder(order)
	}
	v.mu.Unlock()

	// Run and the owner may save concurrently, so a snapshot can be older
	// than what is stored. Reading the record lets the database reject a
	// conflicting write, and the offset never goes back: recovery looks up
	// every client ID below it.
	key := path.Join(DefaultKeyspace, v.uid)
	old, err := kvutil.Get[gobs.OptLimiterState](ctx, rw, key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not read optlimiter state: %w", err)
	}
	if old != nil && old.V1 != nil && old.V1.Progress != nil && old.V1.Progress.ClientIDOffset > gv.V1.Progress.ClientIDOffset {
		gv.V1.Progress.ClientIDOffset = old.V1.Progress.ClientIDOffset
	}
	if err := kvutil.Set(ctx, rw, key, gv); err != nil {
		return fmt.Errorf("could not save optlimiter state: %w", err)
	}
	return nil
}

func Load(ctx context.Context, uid string, r kv.Reader) (*OptLimiter, error) {
	if len(uid) == 0 {
		return nil, fmt.Errorf("optlimiter uid is empty")
	}
	key := path.Join(DefaultKeyspace, uid)
	gv, err := kvutil.Get[gobs.OptLimiterState](ctx, r, key)
	if err != nil {
		return nil, fmt.Errorf("could not load optlimiter state: %w", err)
	}
	if gv.V1 == nil || gv.V1.Config == nil || gv.V1.Progress == nil {
		return nil, fmt.Errorf("optlimiter state at %q is incomplete", key)
	}
	config, progress := gv.V1.Config, gv.V1.Progress
	seed := uid
	if len(config.ClientIDSeed) > 0 {
		seed = config.ClientIDSeed
	}
	v := &OptLimiter{
		uid:             uid,
		exchangeName:    config.ExchangeName,
		contractID:      config.ContractID,
		contractSize:    config.ContractSize,
		numContracts:    config.NumContracts,
		minPremium:      config.MinPremium,
		repriceStep:     config.RepriceStep,
		repriceInterval: config.RepriceInterval,
		// The saved offset is written before each order is placed, so it
		// already covers every client ID that may be at the broker.
		idgen:  idgen.New(seed, progress.ClientIDOffset),
		orders: make(map[string]*exchange.SimpleOrder, len(progress.Orders)),
		absent: make(map[uuid.UUID]*absence),
	}
	for id, gorder := range progress.Orders {
		order, err := fromGobOrder(gorder)
		if err != nil {
			return nil, fmt.Errorf("could not decode optlimiter order %q: %w", id, err)
		}
		v.orders[id] = order
	}
	v.setDefaults()
	if err := v.check(); err != nil {
		return nil, err
	}
	return v, nil
}
