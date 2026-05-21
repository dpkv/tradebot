// Copyright (c) 2023 BVK Chaitanya

package waller

import (
	"context"
	"log"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/bvk/tradebot/looper"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
)

func (w *Waller) Fix(ctx context.Context, rt *trader.Runtime) error {
	for _, l := range w.loopers {
		if err := l.Fix(ctx, rt); err != nil {
			return err
		}
	}
	return nil
}

func (w *Waller) Refresh(ctx context.Context, rt *trader.Runtime) error {
	for _, l := range w.loopers {
		if err := l.Refresh(ctx, rt); err != nil {
			return err
		}
	}
	return nil
}

func (w *Waller) Run(ctx context.Context, rt *trader.Runtime) (status error) {
	slog.Info("started waller", "waller", w)
	defer func() {
		slog.Info("stopping waller", "waller", w, "status", status)
	}()

	if w.summary.Load() == nil {
		if err := kv.WithReadWriter(ctx, rt.Database, w.Save); err != nil {
			return err
		}
	}

	jobUpdatesCh := make(chan string, len(w.loopers))
	ctx = trader.WithJobUpdateChannel(ctx, jobUpdatesCh)

	w.syncers = make([]*trader.Syncer, len(w.loopers))

	var nloopers atomic.Int64
	loopMap := make(map[string]*looper.Looper)
	for i, loop := range w.loopers {
		loop := loop
		loopMap[loop.UID()] = loop
		w.syncers[i] = trader.NewSyncer()
		looperRt := *rt
		looperRt.Syncer = w.syncers[i]

		nloopers.Add(1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("CAUGHT PANIC", "panic", r)
					slog.Error(string(debug.Stack()))
					panic(r)
				}
			}()

			defer func() {
				nloopers.Add(-1)
				jobUpdatesCh <- loop.UID()
			}()

			for ctx.Err() == nil {
				if err := loop.Run(ctx, &looperRt); err != nil {
					if ctx.Err() == nil {
						log.Printf("wall-looper %v has failed (fix manually): %v", loop, err)
						return
					}
					continue
				}
				// Looper job is completed successfully.
				return
			}
		}()
	}

	if rt.Syncer != nil {
		go func() {
			var totalForcedAcks int
			defer func() {
				if totalForcedAcks > 0 {
					slog.Warn("waller syncer: total forced order update acks", "waller", w, "total", totalForcedAcks)
				}
			}()
			for {
				slog.Debug("waller syncer: waiting for tick event", "waller", w)
				event, err := rt.Syncer.Recv(ctx)
				if err != nil {
					slog.Error("waller syncer recv failed", "waller", w, "err", err)
					return
				}
				slog.Debug("waller syncer: received tick event", "waller", w, "tick", event.Time, "fills", len(event.OrderIDs), "order_ids", event.OrderIDs)
				for _, s := range w.syncers {
					s.Add(1, len(event.OrderIDs))
				}
				slog.Debug("waller syncer: waiting for looper syncers", "waller", w, "loopers", len(w.syncers))
				watchdogDone := make(chan struct{})
				go func() {
					select {
					case <-time.After(5 * time.Second):
						slog.Warn("waller syncer: hang detected, dumping stuck syncers", "waller", w, "tick", event.Time, "fills", len(event.OrderIDs), "order_ids", event.OrderIDs)
						for i, s := range w.syncers {
							td, dd, to, do_ := s.TodoTicks(), s.DoneTicks(), s.TodoOrderUpdates(), s.DoneOrderUpdates()
							if td > dd {
								slog.Error("waller syncer: stuck looper missing tick done — not recovering", "looper_index", i, "looper", w.loopers[i].UID(), "todo_ticks", td, "done_ticks", dd)
							} else if to > do_ {
								before := s.DoneOrderUpdates()
								for _, id := range event.OrderIDs {
									s.OrderUpdateDone(id)
								}
								forced := int(s.DoneOrderUpdates() - before)
								totalForcedAcks += forced
								slog.Warn("waller syncer: forced missing order update acks", "looper_index", i, "looper", w.loopers[i].UID(), "forced", forced, "total_forced_so_far", totalForcedAcks)
							}
						}
					case <-watchdogDone:
					}
				}()
				if err := trader.WaitSyncers(ctx, w.syncers); err != nil {
					close(watchdogDone)
					slog.Error("waller syncer wait failed", "waller", w, "err", err)
					return
				}
				close(watchdogDone)
				slog.Debug("waller syncer: looper syncers done", "waller", w)
				rt.Syncer.Done(event.Time, event.OrderIDs)
				slog.Debug("waller syncer: done signaled to engine", "waller", w)
			}
		}()
	}

	for nloopers.Load() > 0 {
		select {
		case uid := <-jobUpdatesCh:
			if _, ok := loopMap[uid]; ok {
				w.summary.Store(nil)
				if err := kv.WithReadWriter(context.Background(), rt.Database, w.Save); err != nil {
					slog.Error("could not save waller to the database (ignored; will retry)", "err", err)
				}
			}
		}
	}

	return context.Cause(ctx)
}
