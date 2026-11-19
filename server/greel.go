// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/greelladder"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
)

func (s *Server) doGreel(ctx context.Context, req *api.GreelRequest) (_ *api.GreelResponse, status error) {
	defer func() {
		if status != nil {
			slog.ErrorContext(ctx, "greel has failed", "error", status)
		}
	}()

	if err := req.Check(); err != nil {
		return nil, fmt.Errorf("invalid greel request: %w", err)
	}
	if err := s.checkGreelProduct(ctx, req.Config.AccountName, req.Config.ProductID); err != nil {
		return nil, err
	}

	uid := uuid.New().String()
	v, err := greeler.New(uid, req.Config)
	if err != nil {
		return nil, err
	}
	if err := s.startGreelJob(ctx, v, "Greeler", req.Options); err != nil {
		return nil, err
	}
	return &api.GreelResponse{UID: uid}, nil
}

func (s *Server) doGreelLadder(ctx context.Context, req *api.GreelLadderRequest) (_ *api.GreelLadderResponse, status error) {
	defer func() {
		if status != nil {
			slog.ErrorContext(ctx, "greel ladder has failed", "error", status)
		}
	}()

	if err := req.Check(); err != nil {
		return nil, fmt.Errorf("invalid greel ladder request: %w", err)
	}
	if err := s.checkGreelProduct(ctx, req.AccountName, req.ProductID); err != nil {
		return nil, err
	}

	uid := uuid.New().String()
	v, err := greelladder.New(uid, req.AccountName, req.ProductID, req.Bands)
	if err != nil {
		return nil, err
	}
	if err := s.startGreelJob(ctx, v, "GreelLadder", req.Options); err != nil {
		return nil, err
	}
	return &api.GreelLadderResponse{UID: uid}, nil
}

// checkGreelProduct rejects an account that can't trade options here,
// instead of leaving the job to fail when it first runs, and checks that
// the product is enabled.
func (s *Server) checkGreelProduct(ctx context.Context, accountName, productID string) error {
	s.mu.Lock()
	account, ok := s.accountMap[strings.ToLower(accountName)]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("account with name %q not found: %w", accountName, os.ErrNotExist)
	}
	if _, ok := account.(exchange.OptionsAccount); !ok {
		return fmt.Errorf("account %q doesn't support options: %w", accountName, os.ErrInvalid)
	}
	if _, err := s.getProduct(ctx, accountName, productID); err != nil {
		return err
	}
	return nil
}

// startGreelJob sets options on a new job before its first save, so it
// never runs without them, then saves, adds and resumes it.
func (s *Server) startGreelJob(ctx context.Context, v trader.Trader, typename string, options map[string]string) error {
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if _, err := v.SetOption(k, options[k]); err != nil {
			return fmt.Errorf("could not set option %s=%q: %w", k, options[k], err)
		}
	}

	uid := v.UID()
	start := func(ctx context.Context, rw kv.ReadWriter) error {
		if err := v.Save(ctx, rw); err != nil {
			return fmt.Errorf("could not save new %s: %w", typename, err)
		}
		if err := s.runner.Add(ctx, rw, uid, typename); err != nil {
			return fmt.Errorf("could not add new %s as a job: %w", typename, err)
		}
		return nil
	}
	if err := kv.WithReadWriter(ctx, s.db, start); err != nil {
		return err
	}

	if err := s.runner.Resume(ctx, uid, s.makeJobFunc(v), s.cg.Context()); err != nil {
		slog.Error("could not resume newly added job (ignored)", "type", typename, "err", err)
	}
	return nil
}
