// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/approval"
	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvkgo/kvbadger"
	"github.com/dgraph-io/badger/v4"
)

func TestApprovalHandlers(t *testing.T) {
	ctx := context.Background()
	bdb, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bdb.Close() })
	db := kvbadger.New(bdb, func(string) bool { return true })
	s := &Server{db: db, approvals: approval.New(db, nil)}

	if rt := s.Runtime(fakeProduct{}); rt.Approver != s.approvals {
		t.Fatalf("runtime approver = %v, want the server's store", rt.Approver)
	}

	asked, err := s.approvals.Ask(ctx, &gobs.ApprovalConfig{Key: "k", Owner: "o", Kind: "sell-to-open", Summary: "sell 1 PUT"})
	if err != nil {
		t.Fatal(err)
	}
	id := asked.V1.Config.ID

	list, err := s.doApprovalList(ctx, &api.ApprovalListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Requests) != 1 || list.Requests[0].V1.Config.ID != id {
		t.Fatalf("list = %+v, want just %s", list.Requests, id)
	}

	if _, err := s.doApprovalDecide(ctx, &api.ApprovalDecideRequest{ID: id, Approve: true, Final: true}); err == nil {
		t.Error("final approve was accepted")
	}
	resp, err := s.doApprovalDecide(ctx, &api.ApprovalDecideRequest{ID: " " + id + " ", Reason: "no"})
	if err != nil {
		t.Fatal(err)
	}
	if p := resp.Request.V1.Progress; p.Status != approval.Rejected || p.Reason != "no" {
		t.Errorf("decided = %+v, want rejected with the reason", p)
	}
	if _, err := s.doApprovalDecide(ctx, &api.ApprovalDecideRequest{ID: id, Approve: true}); !errors.Is(err, approval.ErrNotPending) {
		t.Errorf("second decide = %v, want ErrNotPending", err)
	}

	list, err = s.doApprovalList(ctx, &api.ApprovalListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Requests) != 0 {
		t.Errorf("pending after decide = %d, want 0", len(list.Requests))
	}
	list, err = s.doApprovalList(ctx, &api.ApprovalListRequest{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Requests) != 1 {
		t.Errorf("all after decide = %d, want 1", len(list.Requests))
	}
}

// fakeProduct is just enough of a product for Runtime.
type fakeProduct struct{ exchange.Product }

func (fakeProduct) AccountName() string { return "fake" }
