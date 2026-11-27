// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/approval"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvkgo/kv"
)

// fakeApprover keeps requests in memory; tests answer them with decide.
type fakeApprover struct {
	mu        sync.Mutex
	byKey     map[string]*gobs.ApprovalState
	byID      map[string]*gobs.ApprovalState
	withdrawn []string
}

func newFakeApprover() *fakeApprover {
	return &fakeApprover{byKey: make(map[string]*gobs.ApprovalState), byID: make(map[string]*gobs.ApprovalState)}
}

func (f *fakeApprover) Ask(ctx context.Context, req *gobs.ApprovalConfig) (*gobs.ApprovalState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.byKey[req.Key]; ok {
		return s, nil
	}
	cfg := *req
	cfg.ID = fmt.Sprintf("id%d", len(f.byID))
	s := &gobs.ApprovalState{V1: &gobs.ApprovalStateV1{Config: &cfg, Progress: &gobs.ApprovalProgress{Status: approval.Pending}}}
	f.byKey[req.Key], f.byID[cfg.ID] = s, s
	return s, nil
}

func (f *fakeApprover) Get(ctx context.Context, id string) (*gobs.ApprovalState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("no request %q", id)
	}
	dup := *s.V1.Progress
	return &gobs.ApprovalState{V1: &gobs.ApprovalStateV1{Config: s.V1.Config, Progress: &dup}}, nil
}

func (f *fakeApprover) Withdraw(ctx context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.byID[id]; ok && s.V1.Progress.Status == approval.Pending {
		s.V1.Progress.Status = approval.Withdrawn
		f.withdrawn = append(f.withdrawn, id)
	}
	return nil
}

func (f *fakeApprover) decide(t *testing.T, id, status string, final bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok {
		t.Fatalf("no request %q", id)
	}
	s.V1.Progress.Status, s.V1.Progress.Final = status, final
}

func (f *fakeApprover) request(t *testing.T, id string) *gobs.ApprovalConfig {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.byID[id]
	if !ok {
		t.Fatalf("no request %q", id)
	}
	return s.V1.Config
}

func (f *fakeApprover) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byID)
}

// gatedPosition is a new position that asks ap.
func (e *testEnv) gatedPosition(uid string, ap approval.Approver) *Position {
	v := e.newPosition(uid)
	v.SetApprover(ap, "greeler-1")
	return v
}

func (e *testEnv) gatedLoad(uid string, ap approval.Approver) *Position {
	v := e.load(uid)
	v.SetApprover(ap, "greeler-1")
	return v
}

func check(t *testing.T, v *Position, fctx context.Context) {
	t.Helper()
	if err := v.Check(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
}

func heldID(t *testing.T, e *testEnv, uid string) string {
	t.Helper()
	var id string
	if err := kv.WithReader(context.Background(), e.db, func(ctx context.Context, r kv.Reader) (err error) {
		id, err = HeldContractID(ctx, uid, r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestApprovalGatesFirstAttempt(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()

	v := e.gatedPosition("p1", ap)
	c := putConstraint()
	c.Spot = d("230")
	if err := v.Open(context.Background(), fctx, c); err != nil {
		t.Fatal(err)
	}
	if len(v.legIDs) != 0 || v.active != nil {
		t.Fatalf("attempt started before approval: legs %v", v.legIDs)
	}
	p := v.Proposal()
	if p == nil || p.RequestID != "id0" || p.Key != "p1/ask-000000" || p.Contract.ContractID != putA || p.Switch {
		t.Fatalf("proposal = %+v", p)
	}
	req := ap.request(t, "id0")
	if want := "sell 1 AAPL 2026-12-18 200 PUT at no less than 0.50 (bid 1.00, ask 1.40, spot 230.00)"; req.Summary != want {
		t.Errorf("summary = %q, want %q", req.Summary, want)
	}
	if req.Owner != "greeler-1" || req.Kind != "sell-to-open" || req.Details["contract"] != putA || req.Details["yield-pct"] != "0.25" {
		t.Errorf("request = %+v", req)
	}
	if want := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC); !req.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want the session close %v", req.ExpiresAt, want)
	}
	if got := heldID(t, e, "p1"); got != putA {
		t.Errorf("held contract while pending = %q, want %q", got, putA)
	}

	// Pending: nothing happens, and a restart resumes the same request.
	check(t, v, fctx)
	w := e.gatedLoad("p1", ap)
	check(t, w, fctx)
	if n, _ := e.ex.counts(putA); n != 0 || ap.count() != 1 {
		t.Fatalf("orders %d requests %d while pending", n, ap.count())
	}

	ap.decide(t, "id0", approval.Approved, false)
	check(t, w, fctx)
	waitFor(t, "approved order", func() bool { n, _ := e.ex.counts(putA); return n == 1 })
	if w.proposal != nil || len(w.legIDs) != 1 {
		t.Fatalf("after approval: proposal %+v legs %v", w.proposal, w.legIDs)
	}
	if got := w.leg.MinPremium(); !got.Equal(d("0.50")) {
		t.Errorf("leg floor = %s, want the approved 0.50", got)
	}
	if r := e.load("p1"); r.proposal != nil {
		t.Errorf("saved proposal after approval: %+v", r.proposal)
	}
}

func TestApprovalRejectTriesNextContract(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()

	v := e.gatedPosition("p1", ap)
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	e.sel.set(putB)
	ap.decide(t, "id0", approval.Rejected, false)
	check(t, v, fctx)
	if p := v.Proposal(); p == nil || p.Contract.ContractID != putB || p.Key != "p1/ask-000001" {
		t.Fatalf("proposal after reject = %+v", p)
	}
	if w := e.load("p1"); len(w.rejected) != 1 || w.rejected[0] != putA || w.asks != 2 {
		t.Errorf("saved rejected %v asks %d", w.rejected, w.asks)
	}

	// A rejected contract is never selected again.
	e.sel.set(putA)
	if _, err := v.selectContract(context.Background(), putConstraint()); err == nil {
		t.Error("selection returned a rejected contract")
	}

	ap.decide(t, "id1", approval.Rejected, true)
	check(t, v, fctx)
	if v.Outcome() != "unfilled" || v.proposal != nil {
		t.Fatalf("after final reject: outcome %q proposal %+v", v.Outcome(), v.proposal)
	}
	if n, _ := e.ex.counts(putB); n != 0 {
		t.Errorf("orders after final reject: %d", n)
	}
}

func TestApprovalVoidedWhenContractNoLongerPasses(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()

	v := e.gatedPosition("p1", ap)
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	// The selector no longer accepts putA: the approval is void, and the
	// next request says so.
	e.sel.set(putB)
	ap.decide(t, "id0", approval.Approved, false)
	check(t, v, fctx)
	if n, _ := e.ex.counts(putA); n != 0 {
		t.Fatalf("voided approval placed %d orders", n)
	}
	p := v.Proposal()
	if p == nil || p.Contract.ContractID != putB {
		t.Fatalf("proposal after void = %+v", p)
	}
	if s := ap.request(t, p.RequestID).Summary; !strings.HasPrefix(s, "approval id0 voided (") {
		t.Errorf("summary after void = %q", s)
	}
}

func TestApprovalExpiredAsksAgain(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()

	v := e.gatedPosition("p1", ap)
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	ap.decide(t, "id0", approval.Expired, false)
	check(t, v, fctx)
	if p := v.Proposal(); p == nil || p.RequestID != "id1" || p.Contract.ContractID != putA {
		t.Fatalf("proposal after expiry = %+v", p)
	}
}

func TestApprovalAbandonWithdraws(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()

	v := e.gatedPosition("p1", ap)
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		t.Fatal(err)
	}
	if err := v.Abandon(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v.Outcome() != "unfilled" || len(ap.withdrawn) != 1 {
		t.Errorf("outcome %q withdrawn %v", v.Outcome(), ap.withdrawn)
	}
	if got := heldID(t, e, "p1"); got != "" {
		t.Errorf("held contract after abandon = %q", got)
	}
}

// approvedResting opens a gated position and approves its first contract,
// leaving the order resting.
func (e *testEnv) approvedResting(fctx context.Context, uid string, ap *fakeApprover) *Position {
	e.t.Helper()
	v := e.gatedPosition(uid, ap)
	if err := v.Open(context.Background(), fctx, putConstraint()); err != nil {
		e.t.Fatal(err)
	}
	ap.decide(e.t, v.proposal.RequestID, approval.Approved, false)
	check(e.t, v, fctx)
	waitFor(e.t, "first order", func() bool { n, _ := e.ex.counts(putA); return n == 1 })
	return v
}

func TestApprovalSwitch(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()
	v := e.approvedResting(fctx, "p1", ap)

	// Next session the selector prefers putB: a switch is asked for while
	// the putA order keeps working.
	e.sel.set(putB)
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	check(t, v, fctx)
	p := v.Proposal()
	if p == nil || !p.Switch || p.Contract.ContractID != putB {
		t.Fatalf("switch proposal = %+v", p)
	}
	if s := ap.request(t, p.RequestID).Summary; !strings.HasPrefix(s, "switch the unfilled sale of "+putA+" to: sell 1 AAPL") {
		t.Errorf("switch summary = %q", s)
	}
	if _, live := e.ex.counts(putA); live != 1 || len(v.legIDs) != 1 {
		t.Fatalf("live %d legs %v while the switch waits", live, v.legIDs)
	}

	// Reject keeps putA, with no more asks this session.
	ap.decide(t, p.RequestID, approval.Rejected, false)
	check(t, v, fctx)
	check(t, v, fctx)
	if v.proposal != nil || len(v.legIDs) != 1 || ap.count() != 2 {
		t.Fatalf("after switch reject: proposal %+v legs %v requests %d", v.proposal, v.legIDs, ap.count())
	}
	if len(v.rejected) != 0 {
		t.Errorf("switch reject recorded rejected %v", v.rejected)
	}

	// The next session asks again; approving switches.
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	check(t, v, fctx)
	p = v.Proposal()
	if p == nil || !p.Switch || ap.count() != 3 {
		t.Fatalf("second switch proposal = %+v (requests %d)", p, ap.count())
	}
	ap.decide(t, p.RequestID, approval.Approved, false)
	check(t, v, fctx)
	waitFor(t, "order for the new contract", func() bool { n, _ := e.ex.counts(putB); return n == 1 })
	if _, live := e.ex.counts(putA); live != 1 {
		t.Errorf("live orders after switch: %d", live)
	}
	if len(v.legIDs) != 2 || v.contract.ContractID != putB || v.proposal != nil {
		t.Errorf("after switch: legs %v contract %s proposal %+v", v.legIDs, v.contract.ContractID, v.proposal)
	}
}

func TestApprovalSwitchWithdrawnWhenFilled(t *testing.T) {
	e := newTestEnv(t)
	fctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ap := newFakeApprover()
	v := e.approvedResting(fctx, "p1", ap)

	e.sel.set(putB)
	e.clock.Set(e.clock.Now().Add(24 * time.Hour))
	check(t, v, fctx)
	if v.proposal == nil {
		t.Fatal("no switch proposal")
	}
	if !e.ex.fillLive() {
		t.Fatal("no live order to fill")
	}
	waitFor(t, "fill", func() bool { return v.leg.IsDone() })
	check(t, v, fctx)
	if v.proposal != nil || len(ap.withdrawn) != 1 || len(v.legIDs) != 1 {
		t.Errorf("after fill: proposal %+v withdrawn %v legs %v", v.proposal, ap.withdrawn, v.legIDs)
	}
}
