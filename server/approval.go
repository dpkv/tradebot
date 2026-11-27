// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bvk/tradebot/api"
)

func (s *Server) doApprovalList(ctx context.Context, req *api.ApprovalListRequest) (*api.ApprovalListResponse, error) {
	states, err := s.approvals.List(ctx, req.All)
	if err != nil {
		return nil, err
	}
	return &api.ApprovalListResponse{Requests: states}, nil
}

func (s *Server) doApprovalDecide(ctx context.Context, req *api.ApprovalDecideRequest) (_ *api.ApprovalDecideResponse, status error) {
	defer func() {
		if status != nil {
			slog.ErrorContext(ctx, "approval decision has failed", "error", status)
		}
	}()

	if err := req.Check(); err != nil {
		return nil, fmt.Errorf("invalid approval decision: %w", err)
	}
	id := strings.ToLower(strings.TrimSpace(req.ID))
	if err := s.approvals.Decide(ctx, id, req.Approve, req.Final, req.Reason); err != nil {
		return nil, err
	}
	state, err := s.approvals.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &api.ApprovalDecideResponse{Request: state}, nil
}
