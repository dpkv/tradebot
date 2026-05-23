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

// Syncer coordinates tick and order-update acknowledgment between a producer
// and a consumer. The producer sets a target (latest tick time + optional order
// ID); the consumer advances its progress via TickDone and OrderUpdateDone.
// WaitSyncers blocks until every syncer's observed state satisfies its target.
//
// Satisfaction uses timestamp comparison rather than counting:
//   - doneTick >= targetTick
//   - doneOrder == targetOrder OR targetOrder == ""
//
// This tolerates duplicate tick deliveries: once doneTick reaches targetTick,
// re-delivery of the same timestamp leaves the condition already true.
type Syncer struct {
	mu   sync.Mutex
	cond sync.Cond

	// Target state set by the producer.
	targetTick  time.Time
	targetOrder string // "" means no order ack required

	// Progress state reported by the consumer.
	doneTick  time.Time
	doneOrder string

	tickCh chan TickEvent
}

func NewSyncer() *Syncer {
	s := &Syncer{
		tickCh: make(chan TickEvent, 1),
	}
	s.cond.L = &s.mu
	return s
}

// Notify sets the target tick and order, then delivers a TickEvent to Recv.
func (s *Syncer) Notify(t time.Time, orderIDs []string) {
	s.SetTarget(t, lastID(orderIDs))
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

// SetTarget advances the target to t and orderID.
func (s *Syncer) SetTarget(t time.Time, orderID string) {
	s.mu.Lock()
	s.targetTick = t
	s.targetOrder = orderID
	s.mu.Unlock()
}

// TickDone records that the consumer has processed a tick at time t. If t does
// not advance doneTick the call is a no-op and does not wake waiters.
func (s *Syncer) TickDone(t time.Time) {
	s.mu.Lock()
	if t.After(s.doneTick) {
		s.doneTick = t
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// OrderUpdateDone records that the consumer has processed an order update.
func (s *Syncer) OrderUpdateDone(orderID string) {
	s.mu.Lock()
	s.doneOrder = orderID
	s.cond.Broadcast()
	s.mu.Unlock()
}

// Done records completion for both tick and order in one step.
func (s *Syncer) Done(t time.Time, orderIDs []string) {
	s.mu.Lock()
	if t.After(s.doneTick) {
		s.doneTick = t
	}
	if id := lastID(orderIDs); id != "" {
		s.doneOrder = id
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

// satisfied reports whether the consumer has reached the current target.
// Must be called with mu held.
func (s *Syncer) satisfied() bool {
	if s.targetTick.IsZero() {
		return true
	}
	return !s.doneTick.Before(s.targetTick) &&
		(s.targetOrder == "" || s.doneOrder == s.targetOrder)
}

// IsSatisfied reports whether the consumer has reached the current target.
func (s *Syncer) IsSatisfied() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.satisfied()
}

func (s *Syncer) TargetTick() time.Time  { s.mu.Lock(); defer s.mu.Unlock(); return s.targetTick }
func (s *Syncer) DoneTick() time.Time    { s.mu.Lock(); defer s.mu.Unlock(); return s.doneTick }
func (s *Syncer) TargetOrder() string    { s.mu.Lock(); defer s.mu.Unlock(); return s.targetOrder }
func (s *Syncer) DoneOrder() string      { s.mu.Lock(); defer s.mu.Unlock(); return s.doneOrder }

// WaitSyncers blocks until every syncer is satisfied or ctx is cancelled.
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
			for !v.satisfied() {
				slog.Debug("syncer: waiting",
					"targetTick", v.targetTick, "doneTick", v.doneTick,
					"targetOrder", v.targetOrder, "doneOrder", v.doneOrder)
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

// lastID returns the last element of ids, or "" if ids is empty.
func lastID(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}
