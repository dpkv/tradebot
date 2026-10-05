// Copyright (c) 2026 Deepak Vankadaru

// Package greelladder runs a ladder of greelers across price bands: it
// spawns and drives them (as waller does loopers), keeps siblings off the
// same option contract, and cross-checks their stock against the account.
package greelladder

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/greeler"
	"github.com/bvk/tradebot/kvutil"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvk/tradebot/trader"
	"github.com/bvkgo/kv"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const DefaultKeyspace = "/greelladders/"

// claimTTL is how long a greeler's latest selection keeps a contract from
// its siblings. It only needs to cover the moment between a selection and
// the greeler reporting the contract as held, which takes a save and an
// exchange call.
const claimTTL = time.Minute

// sibling is what exclusion needs from a greeler.
type sibling interface {
	UID() string
	HeldContract() (id string, known bool)
}

// claim is a greeler's latest selection, which may still be in flight.
type claim struct {
	contractID string
	at         time.Time
}

// GreelLadder spawns, drives, and aggregates greelers across price bands,
// keeps siblings off the same contract, and cross-checks stock movement.
type GreelLadder struct {
	runtimeLock sync.Mutex

	uid string
	cfg *gobs.GreelLadderConfig

	greelers []*greeler.Greeler

	// mu guards claims, so two siblings can't claim one contract at once.
	mu       sync.Mutex
	siblings []sibling
	claims   map[string]*claim // greeler UID -> its latest selection

	// Overridable in tests.
	now               func() time.Time
	reconcileInterval time.Duration
}

var _ trader.Trader = &GreelLadder{}

// New creates a ladder with one greeler per band, in band order. Each
// band's product and exchange must be empty or match the ladder's; bands
// are copied.
func New(uid, exchangeName, productID string, bands []*gobs.GreelConfig) (*GreelLadder, error) {
	if err := checkUID(uid); err != nil {
		return nil, err
	}
	if exchangeName == "" || productID == "" {
		return nil, fmt.Errorf("greel ladder product or exchange name is empty")
	}
	if len(bands) == 0 {
		return nil, fmt.Errorf("greel ladder has no bands")
	}
	cfg := &gobs.GreelLadderConfig{ProductID: productID, ExchangeName: exchangeName}
	var greelers []*greeler.Greeler
	for i, band := range bands {
		if band == nil {
			return nil, fmt.Errorf("greel ladder band %d is nil", i)
		}
		b := *band
		if b.ProductID == "" {
			b.ProductID = productID
		}
		if b.ExchangeName == "" {
			b.ExchangeName = exchangeName
		}
		gid := path.Join(uid, fmt.Sprintf("greeler-%06d", i))
		g, err := greeler.New(gid, &b)
		if err != nil {
			return nil, fmt.Errorf("greel ladder band %d: %w", i, err)
		}
		greelers = append(greelers, g)
		cfg.GreelerIDs = append(cfg.GreelerIDs, gid)
	}
	return newLadder(uid, cfg, greelers)
}

func newLadder(uid string, cfg *gobs.GreelLadderConfig, greelers []*greeler.Greeler) (*GreelLadder, error) {
	v := &GreelLadder{
		uid:               uid,
		cfg:               cfg,
		greelers:          greelers,
		claims:            make(map[string]*claim),
		now:               time.Now,
		reconcileInterval: time.Hour,
	}
	if err := v.check(); err != nil {
		return nil, err
	}
	for _, g := range greelers {
		v.siblings = append(v.siblings, g)
		g.SetExclude(v.excludeFor(g.UID()))
	}
	return v, nil
}

func (v *GreelLadder) check() error {
	if len(v.greelers) != len(v.cfg.GreelerIDs) {
		return fmt.Errorf("greel ladder has %d greelers, want %d", len(v.greelers), len(v.cfg.GreelerIDs))
	}
	seen := make(map[string]bool)
	for i, g := range v.greelers {
		if g.UID() != v.cfg.GreelerIDs[i] {
			return fmt.Errorf("greel ladder greeler %d is %s, want %s", i, g.UID(), v.cfg.GreelerIDs[i])
		}
		if seen[g.UID()] {
			return fmt.Errorf("greel ladder names greeler %s twice", g.UID())
		}
		seen[g.UID()] = true
		if g.ProductID() != v.cfg.ProductID || g.ExchangeName() != v.cfg.ExchangeName {
			return fmt.Errorf("greeler %s trades %s on %s, want %s on %s", g.UID(), g.ProductID(), g.ExchangeName(), v.cfg.ProductID, v.cfg.ExchangeName)
		}
	}
	return nil
}

// excludeFor is the exclusion hook for one greeler. The last contract a
// greeler asks about stays claimed for it: optpos checks the selected
// contract last, so that is the one it goes on to sell.
func (v *GreelLadder) excludeFor(uid string) func(contractID string) bool {
	return func(contractID string) bool {
		return v.exclude(uid, contractID)
	}
}

// exclude reports whether a sibling of uid holds or has just selected
// contractID; otherwise it claims contractID for uid. While a sibling
// hasn't loaded its position, nothing is safe and everything is excluded.
func (v *GreelLadder) exclude(uid, contractID string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	now := v.now()
	for _, s := range v.siblings {
		sid := s.UID()
		if sid == uid {
			continue
		}
		held, known := s.HeldContract()
		if !known || held == contractID {
			return true
		}
		if c, ok := v.claims[sid]; ok && c.contractID == contractID && now.Sub(c.at) < claimTTL {
			return true
		}
	}
	v.claims[uid] = &claim{contractID: contractID, at: now}
	return false
}

func (v *GreelLadder) String() string {
	return "greelladder:" + v.uid
}

func (v *GreelLadder) LogValue() slog.Value {
	return slog.StringValue(v.uid)
}

func (v *GreelLadder) UID() string { return v.uid }

func (v *GreelLadder) ProductID() string { return v.cfg.ProductID }

func (v *GreelLadder) ExchangeName() string { return v.cfg.ExchangeName }

// Greelers returns the ladder's greelers in band order.
func (v *GreelLadder) Greelers() []*greeler.Greeler {
	return append([]*greeler.Greeler(nil), v.greelers...)
}

// Actions returns every greeler's actions, oldest first.
func (v *GreelLadder) Actions() []*gobs.Action {
	var actions []*gobs.Action
	for _, g := range v.greelers {
		actions = append(actions, g.Actions()...)
	}
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Orders[0].CreateTime.Time.Before(actions[j].Orders[0].CreateTime.Time)
	})
	if len(actions) == 0 {
		return nil
	}
	return actions
}

// BudgetAt sums the greelers' budgets.
func (v *GreelLadder) BudgetAt(feePct decimal.Decimal) decimal.Decimal {
	var sum decimal.Decimal
	for _, g := range v.greelers {
		sum = sum.Add(g.BudgetAt(feePct))
	}
	return sum
}

// GetSummary sums the greelers' summaries, which leave option premium and
// assignment cost out until the accounting model lands.
func (v *GreelLadder) GetSummary(r *timerange.Range) *gobs.Summary {
	s := &gobs.Summary{
		Exchange:  v.cfg.ExchangeName,
		ProductID: v.cfg.ProductID,
	}
	for _, g := range v.greelers {
		s.Add(g.GetSummary(r))
	}
	return s
}

// SetOption sets retire or freeze on every greeler, as waller does on its
// loopers, rolling back on failure. The greelers keep the options in their
// own records.
func (v *GreelLadder) SetOption(opt, val string) (_ string, status error) {
	switch key := strings.ToLower(opt); key {
	case "retire", "freeze":
	default:
		return "", fmt.Errorf("invalid/unsupported greel ladder option %q", key)
	}

	undo := ""
	for i, g := range v.greelers {
		g := g
		u, err := g.SetOption(opt, val)
		if err != nil {
			return "", err
		}
		defer func() {
			if status != nil {
				if _, err := g.SetOption(opt, u); err != nil {
					slog.Error("could not undo set-option on greeler (needs manual fix)", "greeler", g, "opt", opt, "val", val, "err", err)
				}
			}
		}()
		if i == 0 {
			undo = u
		} else if u != undo {
			return "", fmt.Errorf("greelers disagree on how to undo %s=%s; set it per greeler", opt, val)
		}
	}
	return undo, nil
}

// Save writes every greeler's record and the ladder's. Run calls it only
// before the greelers start, so it never races their own saves.
func (v *GreelLadder) Save(ctx context.Context, rw kv.ReadWriter) error {
	for _, g := range v.greelers {
		if err := g.Save(ctx, rw); err != nil {
			return fmt.Errorf("could not save greeler %s: %w", g.UID(), err)
		}
	}
	cfg := *v.cfg
	cfg.GreelerIDs = append([]string(nil), v.cfg.GreelerIDs...)
	gv := &gobs.GreelLadderState{
		V1: &gobs.GreelLadderStateV1{
			Config:   &cfg,
			Progress: &gobs.GreelLadderProgress{},
		},
	}
	key := path.Join(DefaultKeyspace, v.uid)
	if err := kvutil.Set(ctx, rw, key, gv); err != nil {
		return fmt.Errorf("could not save greel ladder state: %w", err)
	}
	return nil
}

func checkUID(uid string) error {
	fs := strings.Split(uid, "/")
	if _, err := uuid.Parse(fs[0]); err != nil {
		return fmt.Errorf("greel ladder uid %q doesn't start with an uuid: %w", uid, err)
	}
	return nil
}

// Load rebuilds the ladder and its greelers from their records alone and
// hands each greeler its exclusion hook.
func Load(ctx context.Context, uid string, r kv.Reader) (*GreelLadder, error) {
	if err := checkUID(uid); err != nil {
		return nil, err
	}
	key := path.Join(DefaultKeyspace, uid)
	gv, err := kvutil.Get[gobs.GreelLadderState](ctx, r, key)
	if err != nil {
		return nil, fmt.Errorf("could not load greel ladder state: %w", err)
	}
	if gv.V1 == nil || gv.V1.Config == nil || len(gv.V1.Config.GreelerIDs) == 0 {
		return nil, fmt.Errorf("greel ladder state at %q is incomplete", key)
	}
	var greelers []*greeler.Greeler
	for _, id := range gv.V1.Config.GreelerIDs {
		g, err := greeler.Load(ctx, id, r)
		if err != nil {
			return nil, fmt.Errorf("could not load greel ladder greeler %s: %w", id, err)
		}
		greelers = append(greelers, g)
	}
	return newLadder(uid, gv.V1.Config, greelers)
}
