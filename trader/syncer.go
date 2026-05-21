package trader

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// TickEvent carries a tick timestamp and the IDs of any orders filled on that
// tick. OrderIDs is empty when the tick produced no fills.
type TickEvent struct {
	Time     time.Time
	OrderIDs []string
}

// Syncer tracks acknowledgments for two event types: ticks and order updates.
// todo counters are monotonically increasing; done counters catch up as events
// are processed. TickDone and OrderUpdateDone are idempotent: duplicate calls
// with the same key (tick timestamp or order ID) are ignored.
//
// Updates flow in either via Add* calls directly or via the channel using
// Notify and Recv.
type Syncer struct {
	mu   sync.Mutex
	cond sync.Cond

	todoTicks        int32
	todoOrderUpdates int32

	doneTicks        int32
	doneOrderUpdates int32

	seenTicks  map[time.Time]bool
	seenOrders map[string]bool

	tickCh chan TickEvent
}

func NewSyncer() *Syncer {
	s := &Syncer{
		seenTicks:  make(map[time.Time]bool),
		seenOrders: make(map[string]bool),
		tickCh:     make(chan TickEvent, 1),
	}
	s.cond.L = &s.mu
	return s
}

// Notify increments the todo counts and sends a TickEvent to the channel.
func (s *Syncer) Notify(t time.Time, orderIDs []string) {
	s.Add(1, len(orderIDs))
	s.tickCh <- TickEvent{Time: t, OrderIDs: orderIDs}
}

// Recv blocks until a TickEvent is available or ctx is cancelled.
func (s *Syncer) Recv(ctx context.Context) (TickEvent, error) {
	select {
	case <-ctx.Done():
		return TickEvent{}, context.Cause(ctx)
	case e := <-s.tickCh:
		return e, nil
	}
}

func (s *Syncer) TodoTicks() int32        { s.mu.Lock(); defer s.mu.Unlock(); return s.todoTicks }
func (s *Syncer) DoneTicks() int32        { s.mu.Lock(); defer s.mu.Unlock(); return s.doneTicks }
func (s *Syncer) TodoOrderUpdates() int32 { s.mu.Lock(); defer s.mu.Unlock(); return s.todoOrderUpdates }
func (s *Syncer) DoneOrderUpdates() int32 { s.mu.Lock(); defer s.mu.Unlock(); return s.doneOrderUpdates }

func (s *Syncer) Add(ticks, orderUpdates int) {
	s.mu.Lock()
	s.todoTicks += int32(ticks)
	s.todoOrderUpdates += int32(orderUpdates)
	s.mu.Unlock()
}

func (s *Syncer) TickDone(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seenTicks[t] {
		return
	}
	s.seenTicks[t] = true
	s.doneTicks++
	s.cond.Broadcast()
}

func (s *Syncer) OrderUpdateDone(orderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seenOrders[orderID] {
		return
	}
	s.seenOrders[orderID] = true
	s.doneOrderUpdates++
	s.cond.Broadcast()
}

// ClearEvent removes the seen entries for the given tick and order IDs, freeing
// memory once a tick cycle is fully processed.
func (s *Syncer) ClearEvent(t time.Time, orderIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seenTicks, t)
	for _, id := range orderIDs {
		delete(s.seenOrders, id)
	}
}

// Done acknowledges a tick and all its associated order updates in one atomic
// operation, acquiring the lock and broadcasting once.
func (s *Syncer) Done(t time.Time, orderIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.seenTicks[t] {
		s.seenTicks[t] = true
		s.doneTicks++
	}
	for _, id := range orderIDs {
		if !s.seenOrders[id] {
			s.seenOrders[id] = true
			s.doneOrderUpdates++
		}
	}
	s.cond.Broadcast()
}

// WaitSyncers blocks until all syncers have caught up (done >= todo for both
// ticks and order updates), or ctx is cancelled.
func WaitSyncers(ctx context.Context, vs []*Syncer) error {
	doneCh := make(chan bool)
	defer close(doneCh)

	var canceled atomic.Bool
	stopf := context.AfterFunc(ctx, func() {
		canceled.Store(true)
		for _, v := range vs {
			v.mu.Lock()
			v.cond.Broadcast()
			v.mu.Unlock()
		}
	})
	defer stopf()

	go func() {
		for _, v := range vs {
			v.mu.Lock()
			for v.doneTicks < v.todoTicks || v.doneOrderUpdates < v.todoOrderUpdates {
				slog.Debug("syncer: waiting", "todoTicks", v.todoTicks, "doneTicks", v.doneTicks, "todoOrderUpdates", v.todoOrderUpdates, "doneOrderUpdates", v.doneOrderUpdates)
				v.cond.Wait()
				if canceled.Load() {
					break
				}
			}
			v.mu.Unlock()
		}
		if !canceled.Load() {
			select {
			case <-ctx.Done():
			case doneCh <- true:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-doneCh:
		return nil
	}
}
