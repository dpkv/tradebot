// Copyright (c) 2026 Deepak Vankadaru

// Package approval keeps requests a job makes for an operator's sign-off
// before it acts, and the operator's answers (greel/approval-story.md). A
// job asks through an Approver; the operator answers through the server,
// which calls Decide. The service records answers; what an answer means is
// up to the asker.
package approval

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvkgo/kv"
)

const DefaultKeyspace = "/approvals/"

// Statuses a request can have. Only pending changes.
const (
	Pending   = "pending"
	Approved  = "approved"
	Rejected  = "rejected"
	Expired   = "expired"
	Withdrawn = "withdrawn"
)

// ErrNotPending is returned by Decide for a request that is no longer
// pending, expired ones included.
var ErrNotPending = errors.New("approval request is not pending")

// minIDLen is the length of a request ID unless a shorter one collides.
const minIDLen = 6

// Approver is what a job sees.
type Approver interface {
	// Ask creates the request for req.Key unless one exists, notifies the
	// operator on creation, and returns the stored request either way.
	Ask(ctx context.Context, req *gobs.ApprovalConfig) (*gobs.ApprovalState, error)

	// Get returns the request; a pending one past its expiry is returned,
	// and saved, as expired.
	Get(ctx context.Context, id string) (*gobs.ApprovalState, error)

	// Withdraw ends a pending request the asker no longer needs. A request
	// that is no longer pending is left as it is.
	Withdraw(ctx context.Context, id, reason string) error
}

// Messenger sends the operator a notification. trader.Runtime's Messenger
// satisfies it.
type Messenger interface {
	SendMessage(context.Context, time.Time, string, ...interface{})
}

// Store keeps approval requests in a database. It is safe for concurrent
// use: every change is one transaction on one record.
type Store struct {
	db        kv.Database
	messenger Messenger // nil sends nothing

	now func() time.Time // overridable in tests
}

var _ Approver = &Store{}

// New returns a store over db that notifies through messenger, which may
// be nil.
func New(db kv.Database, messenger Messenger) *Store {
	return &Store{db: db, messenger: messenger, now: time.Now}
}

// idFor is the request ID of key at length n: a lowercase base-32 hash.
func idFor(key string, n int) string {
	sum := sha256.Sum256([]byte(key))
	s := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	return s[:min(n, len(s))]
}

func keyOf(id string) string {
	return path.Join(DefaultKeyspace, id)
}

func checkConfig(req *gobs.ApprovalConfig) error {
	if req == nil {
		return fmt.Errorf("approval request is nil: %w", os.ErrInvalid)
	}
	for _, f := range []struct{ name, v string }{
		{"key", req.Key}, {"owner", req.Owner}, {"kind", req.Kind}, {"summary", req.Summary},
	} {
		if strings.TrimSpace(f.v) == "" {
			return fmt.Errorf("approval request %s is empty: %w", f.name, os.ErrInvalid)
		}
	}
	return nil
}

func (s *Store) Ask(ctx context.Context, req *gobs.ApprovalConfig) (*gobs.ApprovalState, error) {
	if err := checkConfig(req); err != nil {
		return nil, err
	}
	var result *gobs.ApprovalState
	created := false
	err := kv.WithReadWriter(ctx, s.db, func(ctx context.Context, rw kv.ReadWriter) error {
		result, created = nil, false
		full := len(idFor(req.Key, 1<<10))
		for n := minIDLen; n <= full; n++ {
			id := idFor(req.Key, n)
			old, err := kvutil.Get[gobs.ApprovalState](ctx, rw, keyOf(id))
			if err == nil {
				if old.V1 != nil && old.V1.Config != nil && old.V1.Config.Key == req.Key {
					result = old
					return nil
				}
				continue // another key's request; try a longer ID
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			cfg := *req
			cfg.ID = id
			cfg.Details = maps.Clone(req.Details)
			if cfg.AskedAt.IsZero() {
				cfg.AskedAt = s.now()
			}
			state := &gobs.ApprovalState{V1: &gobs.ApprovalStateV1{
				Config:   &cfg,
				Progress: &gobs.ApprovalProgress{Status: Pending},
			}}
			if err := kvutil.Set(ctx, rw, keyOf(id), state); err != nil {
				return err
			}
			result, created = state, true
			return nil
		}
		return fmt.Errorf("could not find a free approval id for key %q", req.Key)
	})
	if err != nil {
		return nil, fmt.Errorf("could not ask for approval: %w", err)
	}
	if created {
		cfg := result.V1.Config
		slog.Info("asked for approval", "approval", cfg.ID, "owner", cfg.Owner, "kind", cfg.Kind, "summary", cfg.Summary, "expires-at", cfg.ExpiresAt)
		s.notify(ctx, cfg)
	}
	return result, nil
}

func (s *Store) notify(ctx context.Context, cfg *gobs.ApprovalConfig) {
	if s.messenger == nil {
		return
	}
	expiry := ""
	if !cfg.ExpiresAt.IsZero() {
		expiry = "; expires " + cfg.ExpiresAt.Local().Format("2006-01-02 15:04 MST")
	}
	s.messenger.SendMessage(ctx, s.now(), "%s asks: %s. Approve with `tradebot approval approve %s`%s.", cfg.Owner, cfg.Summary, cfg.ID, expiry)
}

// expire marks state expired if it is pending past its expiry, and
// reports whether it did.
func (s *Store) expire(state *gobs.ApprovalState, now time.Time) bool {
	cfg, p := state.V1.Config, state.V1.Progress
	if p.Status != Pending || cfg.ExpiresAt.IsZero() || now.Before(cfg.ExpiresAt) {
		return false
	}
	p.Status, p.DecidedAt = Expired, now
	return true
}

func get(ctx context.Context, r kv.Reader, id string) (*gobs.ApprovalState, error) {
	if id == "" || strings.Contains(id, "/") {
		return nil, fmt.Errorf("approval id %q is invalid: %w", id, os.ErrInvalid)
	}
	state, err := kvutil.Get[gobs.ApprovalState](ctx, r, keyOf(id))
	if err != nil {
		return nil, err
	}
	if state.V1 == nil || state.V1.Config == nil || state.V1.Progress == nil {
		return nil, fmt.Errorf("approval record %q is incomplete", id)
	}
	return state, nil
}

// update loads request id, applies fn to it once any expiry is applied,
// and saves it if either changed it.
func (s *Store) update(ctx context.Context, id string, fn func(*gobs.ApprovalState) (bool, error)) (*gobs.ApprovalState, error) {
	var result *gobs.ApprovalState
	err := kv.WithReadWriter(ctx, s.db, func(ctx context.Context, rw kv.ReadWriter) error {
		state, err := get(ctx, rw, id)
		if err != nil {
			return err
		}
		changed := s.expire(state, s.now())
		if fn != nil {
			c, err := fn(state)
			if err != nil {
				// Nothing is saved; an expiry is applied again next time.
				return err
			}
			changed = changed || c
		}
		result = state
		if changed {
			return kvutil.Set(ctx, rw, keyOf(id), state)
		}
		return nil
	})
	return result, err
}

func (s *Store) Get(ctx context.Context, id string) (*gobs.ApprovalState, error) {
	state, err := s.update(ctx, id, nil)
	if err != nil {
		return nil, fmt.Errorf("could not get approval %q: %w", id, err)
	}
	return state, nil
}

func (s *Store) Withdraw(ctx context.Context, id, reason string) error {
	withdrew := false
	_, err := s.update(ctx, id, func(state *gobs.ApprovalState) (bool, error) {
		withdrew = false
		p := state.V1.Progress
		if p.Status != Pending {
			return false, nil
		}
		p.Status, p.Reason, p.DecidedAt = Withdrawn, reason, s.now()
		withdrew = true
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("could not withdraw approval %q: %w", id, err)
	}
	if withdrew {
		slog.Info("withdrew approval request", "approval", id, "reason", reason)
	}
	return nil
}

// Decide answers a pending request: approve, or reject (finally, if
// final). It fails with ErrNotPending once the request has expired or been
// answered or withdrawn, so an answer never lands on a request its asker
// has given up on.
func (s *Store) Decide(ctx context.Context, id string, approve, final bool, reason string) error {
	if approve && final {
		return fmt.Errorf("only a reject can be final: %w", os.ErrInvalid)
	}
	_, err := s.update(ctx, id, func(state *gobs.ApprovalState) (bool, error) {
		p := state.V1.Progress
		if p.Status != Pending {
			return false, fmt.Errorf("approval %q is %s: %w", id, p.Status, ErrNotPending)
		}
		p.Status = Rejected
		if approve {
			p.Status = Approved
		}
		p.Final, p.Reason, p.DecidedAt = final, reason, s.now()
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("could not decide approval %q: %w", id, err)
	}
	slog.Info("decided approval request", "approval", id, "approve", approve, "final", final, "reason", reason)
	return nil
}

// List returns pending requests, or every request with all, newest first.
// A pending request past its expiry is listed as expired (unsaved; the
// next Get or Decide saves it).
func (s *Store) List(ctx context.Context, all bool) ([]*gobs.ApprovalState, error) {
	var states []*gobs.ApprovalState
	now := s.now()
	begin, end := kvutil.PathRange(DefaultKeyspace)
	err := kvutil.AscendDB(ctx, s.db, begin, end, func(_ context.Context, _ kv.Reader, key string, state *gobs.ApprovalState) error {
		if state.V1 == nil || state.V1.Config == nil || state.V1.Progress == nil {
			slog.Warn("skipping incomplete approval record", "key", key)
			return nil
		}
		s.expire(state, now)
		if all || state.V1.Progress.Status == Pending {
			states = append(states, state)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("could not list approvals: %w", err)
	}
	sort.SliceStable(states, func(i, j int) bool {
		return states[i].V1.Config.AskedAt.After(states[j].V1.Config.AskedAt)
	})
	return states, nil
}
