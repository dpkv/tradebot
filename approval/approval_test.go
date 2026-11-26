// Copyright (c) 2026 Deepak Vankadaru

package approval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
)

func newTestDB(t *testing.T) kv.Database {
	bdb, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bdb.Close() })
	return kvbadger.New(bdb, func(string) bool { return true })
}

type fakeMessenger struct {
	mu   sync.Mutex
	msgs []string
}

func (m *fakeMessenger) SendMessage(_ context.Context, _ time.Time, f string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, fmt.Sprintf(f, args...))
}

func (m *fakeMessenger) all() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.msgs...)
}

var t0 = time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) (*Store, *fakeMessenger, *time.Time) {
	m := &fakeMessenger{}
	s := New(newTestDB(t), m)
	now := t0
	s.now = func() time.Time { return now }
	return s, m, &now
}

func request(key string) *gobs.ApprovalConfig {
	return &gobs.ApprovalConfig{
		Key:       key,
		Owner:     "greeler-1",
		Kind:      "sell-to-open",
		Summary:   "sell 1 NVDA 2026-12-19 160 PUT at no less than 1.85",
		Details:   map[string]string{"strike": "160"},
		ExpiresAt: t0.Add(time.Hour),
	}
}

func status(t *testing.T, s *Store, id string) *gobs.ApprovalProgress {
	t.Helper()
	state, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return state.V1.Progress
}

func TestAskIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, m, _ := newTestStore(t)

	first, err := s.Ask(ctx, request("pos-1/ask-000000"))
	if err != nil {
		t.Fatal(err)
	}
	id := first.V1.Config.ID
	if len(id) != minIDLen || id != idFor("pos-1/ask-000000", minIDLen) {
		t.Fatalf("id = %q, want the %d-char hash of the key", id, minIDLen)
	}
	if got := first.V1.Progress.Status; got != Pending {
		t.Fatalf("status = %q, want pending", got)
	}
	if !first.V1.Config.AskedAt.Equal(t0) {
		t.Errorf("asked at = %v, want %v", first.V1.Config.AskedAt, t0)
	}

	again, err := s.Ask(ctx, request("pos-1/ask-000000"))
	if err != nil {
		t.Fatal(err)
	}
	if again.V1.Config.ID != id {
		t.Errorf("second ask id = %q, want %q", again.V1.Config.ID, id)
	}
	msgs := m.all()
	if len(msgs) != 1 {
		t.Fatalf("got %d notifications, want 1: %q", len(msgs), msgs)
	}
	if want := "tradebot approval approve " + id; !strings.Contains(msgs[0], want) || !strings.Contains(msgs[0], "greeler-1 asks: sell 1 NVDA") {
		t.Errorf("notification %q doesn't name the owner, summary and %q", msgs[0], want)
	}
}

func TestAskChecksRequest(t *testing.T) {
	s, _, _ := newTestStore(t)
	req := request("k")
	req.Summary = " "
	if _, err := s.Ask(context.Background(), req); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("Ask with no summary = %v, want os.ErrInvalid", err)
	}
	if _, err := s.Ask(context.Background(), nil); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("Ask(nil) = %v, want os.ErrInvalid", err)
	}
}

func TestAskLengthensCollidingID(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestStore(t)

	// Plant another key's request at the short ID of "mine".
	short := idFor("mine", minIDLen)
	other := &gobs.ApprovalState{V1: &gobs.ApprovalStateV1{
		Config:   &gobs.ApprovalConfig{ID: short, Key: "other", Owner: "o", Kind: "k", Summary: "s"},
		Progress: &gobs.ApprovalProgress{Status: Pending},
	}}
	if err := kvutil.SetDB(ctx, s.db, keyOf(short), other); err != nil {
		t.Fatal(err)
	}

	state, err := s.Ask(ctx, request("mine"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := state.V1.Config.ID, idFor("mine", minIDLen+1); got != want {
		t.Errorf("id = %q, want %q", got, want)
	}
	again, err := s.Ask(ctx, request("mine"))
	if err != nil {
		t.Fatal(err)
	}
	if again.V1.Config.ID != state.V1.Config.ID {
		t.Errorf("second ask id = %q, want %q", again.V1.Config.ID, state.V1.Config.ID)
	}
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	s, _, now := newTestStore(t)

	ask := func(key string) string {
		state, err := s.Ask(ctx, request(key))
		if err != nil {
			t.Fatal(err)
		}
		return state.V1.Config.ID
	}

	a := ask("a")
	*now = t0.Add(time.Minute)
	if err := s.Decide(ctx, a, true, false, "looks fine"); err != nil {
		t.Fatal(err)
	}
	if p := status(t, s, a); p.Status != Approved || p.Final || p.Reason != "looks fine" || !p.DecidedAt.Equal(*now) {
		t.Errorf("after approve = %+v", p)
	}
	if err := s.Decide(ctx, a, false, false, ""); !errors.Is(err, ErrNotPending) {
		t.Errorf("second decide = %v, want ErrNotPending", err)
	}

	r := ask("r")
	if err := s.Decide(ctx, r, false, true, "too close"); err != nil {
		t.Fatal(err)
	}
	if p := status(t, s, r); p.Status != Rejected || !p.Final {
		t.Errorf("after final reject = %+v", p)
	}

	if err := s.Decide(ctx, ask("f"), true, true, ""); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("final approve = %v, want os.ErrInvalid", err)
	}
	if err := s.Decide(ctx, "nosuch", true, false, ""); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("decide of a missing request = %v, want os.ErrNotExist", err)
	}
	if err := s.Decide(ctx, "../x", true, false, ""); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("decide of a bad id = %v, want os.ErrInvalid", err)
	}
}

func TestExpiry(t *testing.T) {
	ctx := context.Background()
	s, _, now := newTestStore(t)

	state, err := s.Ask(ctx, request("e"))
	if err != nil {
		t.Fatal(err)
	}
	id := state.V1.Config.ID

	*now = t0.Add(time.Hour)
	if err := s.Decide(ctx, id, true, false, ""); !errors.Is(err, ErrNotPending) {
		t.Errorf("decide after expiry = %v, want ErrNotPending", err)
	}
	if p := status(t, s, id); p.Status != Expired || !p.DecidedAt.Equal(*now) {
		t.Errorf("after expiry = %+v", p)
	}

	never := request("never")
	never.ExpiresAt = time.Time{}
	state, err = s.Ask(ctx, never)
	if err != nil {
		t.Fatal(err)
	}
	*now = t0.Add(1000 * time.Hour)
	if p := status(t, s, state.V1.Config.ID); p.Status != Pending {
		t.Errorf("request without expiry = %q, want pending", p.Status)
	}
}

func TestWithdraw(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestStore(t)

	state, err := s.Ask(ctx, request("w"))
	if err != nil {
		t.Fatal(err)
	}
	id := state.V1.Config.ID
	if err := s.Withdraw(ctx, id, "filled"); err != nil {
		t.Fatal(err)
	}
	if p := status(t, s, id); p.Status != Withdrawn || p.Reason != "filled" {
		t.Errorf("after withdraw = %+v", p)
	}
	if err := s.Decide(ctx, id, true, false, ""); !errors.Is(err, ErrNotPending) {
		t.Errorf("decide after withdraw = %v, want ErrNotPending", err)
	}

	// Withdrawing an answered request leaves the answer.
	state, err = s.Ask(ctx, request("w2"))
	if err != nil {
		t.Fatal(err)
	}
	id = state.V1.Config.ID
	if err := s.Decide(ctx, id, true, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Withdraw(ctx, id, "late"); err != nil {
		t.Fatal(err)
	}
	if p := status(t, s, id); p.Status != Approved {
		t.Errorf("withdraw of an approved request changed it to %q", p.Status)
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	s, _, now := newTestStore(t)

	var ids []string
	for i, key := range []string{"l1", "l2", "l3"} {
		*now = t0.Add(time.Duration(i) * time.Minute)
		state, err := s.Ask(ctx, request(key))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, state.V1.Config.ID)
	}
	if err := s.Decide(ctx, ids[0], false, false, ""); err != nil {
		t.Fatal(err)
	}

	pending, err := s.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := listIDs(pending); strings.Join(got, ",") != ids[2]+","+ids[1] {
		t.Errorf("pending = %v, want [%s %s]", got, ids[2], ids[1])
	}

	all, err := s.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := listIDs(all); strings.Join(got, ",") != ids[2]+","+ids[1]+","+ids[0] {
		t.Errorf("all = %v, want newest first", got)
	}

	// Past expiry, pending requests list as expired.
	*now = t0.Add(2 * time.Hour)
	pending, err = s.List(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending after expiry = %v, want none", listIDs(pending))
	}
}

func listIDs(states []*gobs.ApprovalState) []string {
	var ids []string
	for _, s := range states {
		ids = append(ids, s.V1.Config.ID)
	}
	return ids
}
