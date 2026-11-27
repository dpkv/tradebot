// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/bvk/tradebot/approval"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

// Approval gate (greel/approval-story.md scenarios 6 to 8): with an
// Approver, every selection that would start a sell-to-open attempt is
// saved as a proposal and asked for first. The first selection waits with
// no order; a switch to another contract waits while the current attempt
// keeps working.

// SetApprover makes the position ask approver before each new attempt, in
// owner's name. Nil, the default, never asks. Call it before Open or Check.
func (v *Position) SetApprover(approver approval.Approver, owner string) {
	v.approver, v.owner = approver, owner
}

// Proposal is the selection waiting for approval, or nil.
func (v *Position) Proposal() *gobs.OptProposal {
	if v.proposal == nil {
		return nil
	}
	dup := *v.proposal
	return &dup
}

// begin starts an attempt for sel, or proposes it when there is an
// Approver.
func (v *Position) begin(ctx, fctx context.Context, c *Constraint, sel *Selection, isSwitch bool) error {
	if v.approver == nil {
		return v.newAttempt(ctx, fctx, c, sel)
	}
	return v.propose(ctx, c, sel, isSwitch)
}

// sessionClose is when the session a selection at t is for closes.
func (v *Position) sessionClose(t time.Time) time.Time {
	open, next := v.session(t)
	if open {
		return next
	}
	_, close := v.session(next)
	return close
}

// propose saves sel as the proposal ahead of asking for it.
func (v *Position) propose(ctx context.Context, c *Constraint, sel *Selection, isSwitch bool) error {
	p := &gobs.OptProposal{
		Key:        fmt.Sprintf("%s/ask-%06d", v.uid, v.asks),
		Contract:   sel.Contract,
		MinPremium: sel.MinPremium,
		Spot:       c.Spot,
		Switch:     isSwitch,
		ExpiresAt:  v.sessionClose(v.now()),
	}
	v.proposal = p
	v.asks++
	if err := kv.WithReadWriter(ctx, v.db, v.Save); err != nil {
		v.proposal = nil
		v.asks--
		return fmt.Errorf("could not save optpos %s proposal: %w", v.uid, err)
	}
	slog.Info("proposed contract for approval", "optpos", v, "contract", sel.Contract.ContractID, "min-premium", sel.MinPremium, "switch", isSwitch)
	return v.ask(ctx, c)
}

// ask makes the request for the saved proposal; asking again with its key
// returns the same request.
func (v *Position) ask(ctx context.Context, c *Constraint) error {
	p := v.proposal
	note := v.voided
	state, err := v.approver.Ask(ctx, &gobs.ApprovalConfig{
		Key:       p.Key,
		Owner:     v.owner,
		Kind:      "sell-to-open",
		Summary:   note + v.summary(p, c),
		Details:   v.details(p),
		ExpiresAt: p.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("could not ask approval for optpos %s: %w", v.uid, err)
	}
	v.voided = ""
	p.RequestID = state.V1.Config.ID
	if err := kv.WithReadWriter(ctx, v.db, v.Save); err != nil {
		// The next Check asks again with the same key and gets this request.
		slog.Warn("could not save approval request id (will ask again)", "optpos", v, "approval", p.RequestID, "err", err)
		p.RequestID = ""
	}
	return nil
}

func numContracts(c *Constraint) decimal.Decimal {
	if c == nil || c.NumContracts.IsZero() {
		return decimal.NewFromInt(1)
	}
	return c.NumContracts
}

func (v *Position) summary(p *gobs.OptProposal, c *Constraint) string {
	k := p.Contract
	var b strings.Builder
	if p.Switch && v.leg != nil {
		fmt.Fprintf(&b, "switch the unfilled sale of %s to: ", v.leg.ContractID())
	}
	fmt.Fprintf(&b, "sell %s %s %s %s %s at no less than %s (bid %s, ask %s",
		numContracts(c), k.Underlying, k.Expiry.Format(time.DateOnly), k.Strike, k.OptionType,
		p.MinPremium.StringFixed(2), k.Bid.StringFixed(2), k.Ask.StringFixed(2))
	if p.Spot.IsPositive() {
		fmt.Fprintf(&b, ", spot %s", p.Spot.StringFixed(2))
	}
	b.WriteString(")")
	return b.String()
}

func (v *Position) details(p *gobs.OptProposal) map[string]string {
	k := p.Contract
	m := map[string]string{
		"contract":      k.ContractID,
		"type":          k.OptionType,
		"strike":        k.Strike.String(),
		"expiry":        k.Expiry.Format(time.DateOnly),
		"dte":           fmt.Sprint(int(k.Expiry.Sub(v.now()).Hours() / 24)),
		"min-premium":   p.MinPremium.String(),
		"bid":           k.Bid.String(),
		"ask":           k.Ask.String(),
		"open-interest": k.OpenInterest.String(),
	}
	if k.Strike.IsPositive() {
		m["yield-pct"] = p.MinPremium.Div(k.Strike).Mul(decimal.NewFromInt(100)).StringFixed(2)
	}
	if p.Spot.IsPositive() {
		m["spot"] = p.Spot.String()
	}
	if p.Switch && v.leg != nil {
		m["replaces"] = v.leg.ContractID()
	}
	return m
}

// clearProposal drops the proposal and saves.
func (v *Position) clearProposal(ctx context.Context) error {
	p := v.proposal
	v.proposal = nil
	if err := kv.WithReadWriter(ctx, v.db, v.Save); err != nil {
		v.proposal = p
		return fmt.Errorf("could not save optpos %s without its proposal: %w", v.uid, err)
	}
	return nil
}

// withdraw withdraws the proposal's request, if made, and drops the
// proposal.
func (v *Position) withdraw(ctx context.Context, reason string) error {
	p := v.proposal
	if p == nil {
		return nil
	}
	if p.RequestID != "" && v.approver != nil {
		if err := v.approver.Withdraw(ctx, p.RequestID, reason); err != nil {
			return err
		}
	}
	return v.clearProposal(ctx)
}

// proposeNext selects again and proposes the result.
func (v *Position) proposeNext(ctx context.Context, c *Constraint) error {
	sel, err := v.selectContract(ctx, c)
	if err == nil {
		err = v.propose(ctx, c, sel, false)
	}
	if err != nil {
		v.retryAt = v.now().Add(retryDelay)
		return err
	}
	v.selectedFor = v.sessionKey(v.now())
	return nil
}

// checkProposal reads the proposal's request and acts on its answer
// (greel/approval-story.md scenario 6).
func (v *Position) checkProposal(ctx, fctx context.Context, c *Constraint) error {
	p := v.proposal
	if v.approver == nil {
		// Approval was turned off while the proposal waited.
		slog.Info("dropping proposal: the position no longer asks for approval", "optpos", v, "contract", p.Contract.ContractID)
		return v.clearProposal(ctx)
	}
	if p.RequestID == "" {
		return v.ask(ctx, c)
	}
	state, err := v.approver.Get(ctx, p.RequestID)
	if err != nil {
		slog.Warn("could not read approval request (will retry)", "optpos", v, "approval", p.RequestID, "err", err)
		return nil
	}
	status := state.V1.Progress
	switch status.Status {
	case approval.Pending:
		return nil

	case approval.Approved:
		slog.Info("proposal was approved", "optpos", v, "approval", p.RequestID, "contract", p.Contract.ContractID)
		return v.approved(ctx, fctx, c)

	case approval.Rejected:
		slog.Info("proposal was rejected", "optpos", v, "approval", p.RequestID, "contract", p.Contract.ContractID, "final", status.Final, "reason", status.Reason)
		if p.Switch {
			// Any reject keeps the working contract until the next session.
			return v.clearProposal(ctx)
		}
		if status.Final {
			v.proposal = nil
			if err := v.finish(ctx, "unfilled", v.now(), nil); err != nil {
				v.proposal = p
				return err
			}
			slog.Info("abandoned option position: its contract was rejected as final", "optpos", v)
			return nil
		}
		if !slices.Contains(v.rejected, p.Contract.ContractID) {
			v.rejected = append(v.rejected, p.Contract.ContractID)
		}
		if err := v.clearProposal(ctx); err != nil {
			v.rejected = v.rejected[:len(v.rejected)-1]
			return err
		}
		return v.proposeNext(ctx, c)

	default: // expired or withdrawn
		slog.Info("proposal ended without an answer", "optpos", v, "approval", p.RequestID, "status", status.Status)
		if err := v.clearProposal(ctx); err != nil {
			return err
		}
		if p.Switch {
			return nil
		}
		return v.proposeNext(ctx, c)
	}
}

// approved re-validates the approved contract on a fresh chain (scenario
// 8) and starts its attempt; a contract that no longer passes voids the
// approval and is asked about again.
func (v *Position) approved(ctx, fctx context.Context, c *Constraint) error {
	p := v.proposal
	sel, err := v.selectFrom(ctx, c, p.Contract.ContractID)
	if err != nil {
		if errors.Is(err, errFetchChain) {
			slog.Warn("could not re-validate approved contract (will retry)", "optpos", v, "contract", p.Contract.ContractID, "err", err)
			return nil
		}
		slog.Warn("approved contract no longer passes selection; asking again", "optpos", v, "approval", p.RequestID, "contract", p.Contract.ContractID, "err", err)
		v.voided = fmt.Sprintf("approval %s voided (%v); ", p.RequestID, err)
		if err := v.clearProposal(ctx); err != nil {
			return err
		}
		if p.Switch {
			return v.reselect(ctx, fctx, c)
		}
		return v.proposeNext(ctx, c)
	}
	sel.MinPremium = p.MinPremium

	if p.Switch {
		if err := v.stop(ctx); err != nil {
			return err
		}
		if v.leg.FilledSize().IsPositive() {
			slog.Info("opening order filled before the approved switch; keeping it", "optpos", v, "contract", v.leg.ContractID())
			return v.clearProposal(ctx)
		}
		if c.Exclude != nil && c.Exclude(sel.Contract.ContractID) {
			return fmt.Errorf("optpos %s: approved %s was taken by a sibling while stopping the attempt", v.uid, sel.Contract.ContractID)
		}
	}

	nlegs := len(v.legIDs)
	v.proposal = nil
	if err := v.newAttempt(ctx, fctx, c, sel); err != nil {
		if len(v.legIDs) == nlegs {
			// Nothing was saved: keep the approval and try again next time.
			v.proposal = p
		}
		return err
	}
	return nil
}
