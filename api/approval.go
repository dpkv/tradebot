// Copyright (c) 2026 Deepak Vankadaru

package api

import (
	"fmt"
	"strings"

	"github.com/bvk/tradebot/gobs"
)

const (
	ApprovalListPath   = "/approval/list"
	ApprovalDecidePath = "/approval/decide"
)

// ApprovalListRequest lists pending approval requests, or every request
// with All.
type ApprovalListRequest struct {
	All bool
}

type ApprovalListResponse struct {
	Requests []*gobs.ApprovalState // newest first
}

// ApprovalDecideRequest answers one pending approval request. Only a
// reject can be Final.
type ApprovalDecideRequest struct {
	ID      string
	Approve bool
	Final   bool
	Reason  string
}

type ApprovalDecideResponse struct {
	Request *gobs.ApprovalState // as decided
}

func (r *ApprovalDecideRequest) Check() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("approval id cannot be empty")
	}
	if r.Approve && r.Final {
		return fmt.Errorf("only a reject can be final")
	}
	return nil
}
