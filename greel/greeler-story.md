# Story: `greeler` — per-level posture derivation, mode flips, assignment bookkeeping

Companion to [project.md](project.md), [gobs-story.md](gobs-story.md),
[optlimiter-story.md](optlimiter-story.md), and
[optpos-story.md](optpos-story.md). Fourth stage of the dependency order
(`gobs/greel.go → optlimiter → optpos → greeler → greelladder`) and the
module every earlier decision was made to serve: it owns the levels, drives
the mode flip, and is the one caller of `optpos.Position`.

This story also settles optpos-story.md's one remaining open question
(does `Position.Check` need spot info) and project.md's long-deferred open
item 9 (the deterministic share-allocation rule for CSP/CC assignment).

---

## Scenario walkthrough

### 1. Grid-mode derivation: the same fold as `Looper`, generalized

Per level *i*, the posture (bought/sold, next action) is derived by folding
`FilledSize()` over every limiter in that level's history — exactly
`Looper.Run`'s `bought`/`sold`/`action` derivation
(`looper/run.go:87-125`), with one difference: a level's limiter history
isn't one flat list, it's `LevelLimiterIDs[i]` concatenated across every
grid epoch in `Epochs`, in order (gobs-story.md scenario 2). The fold
itself — sum filled sizes, compare against `Pair.Buy.Size`/`Pair.Sell.Size`,
decide BUY/SELL/STOP — is unchanged from `Looper`'s; only *where the
history comes from* differs.

This derivation runs **per level**, independently — `greeler.Run`'s grid-
mode loop is `for i, level := range GridLevels { deriveAndAct(i, level) }`,
each iteration deciding whether that level needs a new `limiter.Limiter`
created, same as `Looper` decides BUY vs SELL vs STOP.

### 2. Mode-flip mechanics, tied to `Epochs`

project.md already specifies the sequence; this scenario is about which
persisted pieces each step touches:

**Flip to wheel (grid → wheel):**
1. Cancel all levels' live limiters (each level's derivation this iteration
   naturally converges to "no order should be live" once the dwell clock —
   `Epochs[last].PendingFlip`/`PendingFlipAt`, gobs-story.md decision #8 —
   has fired; cancellation reuses each `limiter.Limiter`'s own idempotent
   cancel, nothing new).
2. Once no live limiters remain (await confirmations — an ordinary
   iteration-to-iteration wait, not a blocking call), verify qualification:
   all-cash (CSP) or all-shares (CC) — the all-or-nothing rule from
   gobs-story.md/project.md, checked by folding the same per-level history
   from scenario 1 across *every* level at once.
3. Call `optpos.New(...).Open(ctx, fctx, constraint)` — `constraint` is
   built from `GridLevels` per optpos-story.md scenario 1.
4. **Append the wheel epoch**: `{Mode: "wheel", StartAt: now, PositionID:
   position.UID()}` to `Epochs`. This is the "journaled in greeler state"
   project.md's mode-flip section names — concretely, it's this one
   append, in the greeler's own `Save`.

**Flip to grid (wheel → grid):**
1. `greeler.Run` calls `optpos.Position.Check(ctx, fctx)` every iteration
   while the last epoch is `"wheel"` and its position isn't terminal
   (optpos-story.md scenario 2) — **unconditionally**, not gated on spot's
   zone. This is the answer to optpos-story.md's open question: `Check`
   needs no spot/zone information because the greeler never force-closes a
   position on a zone change — project.md is explicit that "existing
   orders/positions drift through untouched" in the buffer, and the same
   principle extends past the buffer into wheel mode itself. A position
   only becomes terminal when `RollPolicy` decides to close it, it
   expires, or it's assigned — never because spot re-entered the grid
   band. `greeler` just keeps calling `Check`; the flip back to grid is a
   *consequence* of the position going terminal, not something `greeler`
   commands.
2. Once `Position`'s `Outcome` is non-empty (observed on load, or via
   `Check`'s own return), `greeler` derives post-assignment posture
   (scenario 3) and creates the levels' limiters per that posture.
3. **Append the grid epoch**: `{Mode: "grid", StartAt: now}` to `Epochs`.

Both directions are crash-safe by the same argument gobs-story.md already
made: nothing here is a phase pointer. A crash before step 3 in either
direction just means the greeler re-derives the same desired state next
iteration and re-issues the same idempotent calls; the `Epochs` append is
a journal entry appended *after* the transition is already real (design
principle 2, gobs-story.md decision #5's "closed epochs are frozen, only
the last is live" invariant).

### 3. The allocation rule — decided, closing project.md open item 9

Both CSP and CC assignment need to attribute a 100-share delta to specific
`GridLevels` indices. **One rule serves both directions**: `GridLevels` is
stored in ascending-price order (an invariant established at greeler
creation, gobs-story.md's `GridLevels []*Pair` is never reordered) —
allocate **lowest level first**, filling each level's full size before
moving to the next, until the 100 shares are exhausted.

- **CC assigned** (100 shares leave at strike K): this *is* project.md's
  already-proposed rule ("lowest-level-first") — the lowest levels are
  where the earliest, lowest-cost-basis inventory sits, so crediting the
  sale to them first is the natural FIFO reading of "which shares left."
- **CSP assigned** (100 shares arrive at strike K): the same rule, same
  direction — the new sell limiters get created starting at the lowest
  `GridLevels` index above spot, filling each level's size before the
  next. This wasn't previously decided (project.md flagged it as a
  separate TODO); using the identical rule for both directions means
  there's exactly one allocation function, parameterized by direction
  (deliver vs. depart), not two separate policies to keep in sync — and it
  biases toward the levels closest to spot/strike getting filled first,
  which maximizes how quickly that capital cycles back through a grid
  buy/sell round-trip.

This allocation is **never stored** — consistent with gobs-story.md's
derivation-over-state stance throughout: it's applied fresh, by this same
rule, every time `greeler` derives which level should own the next sell
limiter (post-CSP-assignment) or which level's inventory a CC sale
satisfies. Nothing about the rule depends on history beyond what's already
in `GridLevels` (static) and each level's own current derived inventory
(scenario 1) — nothing new to persist.

### 4. `SetOption`: freeze and retire, per-greeler

project.md open item 6 names `freeze=grid|wheel|all` and retire semantics.
Following `Looper`'s existing pattern (`freezeBuysOpt`/`freezeSellsOpt`/
`retireOpt`, parsed from `Options` in `Load`, mirrored in `greeler/
options.go`):

- `freeze=grid`: scenario 1's per-level loop stops creating new limiters
  (existing ones still resolve normally) — the greeler can still flip to
  wheel mode if dwell/hysteresis fire, since that's not "grid activity."
- `freeze=wheel`: scenario 2's flip-to-wheel step never fires (dwell clock
  still accumulates and gets journaled — `PendingFlip`/`PendingFlipAt`
  aren't gated by freeze, only the resulting action is); an *already open*
  position keeps running its own lifecycle (`Check` still gets called —
  freezing new wheel entries isn't the same as abandoning an open one).
- `freeze=all`: both, plus the greeler simply stops iterating meaningfully
  — matches `Looper`'s `freezeBuysOpt && freezeSellsOpt` combination
  today.
- retire: like `Looper`'s `retireOpt` — no new grid cycles start and no
  new wheel entries open, but existing limiters/positions finish naturally.

None of this needs new persisted fields — `Options map[string]string` on
`GreelerStateV1` already carries it (gobs-story.md), read the same way
`Limiter.Load`/`Looper` already read theirs.

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
    "context"

    "github.com/bvk/tradebot/exchange"
    "github.com/bvk/tradebot/gobs"
    "github.com/bvk/tradebot/limiter"
    "github.com/bvk/tradebot/optpos"
    "github.com/bvk/tradebot/point"
    "github.com/bvk/tradebot/trader"
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

// Greeler runs one greel: derives per-level posture from spot, fills, and
// recorded events. Job (trader.Trader), not a component — registered like
// Limiter/Looper/Waller.
type Greeler struct {
    uid          string
    exchangeName string
    productID    string

    gridLevels []*point.Point // Pair, actually — Buy/Sell per level

    gridPct, farPct, hysteresisPct decimal.Decimal
    dwellTime                       int64 // time.Duration

    epochs []*Epoch // mirrors gobs.GreelEpoch; last entry is current

    selector optpos.ContractSelector
    policy   optpos.RollPolicy

    freezeGridOpt, freezeWheelOpt, retireOpt bool
}

// Epoch mirrors gobs.GreelEpoch as an in-memory working type; Save/Load
// convert to/from the persisted shape directly (gobs-story.md).
type Epoch struct {
    Mode       string
    PositionID string
    Position   *optpos.Position // loaded lazily, nil until needed

    LevelLimiters [][]*limiter.Limiter // loaded lazily per level
}

func (v *Greeler) Run(ctx context.Context, rt *trader.Runtime) error {
    panic("unimplemented")
    // Loop:
    //   last := v.epochs[len(v.epochs)-1]
    //   switch last.Mode {
    //   case "grid": v.runGrid(ctx, rt, last)   // scenario 1
    //   case "wheel": v.runWheel(ctx, rt, last) // scenario 2
    //   }
}

// DerivedStock is the greeler's total current stock inventory — scenario
// 1's per-level fold, summed across all levels. Exported so greelladder's
// positions-poll fallback reconciliation (greelladder-story.md scenario 2)
// can compare it against the account's actual stock holding, without
// reaching into greeler's own derivation internals.
func (v *Greeler) DerivedStock() decimal.Decimal {
    panic("unimplemented")
}

// allocate implements scenario 3's rule: lowest GridLevels index first,
// filling each level's size before the next, until shares is exhausted.
// Used both for CSP-assigned sell-limiter placement and CC-assigned sale
// attribution — direction only changes what the caller does with each
// (level index, size) pair this returns, not the rule itself.
func (v *Greeler) allocate(shares decimal.Decimal) []struct {
    LevelIndex int
    Size       decimal.Decimal
} {
    panic("unimplemented")
}

func (v *Greeler) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented")
}

func Load(ctx context.Context, uid string, r kv.Reader, optEx exchange.OptionsExchange, selector optpos.ContractSelector, policy optpos.RollPolicy) (*Greeler, error) {
    panic("unimplemented")
}

var _ trader.Trader = (*Greeler)(nil)
```

---

## Decisions made at this checkpoint

1. **`Epoch.Position`/`LevelLimiters` load lazily, on demand — decided.**
   Not eagerly on `Load`: a greeler can accumulate many historical epochs
   over its life, and eagerly loading every past position and every past
   limiter on every restart would scale with that whole history just to
   start running. Matches the "fold when needed, not eagerly" spirit
   already established elsewhere (e.g. gobs-story.md never materializes a
   level's full lifetime history unless something actually asks for it).
2. **Qualification check reuses scenario 1's per-level fold directly, no
   separate derivation function — decided.** Confirming all-cash/all-shares
   before opening a position is the same fold scenario 1 already computes
   per level for order-placement decisions, just read across every level
   at once rather than acted on individually. One fold, two call sites,
   not two folds to keep in sync.
3. **`greelladder` owns cross-greeler risk gates — confirmed.** Max open
   contracts, max assignment exposure (project.md open item 6) sit above
   `greeler`, at the ladder level. `greeler` itself enforces only its own
   freeze/retire (scenario 4) — no cross-greeler awareness. This is now a
   settled boundary for the `greelladder` story to build on, not something
   it needs to re-derive.
4. **`DerivedStock()` exported — added for `greelladder`.** Surfaced by
   `greelladder-story.md` scenario 2's fallback reconciliation, which needs
   to compare a greeler's derived stock inventory against the account's
   actual holding. Rather than have `greelladder` reach into `greeler`'s
   internals, `greeler` exports the scenario 1 fold's sum directly (see the
   skeleton). No new persisted state — this is a read of an already-derived
   value.
