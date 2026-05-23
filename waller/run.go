// Copyright (c) 2023 BVK Chaitanya

package waller

import (
	"context"
	"fmt"
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

	hangCh := make(chan error, 1)

	if rt.Syncer != nil {
		go func() {
			for {
				slog.Debug("waller syncer: waiting for tick event", "waller", w)
				event, err := rt.Syncer.Recv(ctx)
				if err != nil {
					slog.Error("waller syncer recv failed", "waller", w, "err", err)
					return
				}
				slog.Debug("waller syncer: received tick event", "waller", w, "tick", event.Time, "fills", len(event.OrderIDs), "order_ids", event.OrderIDs)
				var lastOrder string
				if len(event.OrderIDs) > 0 {
					lastOrder = event.OrderIDs[len(event.OrderIDs)-1]
				}
				for _, s := range w.syncers {
					s.SetTarget(event.Time, lastOrder)
				}
				slog.Debug("waller syncer: waiting for looper syncers", "waller", w, "loopers", len(w.syncers))
				watchdogDone := make(chan struct{})
				go func() {
					select {
					case <-time.After(5 * time.Second):
						slog.Error("waller syncer: hang detected", "waller", w, "tick", event.Time)
						for i, s := range w.syncers {
							if !s.IsSatisfied() {
								slog.Error("waller syncer: stuck looper", "looper_index", i, "looper", w.loopers[i].UID(), "target_tick", s.TargetTick(), "done_tick", s.DoneTick(), "target_order", s.TargetOrder(), "done_order", s.DoneOrder())
							}
						}
						hangCh <- fmt.Errorf("waller %v: sync hang at tick %v", w, event.Time)
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
		case err := <-hangCh:
			return err
		}
	}

	return context.Cause(ctx)
}
