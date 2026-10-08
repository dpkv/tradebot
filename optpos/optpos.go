// Copyright (c) 2026 Deepak Vankadaru

// Package optpos implements one written-option position: opened by a
// sell-to-open and held until the broker settles it (v1).
package optpos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/job"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/optlimiter"
	"github.com/bvkgo/kv"
	"github.com/shopspring/decimal"
)

const DefaultKeyspace = "/optpositions/"

// settlementInterval is how often Check asks the broker how the contract
// stands.
const settlementInterval = time.Hour

// retryDelay is how long Check waits before selecting again after Open
// failed, or restarting an attempt that failed.
const retryDelay = time.Minute

// ErrOpened is returned by Abandon when the opening order has filled: the
// position is open and holds to settlement.
var ErrOpened = errors.New("option position is open")

// errStopped is the cause an attempt is canceled with when the position
// stops it.
var errStopped = errors.New("option position stopped the attempt")

// Constraint bounds contract selection to what the owning greeler allows —
// derived from its levels, not a knob. The greeler passes it to Open and
// Check.
type Constraint struct {
	Underlying string
	OptionType string // "PUT" for CSP, "CALL" for CC

	// NumContracts is how many contracts each attempt sells; zero means one.
	NumContracts decimal.Decimal

	MaxStrike decimal.Decimal // CSP: <= levels and <= cash/100; zero means no bound
	MinStrike decimal.Decimal // CC: >= levels' sell prices; zero means no bound

	// ContractSize is the shares per contract the levels are sized for;
	// adjusted contracts of any other size are ruled out. Zero means any.
	ContractSize decimal.Decimal

	// Exclude reports contracts a sibling greeler already holds or is
	// selecting, so two siblings never write the same contract series. Nil
	// when the greeler runs standalone.
	Exclude func(contractID string) bool
}

// Selection is the contract to open and the minimum per-share premium
// worth selling it for.
type Selection struct {
	Contract   *gobs.OptionContract
	MinPremium decimal.Decimal
}

// ContractSelector picks the next contract to open. Its knobs (target-
// delta, dte-range, min-premium-yield, min-open-interest, max-spread-pct)
// are injected at construction; Constraint is what varies call to call.
type ContractSelector interface {
	Select(ctx context.Context, chain []*gobs.OptionContract, c *Constraint) (*Selection, error)
}

// attempt is the current leg running in its own goroutine.
type attempt struct {
	job    *job.Job
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
	err    error // the leg's Run result, valid once done is closed
}

// Position is one written-option position: opened by a sell-to-open,
// held until settled (v1). Component, not a job — driven by its owning
// greeler, which calls its methods from one goroutine.
type Position struct {
	uid          string
	exchangeName string
	underlying   string

	selector ContractSelector
	knobs    *gobs.WheelKnobs // re-price step/interval for each attempt

	contract *gobs.OptionContract // cache: current attempt's contract

	legIDs []string // sell-to-open attempts, in order

	outcome    string
	outcomeAt  time.Time
	assignment *gobs.AssignmentFact

	leg     *optlimiter.OptLimiter  // last attempt; nil before Open and once terminal after Load
	product exchange.OptionsProduct // leg's product while it may run
	active  *attempt                // the attempt in flight, nil if none

	selectedFor         string    // session the contract was last selected for; in memory only
	lastSettlementCheck time.Time // in memory only
	retryAt             time.Time // no Open or attempt restart before this; in memory only

	// Held at construction (New/Load), not re-passed to every method.
	optEx exchange.OptionsExchange
	db    kv.Database

	// Overridable in tests.
	now     func() time.Time
	session func(time.Time) (open bool, next time.Time)
	legHook func(*optlimiter.OptLimiter)
}

// New creates a position with empty progress. The greeler saves it under
// its wheel epoch before calling Open.
func New(uid, exchangeName, underlying string, selector ContractSelector, knobs *gobs.WheelKnobs, optEx exchange.OptionsExchange, db kv.Database) *Position {
	v := &Position{
		uid:          uid,
		exchangeName: exchangeName,
		underlying:   underlying,
		selector:     selector,
		knobs:        knobs,
		optEx:        optEx,
		db:           db,
	}
	v.setDefaults()
	return v
}

func (v *Position) setDefaults() {
	v.now = time.Now
	v.session = optlimiter.RegularSession
}

func (v *Position) check() error {
	if len(v.uid) == 0 {
		return fmt.Errorf("optpos uid is empty")
	}
	if len(v.exchangeName) == 0 {
		return fmt.Errorf("optpos exchange name is empty")
	}
	if len(v.underlying) == 0 {
		return fmt.Errorf("optpos underlying is empty")
	}
	if v.selector == nil {
		return fmt.Errorf("optpos contract selector is nil")
	}
	if v.optEx == nil || v.db == nil {
		return fmt.Errorf("optpos exchange or database is nil")
	}
	return nil
}

func (v *Position) String() string {
	return "optpos:" + v.uid
}

func (v *Position) LogValue() slog.Value {
	return slog.StringValue(v.uid)
}

func (v *Position) UID() string { return v.uid }

// Outcome is empty while open or settling; terminal otherwise.
func (v *Position) Outcome() string { return v.outcome }

func (v *Position) OutcomeAt() time.Time { return v.outcomeAt }

// Assignment is set iff Outcome is "assigned".
func (v *Position) Assignment() *gobs.AssignmentFact { return v.assignment }

// Contract is the current attempt's contract, nil before Open.
func (v *Position) Contract() *gobs.OptionContract { return v.contract }

// Open selects a contract and starts the sell-to-open attempt. The greeler
// has already saved this position's empty record under its wheel epoch;
// Open saves the attempt's UID before it can place an order. The attempt
// runs until fctx is canceled.
func (v *Position) Open(ctx context.Context, fctx context.Context, c *Constraint) error {
	if err := v.check(); err != nil {
		return err
	}
	if v.outcome != "" || len(v.legIDs) != 0 {
		return fmt.Errorf("optpos %s is already opened: %w", v.uid, os.ErrExist)
	}
	sel, err := v.selectContract(ctx, c)
	if err == nil {
		err = v.newAttempt(ctx, fctx, c, sel)
	}
	if err != nil {
		v.retryAt = v.now().Add(retryDelay)
		return err
	}
	v.selectedFor = v.sessionKey(v.now())
	return nil
}

// Check runs settlement, settling, and re-selection, in that order, and
// keeps the opening attempt running. Called by the owning greeler every
// iteration while the position is open; a no-op once Outcome is set. After
// a restart, Check also does what Open would if no attempt was saved.
func (v *Position) Check(ctx context.Context, fctx context.Context, c *Constraint) error {
	if v.outcome != "" {
		return nil
	}
	now := v.now()
	if len(v.legIDs) == 0 {
		if now.Before(v.retryAt) {
			return nil
		}
		return v.Open(ctx, fctx, c)
	}
	v.reap()

	filled := v.leg.FilledSize().IsPositive()

	if filled && now.Sub(v.lastSettlementCheck) >= settlementInterval {
		v.lastSettlementCheck = now
		settled, err := v.checkSettlement(ctx, now)
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
	}

	if filled && !now.Before(v.contract.Expiry) {
		// Settling: only the broker can say whether it expired or was
		// assigned. Nothing more can sell past expiry.
		if v.active != nil {
			return v.stop(ctx)
		}
		return nil
	}

	if !filled {
		if open, _ := v.session(now); open {
			if key := v.sessionKey(now); key != v.selectedFor {
				if err := v.reselect(ctx, fctx, c); err != nil {
					return err
				}
				v.selectedFor = key
			}
		}
	}

	if v.active != nil || v.leg.IsDone() || fctx.Err() != nil || now.Before(v.retryAt) {
		return nil
	}
	return v.start(ctx, fctx)
}

// Stop stops the attempt in flight, if any, and waits until its live order
// is canceled and confirmed. The position stays open; the next Check
// restarts the attempt. The owning greeler calls it before its Run returns,
// so no order is left working without an owner.
func (v *Position) Stop(ctx context.Context) error {
	if v.active == nil {
		return nil
	}
	return v.stop(ctx)
}

// Abandon ends a position whose opening order never filled, with Outcome
// "unfilled". It stops the live attempt first and fails with ErrOpened if
// the order filled — the position is then open.
func (v *Position) Abandon(ctx context.Context) error {
	switch v.outcome {
	case "":
	case "unfilled":
		return nil
	default:
		return fmt.Errorf("optpos %s has already ended %q", v.uid, v.outcome)
	}
	if err := v.stop(ctx); err != nil {
		return err
	}
	if v.leg != nil && v.leg.FilledSize().IsPositive() {
		return fmt.Errorf("could not abandon optpos %s: %w", v.uid, ErrOpened)
	}
	if err := v.finish(ctx, "unfilled", v.now(), nil); err != nil {
		return err
	}
	slog.Info("abandoned option position that never opened", "optpos", v, "legs", len(v.legIDs))
	return nil
}

// sessionKey names the session a selection at t is for: today's while the
// session is open, else the next one.
func (v *Position) sessionKey(t time.Time) string {
	_, next := v.session(t)
	return next.Format(time.DateOnly)
}

// selectContract fetches the chain and asks the selector, then checks the
// answer against the constraint.
func (v *Position) selectContract(ctx context.Context, c *Constraint) (*Selection, error) {
	if c == nil {
		return nil, fmt.Errorf("optpos %s: constraint is nil: %w", v.uid, os.ErrInvalid)
	}
	if c.Underlying != "" && c.Underlying != v.underlying {
		return nil, fmt.Errorf("optpos %s: constraint is for %q, want %q: %w", v.uid, c.Underlying, v.underlying, os.ErrInvalid)
	}
	chain, err := v.optEx.GetOptionsChain(ctx, v.underlying)
	if err != nil {
		return nil, fmt.Errorf("could not fetch options chain for %s: %w", v.underlying, err)
	}
	sel, err := v.selector.Select(ctx, chain, c)
	if err != nil {
		return nil, fmt.Errorf("could not select a contract for %s: %w", v.underlying, err)
	}
	if err := v.checkSelection(sel, c); err != nil {
		return nil, err
	}
	return sel, nil
}

// checkSelection guards against a selector returning a contract the
// constraint rules out.
func (v *Position) checkSelection(sel *Selection, c *Constraint) error {
	if sel == nil || sel.Contract == nil || sel.Contract.ContractID == "" {
		return fmt.Errorf("optpos %s: selector returned no contract", v.uid)
	}
	contract := sel.Contract
	id := contract.ContractID
	if contract.Underlying != v.underlying {
		return fmt.Errorf("optpos %s: selected %s is for %q, want %q", v.uid, id, contract.Underlying, v.underlying)
	}
	if c.OptionType != "" && contract.OptionType != c.OptionType {
		return fmt.Errorf("optpos %s: selected %s is a %s, want a %s", v.uid, id, contract.OptionType, c.OptionType)
	}
	if c.MaxStrike.IsPositive() && contract.Strike.GreaterThan(c.MaxStrike) {
		return fmt.Errorf("optpos %s: selected %s strike %s is above %s", v.uid, id, contract.Strike, c.MaxStrike)
	}
	if c.MinStrike.IsPositive() && contract.Strike.LessThan(c.MinStrike) {
		return fmt.Errorf("optpos %s: selected %s strike %s is below %s", v.uid, id, contract.Strike, c.MinStrike)
	}
	if c.ContractSize.IsPositive() && !contract.ContractSize.Equal(c.ContractSize) {
		return fmt.Errorf("optpos %s: selected %s covers %s shares, want %s", v.uid, id, contract.ContractSize, c.ContractSize)
	}
	if c.Exclude != nil && c.Exclude(id) {
		return fmt.Errorf("optpos %s: selected %s is held by a sibling", v.uid, id)
	}
	return nil
}

// reselect re-runs the selector for an attempt that hasn't filled. A
// different contract replaces the attempt unless it fills while stopping.
func (v *Position) reselect(ctx context.Context, fctx context.Context, c *Constraint) error {
	sel, err := v.selectContract(ctx, c)
	if err != nil {
		slog.Warn("could not re-select contract (keeping the current one)", "optpos", v, "contract", v.leg.ContractID(), "err", err)
		return nil
	}
	if sel.Contract.ContractID == v.leg.ContractID() {
		return nil
	}
	if err := v.stop(ctx); err != nil {
		return err
	}
	if v.leg.FilledSize().IsPositive() {
		slog.Info("opening order filled while re-selecting; keeping it", "optpos", v, "contract", v.leg.ContractID(), "filled", v.leg.FilledSize())
		return nil
	}
	slog.Info("re-selected contract", "optpos", v, "old", v.leg.ContractID(), "new", sel.Contract.ContractID)
	return v.newAttempt(ctx, fctx, c, sel)
}

// newAttempt saves a new leg for sel ahead of starting it, so a crash
// resumes the same attempt instead of selecting again.
func (v *Position) newAttempt(ctx context.Context, fctx context.Context, c *Constraint, sel *Selection) error {
	numContracts := c.NumContracts
	if numContracts.IsZero() {
		numContracts = decimal.NewFromInt(1)
	}
	var step decimal.Decimal
	var interval time.Duration
	if v.knobs != nil {
		step, interval = v.knobs.RepriceStep, v.knobs.RepriceInterval
	}
	contract := sel.Contract
	legUID := path.Join(v.uid, fmt.Sprintf("leg-%06d", len(v.legIDs)))
	leg, err := optlimiter.New(legUID, v.exchangeName, contract.ContractID, contract.ContractSize, numContracts, sel.MinPremium, step, interval)
	if err != nil {
		return fmt.Errorf("could not create attempt for %s: %w", contract.ContractID, err)
	}
	if v.legHook != nil {
		v.legHook(leg)
	}
	product, err := v.optEx.OpenOptionsProduct(ctx, contract.ContractID)
	if err != nil {
		return fmt.Errorf("could not open option product %s: %w", contract.ContractID, err)
	}

	prevContract := v.contract
	v.legIDs = append(v.legIDs, legUID)
	v.contract = contract
	if err := kv.WithReadWriter(ctx, v.db, func(ctx context.Context, rw kv.ReadWriter) error {
		if err := leg.Save(ctx, rw); err != nil {
			return err
		}
		return v.Save(ctx, rw)
	}); err != nil {
		v.legIDs = v.legIDs[:len(v.legIDs)-1]
		v.contract = prevContract
		product.Close()
		return fmt.Errorf("could not save optpos %s ahead of a new attempt: %w", v.uid, err)
	}

	v.closeProduct()
	v.leg, v.product = leg, product
	slog.Info("starting sell-to-open attempt", "optpos", v, "leg", legUID, "contract", contract.ContractID, "contracts", numContracts, "min-premium", sel.MinPremium)
	return v.start(ctx, fctx)
}

// start runs the current leg in its own goroutine until it fills or fctx is
// canceled.
func (v *Position) start(ctx context.Context, fctx context.Context) error {
	if v.product == nil {
		product, err := v.optEx.OpenOptionsProduct(ctx, v.leg.ContractID())
		if err != nil {
			return fmt.Errorf("could not open option product %s: %w", v.leg.ContractID(), err)
		}
		v.product = product
	}
	runCtx, cancel := context.WithCancelCause(fctx)
	a := &attempt{ctx: runCtx, cancel: cancel, done: make(chan struct{})}
	leg, product, optEx, db := v.leg, v.product, v.optEx, v.db
	a.job = job.Run(func(ctx context.Context) error {
		defer close(a.done)
		a.err = leg.Run(ctx, optEx, product, db)
		return a.err
	}, runCtx)
	v.active = a
	return nil
}

// reap clears an attempt that has returned on its own: filled, or failed
// (Check starts it again).
func (v *Position) reap() {
	a := v.active
	if a == nil {
		return
	}
	select {
	case <-a.done:
	default:
		return
	}
	v.active = nil
	a.cancel(errStopped)
	if a.err != nil && !errors.Is(a.err, context.Cause(a.ctx)) {
		v.retryAt = v.now().Add(retryDelay)
		slog.Warn("sell-to-open attempt returned an error (will restart)", "optpos", v, "leg", v.leg.UID(), "err", a.err, "retry-at", v.retryAt)
	}
	if v.leg.IsDone() {
		v.closeProduct()
	}
}

// stop stops the current attempt and waits until its live order, if any,
// is canceled and confirmed. An attempt not running since a restart is run
// just long enough to recover and cancel its orders.
func (v *Position) stop(ctx context.Context) error {
	if v.leg == nil {
		return nil
	}
	if v.active == nil {
		if v.leg.IsDone() {
			v.closeProduct()
			return nil
		}
		if err := v.start(ctx, context.Background()); err != nil {
			return err
		}
	}
	a := v.active
	a.cancel(errStopped)
	if err := a.job.Wait(ctx); err != nil {
		return fmt.Errorf("could not wait for attempt %s to stop: %w", v.leg.UID(), err)
	}
	v.active = nil
	v.closeProduct()
	// A canceled leg returns its context's cause once its order is confirmed
	// done; anything else means the order may still be live.
	if a.err != nil && !errors.Is(a.err, context.Cause(a.ctx)) {
		return fmt.Errorf("could not stop attempt %s: %w", v.leg.UID(), a.err)
	}
	return nil
}

func (v *Position) closeProduct() {
	if v.product == nil {
		return
	}
	if err := v.product.Close(); err != nil {
		slog.Warn("could not close option product (ignored)", "optpos", v, "contract", v.product.ContractID(), "err", err)
	}
	v.product = nil
}

// checkSettlement asks the broker how the contract stands and records an
// assignment or expiration. Errors fetching it are logged; the next check
// is an hour later.
func (v *Position) checkSettlement(ctx context.Context, now time.Time) (bool, error) {
	contractID := v.leg.ContractID()
	s, err := v.optEx.GetOptionsSettlement(ctx, contractID)
	if err != nil {
		slog.Warn("could not fetch option settlement (will retry)", "optpos", v, "contract", contractID, "err", err)
		return false, nil
	}
	if s.Status != "assigned" && s.Status != "expired" {
		return false, nil
	}
	// Nothing more can sell once settled; make sure no order is left live.
	if err := v.stop(ctx); err != nil {
		return false, err
	}
	at := s.At
	if at.IsZero() {
		at = now
	}
	var fact *gobs.AssignmentFact
	if s.Status == "assigned" {
		contracts := s.Contracts
		if !contracts.IsPositive() {
			contracts = v.leg.FilledSize()
		}
		shares := contracts.Mul(v.leg.ContractSize())
		if v.contract.OptionType == "CALL" {
			shares = shares.Neg()
		}
		key := s.Key
		if key == "" {
			key = contractID + "@" + v.contract.Expiry.Format(time.DateOnly)
		}
		fact = &gobs.AssignmentFact{Key: key, Shares: shares, Price: v.contract.Strike}
	}
	if err := v.finish(ctx, s.Status, at, fact); err != nil {
		return false, err
	}
	slog.Info("option position settled", "optpos", v, "contract", contractID, "outcome", s.Status, "key", s.Key, "at", at)
	return true, nil
}

// finish sets the terminal outcome and saves it in one transaction.
func (v *Position) finish(ctx context.Context, outcome string, at time.Time, fact *gobs.AssignmentFact) error {
	v.outcome, v.outcomeAt, v.assignment = outcome, at, fact
	if err := kv.WithReadWriter(ctx, v.db, v.Save); err != nil {
		v.outcome, v.outcomeAt, v.assignment = "", time.Time{}, nil
		return fmt.Errorf("could not save optpos %s outcome %q: %w", v.uid, outcome, err)
	}
	return nil
}

// Save writes Config (exchangeName, underlying) and Progress (contract,
// legIDs, outcome, assignment) as gobs.OptPositionStateV1.
func (v *Position) Save(ctx context.Context, rw kv.ReadWriter) error {
	gv := &gobs.OptPositionState{
		V1: &gobs.OptPositionStateV1{
			Config: &gobs.OptPositionConfig{
				ExchangeName: v.exchangeName,
				Underlying:   v.underlying,
			},
			Progress: &gobs.OptPositionProgress{
				Contract:   v.contract,
				Legs:       append([]string(nil), v.legIDs...),
				Outcome:    v.outcome,
				OutcomeAt:  v.outcomeAt,
				Assignment: v.assignment,
			},
		},
	}
	key := path.Join(DefaultKeyspace, v.uid)
	if err := kvutil.Set(ctx, rw, key, gv); err != nil {
		return fmt.Errorf("could not save optpos state: %w", err)
	}
	return nil
}

// Load restores a position. If it isn't terminal, its last attempt is
// loaded too; the next Check reattaches it, or opens the position if no
// attempt was saved.
func Load(ctx context.Context, uid string, r kv.Reader, selector ContractSelector, knobs *gobs.WheelKnobs, optEx exchange.OptionsExchange, db kv.Database) (*Position, error) {
	if len(uid) == 0 {
		return nil, fmt.Errorf("optpos uid is empty")
	}
	key := path.Join(DefaultKeyspace, uid)
	gv, err := kvutil.Get[gobs.OptPositionState](ctx, r, key)
	if err != nil {
		return nil, fmt.Errorf("could not load optpos state: %w", err)
	}
	if gv.V1 == nil || gv.V1.Config == nil || gv.V1.Progress == nil {
		return nil, fmt.Errorf("optpos state at %q is incomplete", key)
	}
	config, progress := gv.V1.Config, gv.V1.Progress
	v := New(uid, config.ExchangeName, config.Underlying, selector, knobs, optEx, db)
	v.contract = progress.Contract
	v.legIDs = append([]string(nil), progress.Legs...)
	v.outcome = progress.Outcome
	v.outcomeAt = progress.OutcomeAt
	v.assignment = progress.Assignment
	if err := v.check(); err != nil {
		return nil, err
	}

	if v.outcome == "" && len(v.legIDs) > 0 {
		legUID := v.legIDs[len(v.legIDs)-1]
		leg, err := optlimiter.Load(ctx, legUID, r)
		if err != nil {
			return nil, fmt.Errorf("could not load optpos %s attempt %s: %w", uid, legUID, err)
		}
		v.leg = leg
		// Contract is only a cache; the leg's own record is authoritative.
		if v.contract == nil || v.contract.ContractID != leg.ContractID() {
			contract, err := optEx.GetOptionsProduct(ctx, leg.ContractID())
			if err != nil {
				return nil, fmt.Errorf("could not fetch contract %s for optpos %s: %w", leg.ContractID(), uid, err)
			}
			v.contract = contract
		}
	}
	v.selectedFor = v.sessionKey(v.now())
	return v, nil
}

// HeldContractID reads, from saved records alone, the contract the position
// at uid is selling or holds: empty once it has ended or before its first
// attempt. It needs no broker, so a greeler can answer for its position
// before its Run has loaded it.
func HeldContractID(ctx context.Context, uid string, r kv.Reader) (string, error) {
	key := path.Join(DefaultKeyspace, uid)
	gv, err := kvutil.Get[gobs.OptPositionState](ctx, r, key)
	if err != nil {
		return "", fmt.Errorf("could not load optpos state: %w", err)
	}
	if gv.V1 == nil || gv.V1.Progress == nil {
		return "", fmt.Errorf("optpos state at %q is incomplete", key)
	}
	progress := gv.V1.Progress
	if progress.Outcome != "" || len(progress.Legs) == 0 {
		return "", nil
	}
	leg, err := optlimiter.Load(ctx, progress.Legs[len(progress.Legs)-1], r)
	if err != nil {
		return "", fmt.Errorf("could not load optpos %s attempt: %w", uid, err)
	}
	return leg.ContractID(), nil
}
