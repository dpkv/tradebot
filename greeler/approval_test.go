// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvkgo/kv"
)

func TestWheelApprovalOption(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())

	if undo, err := v.SetOption("wheel-approval", "manual"); err != nil || undo != "auto" || !v.manualApprovalOpt {
		t.Fatalf("set manual: undo %q err %v", undo, err)
	}
	if _, err := v.SetOption("wheel-approval", "sometimes"); err == nil {
		t.Error("an invalid wheel-approval was accepted")
	}
	if err := kv.WithReadWriter(context.Background(), e.db, v.Save); err != nil {
		t.Fatal(err)
	}
	if w := e.reload(v); !w.manualApprovalOpt {
		t.Error("wheel-approval=manual was not saved")
	}
	if undo, err := v.SetOption("wheel-approval", "auto"); err != nil || undo != "manual" || v.manualApprovalOpt {
		t.Fatalf("set auto: undo %q err %v", undo, err)
	}
	if opts := v.options(); opts["wheel-approval"] != "" {
		t.Errorf("auto is saved as %q", opts["wheel-approval"])
	}
}

func TestWheelApprovalNeedsApprover(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	if _, err := v.SetOption("wheel-approval", "manual"); err != nil {
		t.Fatal(err)
	}
	rt := *e.rt
	rt.Approver = nil
	if err := v.Run(context.Background(), &rt); !errors.Is(err, os.ErrInvalid) {
		t.Errorf("Run without an approver = %v, want os.ErrInvalid", err)
	}
}

// proposingPosition is a fakePosition with a contract waiting for
// approval.
type proposingPosition struct {
	*fakePosition
	proposal *gobs.OptProposal
}

func (p *proposingPosition) Proposal() *gobs.OptProposal { return p.proposal }

func TestPendingProposalIsHeld(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	pos := &proposingPosition{
		fakePosition: &fakePosition{uid: "p"},
		proposal:     &gobs.OptProposal{Contract: &gobs.OptionContract{ContractID: "AAPL_P1"}},
	}
	v.updateHeld(pos)
	if held, known := v.HeldContract(); held != "AAPL_P1" || !known {
		t.Errorf("held = %q (known %v), want the proposed contract", held, known)
	}
	pos.proposal = nil
	v.updateHeld(pos)
	if held, _ := v.HeldContract(); held != "" {
		t.Errorf("held without a proposal = %q", held)
	}
}

func TestConstraintCarriesSpot(t *testing.T) {
	e := newTestEnv(t)
	v := e.newGreeler(testConfig())
	r := e.runner(v)
	pos := flipToPut(t, e, v, r)
	if len(pos.opens) != 1 || !pos.opens[0].Spot.Equal(d("200")) {
		t.Errorf("open constraint spot = %v, want 200", pos.opens)
	}
}
