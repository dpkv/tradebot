// Copyright (c) 2026 Deepak Vankadaru

// Package greeler runs one greel: a band of grid levels traded through stock
// limiters while spot is near, and one written option over the same levels
// while spot is far.
package greeler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/limiter"
	"github.com/bvk/tradebot/optpos"
	"github.com/bvk/tradebot/point"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const DefaultKeyspace = "/greelers/"

// contractShares is the shares one contract covers; a greeler's levels must
// add up to at least this much.
var contractShares = decimal.NewFromInt(100)

// position is what the greeler needs from an optpos.Position.
type position interface {
	UID() string
	Outcome() string
	Assignment() *gobs.AssignmentFact
	Contract() *gobs.OptionContract
	Open(ctx, fctx context.Context, c *optpos.Constraint) error
	Check(ctx, fctx context.Context, c *optpos.Constraint) error
	Abandon(ctx context.Context) error
	Stop(ctx context.Context) error
	Save(ctx context.Context, rw kv.ReadWriter) error
}

// epoch is a gobs.GreelEpoch with its children once Run has loaded them.
type epoch struct {
	gobs.GreelEpoch

	position position             // wheel epochs; nil until loaded
	limiters [][]*limiter.Limiter // grid epochs; index-aligned with LevelLimiterIDs
}

// Greeler runs one greel: it derives per-level posture from spot, limiter
// fills, and the outcomes its positions report. A job (trader.Trader), so
// it runs standalone; under a ladder, the ladder drives it.
type Greeler struct {
	runtimeLock sync.Mutex

	uid string
	cfg *gobs.GreelConfig

	levels []*point.Pair // cfg.GridLevels, ascending price order

	selector optpos.ContractSelector // rebuilt from cfg in New/Load

	// mu guards epochs and the children in them against readers (Actions,
	// GetSummary, DerivedStock) while Run appends. Only Run writes.
	mu       sync.Mutex
	epochs   []*epoch // last entry is current
	loaded   bool     // children of every epoch are loaded
	holdings []decimal.Decimal

	// held is the contract the current position sells or holds, for the
	// ladder's sibling exclusion; heldKnown is set once New or Load has
	// worked it out.
	held      string
	heldKnown bool

	// exclude is set by the ladder; nil standalone.
	exclude func(contractID string) bool

	freezeGridOpt, freezeWheelOpt, retireOpt bool

	// opened is true once Abandon reported the current position's opening
	// order filled; in memory only.
	opened bool

	// Overridable in tests.
	now          func() time.Time
	interval     time.Duration
	newPosition  func(uid string, optEx exchange.OptionsExchange, db kv.Database) position
	loadPosition func(ctx context.Context, uid string, r kv.Reader, optEx exchange.OptionsExchange, db kv.Database) (position, error)
}

var _ trader.Trader = &Greeler{}

// New creates a greeler born inside a grid epoch. The config is copied.
func New(uid string, cfg *gobs.GreelConfig) (*Greeler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("greeler config is nil")
	}
	v, err := newGreeler(uid, cfg)
	if err != nil {
		return nil, err
	}
	v.epochs = []*epoch{v.newGridEpoch(v.now())}
	v.loaded = true
	v.heldKnown = true
	return v, nil
}

func newGreeler(uid string, cfg *gobs.GreelConfig) (*Greeler, error) {
	dup := *cfg
	dup.GridLevels = nil
	for i, p := range cfg.GridLevels {
		if p == nil {
			return nil, fmt.Errorf("greeler grid level %d is nil: %w", i, os.ErrInvalid)
		}
		pp := *p
		dup.GridLevels = append(dup.GridLevels, &pp)
	}
	if cfg.WheelKnobs != nil {
		knobs := *cfg.WheelKnobs
		dup.WheelKnobs = &knobs
	}
	v := &Greeler{uid: uid, cfg: &dup}
	for _, p := range dup.GridLevels {
		v.levels = append(v.levels, point.NewPairFromGobPair(p))
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	selector, err := optpos.NewSelector(dup.ContractSelector, dup.WheelKnobs)
	if err != nil {
		return nil, err
	}
	v.selector = selector
	v.setDefaults()
	return v, nil
}

func (v *Greeler) setDefaults() {
	v.now = time.Now
	v.interval = 10 * time.Second
	v.newPosition = func(uid string, optEx exchange.OptionsExchange, db kv.Database) position {
		return optpos.New(uid, v.cfg.ExchangeName, v.cfg.ProductID, v.selector, v.cfg.WheelKnobs, optEx, db)
	}
	v.loadPosition = func(ctx context.Context, uid string, r kv.Reader, optEx exchange.OptionsExchange, db kv.Database) (position, error) {
		return optpos.Load(ctx, uid, r, v.selector, v.cfg.WheelKnobs, optEx, db)
	}
}

func (v *Greeler) check() error {
	if err := checkUID(v.uid); err != nil {
		return err
	}
	if v.cfg.ProductID == "" || v.cfg.ExchangeName == "" {
		return fmt.Errorf("greeler product or exchange name is empty")
	}
	if len(v.levels) == 0 {
		return fmt.Errorf("greeler has no grid levels")
	}
	var total decimal.Decimal
	for i, p := range v.levels {
		if err := p.Check(); err != nil {
			return fmt.Errorf("grid level %d is invalid: %w", i, err)
		}
		// A level is either all cash or all shares between cycles, which is
		// what all-or-nothing qualification counts on.
		if !p.Sell.Size.Equal(p.Buy.Size) {
			return fmt.Errorf("grid level %d sell size %s differs from buy size %s", i, p.Sell.Size, p.Buy.Size)
		}
		if i > 0 && !p.Buy.Price.GreaterThan(v.levels[i-1].Buy.Price) {
			return fmt.Errorf("grid level %d is not above level %d", i, i-1)
		}
		total = total.Add(p.Buy.Size)
	}
	if total.LessThan(contractShares) {
		return fmt.Errorf("grid levels hold %s shares, want at least %s for one contract", total, contractShares)
	}
	g, f, h := v.cfg.GridPct, v.cfg.FarPct, v.cfg.HysteresisPct
	if !g.IsPositive() || !f.GreaterThan(g) {
		return fmt.Errorf("zone percentages need 0 < grid (%s) < far (%s)", g, f)
	}
	if h.IsNegative() || !h.LessThan(f) {
		return fmt.Errorf("hysteresis %s must be within [0, far %s)", h, f)
	}
	if v.cfg.DwellTime < 0 {
		return fmt.Errorf("dwell time %s is negative", v.cfg.DwellTime)
	}
	return nil
}

func (v *Greeler) newGridEpoch(now time.Time) *epoch {
	return &epoch{
		GreelEpoch: gobs.GreelEpoch{
			Mode:            "grid",
			StartAt:         now,
			LevelLimiterIDs: make([][]string, len(v.levels)),
		},
		limiters: make([][]*limiter.Limiter, len(v.levels)),
	}
}

func (v *Greeler) String() string {
	return "greeler:" + v.uid
}

func (v *Greeler) LogValue() slog.Value {
	return slog.StringValue(v.uid)
}

func (v *Greeler) UID() string { return v.uid }

func (v *Greeler) ProductID() string { return v.cfg.ProductID }

func (v *Greeler) ExchangeName() string { return v.cfg.ExchangeName }

// Mode is the current epoch's mode, "grid" or "wheel".
func (v *Greeler) Mode() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.epochs[len(v.epochs)-1].Mode
}

// SetExclude installs the ladder's sibling contract exclusion. Call it
// only while the greeler isn't running.
func (v *Greeler) SetExclude(exclude func(contractID string) bool) {
	v.exclude = exclude
}

// HeldContract is the contract ID the current position is selling or
// holds, empty when there is none. known is false until New or Load has
// worked it out, so the answer can't be trusted yet.
func (v *Greeler) HeldContract() (id string, known bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.held, v.heldKnown
}

// updateHeld records pos's contract as held while pos hasn't ended.
func (v *Greeler) updateHeld(pos position) {
	held := ""
	if pos != nil && pos.Outcome() == "" {
		if c := pos.Contract(); c != nil {
			held = c.ContractID
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.held, v.heldKnown = held, true
}

// DerivedStock is the greeler's stock inventory as of Run's last
// derivation: every level's holding, summed. ok is false before Run first
// derives it.
func (v *Greeler) DerivedStock() (sum decimal.Decimal, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, h := range v.holdings {
		sum = sum.Add(h)
	}
	return sum, v.holdings != nil
}

// BudgetAt is the cash every level needs to hold its buy at once.
func (v *Greeler) BudgetAt(feePct decimal.Decimal) decimal.Decimal {
	var sum decimal.Decimal
	for _, p := range v.levels {
		sum = sum.Add(p.Buy.Value()).Add(p.Buy.FeeAt(feePct))
	}
	return sum
}

// levelLimiters returns every loaded limiter per level, oldest first.
func (v *Greeler) levelLimiters() [][]*limiter.Limiter {
	v.mu.Lock()
	defer v.mu.Unlock()
	all := make([][]*limiter.Limiter, len(v.levels))
	for _, e := range v.epochs {
		for i, ls := range e.limiters {
			all[i] = append(all[i], ls...)
		}
	}
	return all
}

// Actions returns the stock limiters' filled orders, paired per level.
// Option premium and assignments are left out until the accounting model
// lands.
func (v *Greeler) Actions() []*gobs.Action {
	var actions []*gobs.Action
	for i, ls := range v.levelLimiters() {
		for _, l := range ls {
			if as := l.Actions(); len(as) > 0 {
				as[0].PairingKey = fmt.Sprintf("%s/level-%03d", v.uid, i)
				actions = append(actions, as[0])
			}
		}
	}
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Orders[0].CreateTime.Time.Before(actions[j].Orders[0].CreateTime.Time)
	})
	if len(actions) == 0 {
		return nil
	}
	return actions
}

// GetSummary sums the stock limiters' fills. Shares bought but not yet
// sold count as unsold at the level's buy price, and shares sold beyond
// those bought (assigned shares) as oversold at its sell price. Option
// premium and assignment cost are left out until the accounting model
// lands.
//
// Within a time range, each sell that sold is paired with the level's
// oldest unpaired buy, counted in full even if it filled before the range
// (as Looper.GetSummary does), so a buy last month and its sell this month
// don't show as shares oversold this month.
func (v *Greeler) GetSummary(r *timerange.Range) *gobs.Summary {
	s := &gobs.Summary{
		Exchange:  v.cfg.ExchangeName,
		ProductID: v.cfg.ProductID,
		Budget:    v.BudgetAt(decimal.Zero),
	}
	for i, ls := range v.levelLimiters() {
		var bought, sold decimal.Decimal
		addBuy := func(l *limiter.Limiter, r *timerange.Range) {
			bs := l.GetSummary(r)
			s.Add(bs)
			bought = bought.Add(bs.BoughtSize)
		}
		var unpaired []*limiter.Limiter
		for _, l := range ls {
			if l.IsBuy() {
				unpaired = append(unpaired, l)
				continue
			}
			ss := l.GetSummary(r)
			s.Add(ss)
			sold = sold.Add(ss.SoldSize)
			if len(unpaired) > 0 {
				buy := unpaired[0]
				unpaired = unpaired[1:]
				if ss.SoldSize.IsZero() {
					addBuy(buy, r)
				} else {
					addBuy(buy, nil)
				}
			}
		}
		for _, l := range unpaired {
			addBuy(l, r)
		}
		switch net := bought.Sub(sold); {
		case net.IsPositive():
			s.UnsoldSize = s.UnsoldSize.Add(net)
			s.UnsoldValue = s.UnsoldValue.Add(net.Mul(v.levels[i].Buy.Price))
		case net.IsNegative():
			s.OversoldSize = s.OversoldSize.Add(net.Neg())
			s.OversoldValue = s.OversoldValue.Add(net.Neg().Mul(v.levels[i].Sell.Price))
		}
	}
	return s
}

// Save writes the greeler record only; children save themselves.
func (v *Greeler) Save(ctx context.Context, rw kv.ReadWriter) error {
	v.mu.Lock()
	epochs := make([]*gobs.GreelEpoch, 0, len(v.epochs))
	for _, e := range v.epochs {
		ge := e.GreelEpoch
		ge.LevelLimiterIDs = nil
		if e.LevelLimiterIDs != nil {
			ge.LevelLimiterIDs = make([][]string, len(e.LevelLimiterIDs))
			for i, ids := range e.LevelLimiterIDs {
				ge.LevelLimiterIDs[i] = append([]string(nil), ids...)
			}
		}
		epochs = append(epochs, &ge)
	}
	v.mu.Unlock()

	gv := &gobs.GreelerState{
		V1: &gobs.GreelerStateV1{
			Options:  v.options(),
			Config:   v.cfg,
			Progress: &gobs.GreelProgress{Epochs: epochs},
		},
	}
	key := path.Join(DefaultKeyspace, v.uid)
	if err := kvutil.Set(ctx, rw, key, gv); err != nil {
		return fmt.Errorf("could not save greeler state: %w", err)
	}
	return nil
}

func checkUID(uid string) error {
	fs := strings.Split(uid, "/")
	if _, err := uuid.Parse(fs[0]); err != nil {
		return fmt.Errorf("greeler uid %q doesn't start with an uuid: %w", uid, err)
	}
	return nil
}

// Load rebuilds a greeler from its record alone, the selector included.
// Children load when Run starts, which has the options exchange.
func Load(ctx context.Context, uid string, r kv.Reader) (*Greeler, error) {
	if err := checkUID(uid); err != nil {
		return nil, err
	}
	key := path.Join(DefaultKeyspace, uid)
	gv, err := kvutil.Get[gobs.GreelerState](ctx, r, key)
	if err != nil {
		return nil, fmt.Errorf("could not load greeler state: %w", err)
	}
	if gv.V1 == nil || gv.V1.Config == nil || gv.V1.Progress == nil || len(gv.V1.Progress.Epochs) == 0 {
		return nil, fmt.Errorf("greeler state at %q is incomplete", key)
	}
	v, err := newGreeler(uid, gv.V1.Config)
	if err != nil {
		return nil, err
	}
	for i, ge := range gv.V1.Progress.Epochs {
		e := &epoch{GreelEpoch: *ge}
		switch e.Mode {
		case "grid":
			if len(e.LevelLimiterIDs) != len(v.levels) {
				return nil, fmt.Errorf("greeler epoch %d has %d levels, want %d", i, len(e.LevelLimiterIDs), len(v.levels))
			}
			// Loaded here, not in Run, so a greeler that isn't running still
			// reports its fills.
			e.limiters = make([][]*limiter.Limiter, len(e.LevelLimiterIDs))
			for li, ids := range e.LevelLimiterIDs {
				for _, id := range ids {
					l, err := limiter.Load(ctx, id, r)
					if err != nil {
						return nil, fmt.Errorf("could not load greeler %s limiter %s: %w", uid, id, err)
					}
					e.limiters[li] = append(e.limiters[li], l)
				}
			}
		case "wheel":
			if e.PositionID == "" {
				return nil, fmt.Errorf("greeler wheel epoch %d has no position", i)
			}
		default:
			return nil, fmt.Errorf("greeler epoch %d has invalid mode %q", i, e.Mode)
		}
		v.epochs = append(v.epochs, e)
	}
	for opt, val := range gv.V1.Options {
		if _, err := v.SetOption(opt, val); err != nil {
			return nil, fmt.Errorf("could not set greeler option (%s=%q): %w", opt, val, err)
		}
	}
	// Siblings ask for the held contract before Run has loaded the position
	// (and even if Run fails first), so read it from the saved records.
	if last := v.epochs[len(v.epochs)-1]; last.Mode == "wheel" {
		held, err := optpos.HeldContractID(ctx, last.PositionID, r)
		if err != nil {
			return nil, fmt.Errorf("could not read greeler %s position %s: %w", uid, last.PositionID, err)
		}
		v.held = held
	}
	v.heldKnown = true
	return v, nil
}
