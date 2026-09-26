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

Per level *i*, the posture (bought/sold, next action) is derived the way
`Looper.Run` derives its `bought`/`sold`/`action` (`looper/run.go:87-125`),
with two differences:

- **Where the limiter history comes from:** not one flat list, but
  `LevelLimiterIDs[i]` concatenated across every grid epoch in `Epochs`, in
  order (gobs-story.md scenario 2).
- **A second source of shares:** assignments move shares into or out of a
  level with no limiter behind them (project.md decision #4: "merge two
  sources"). Looper's fold alone can't absorb that — a level that received
  assigned shares later has a sell with no matching buy, which trips
  Looper's STOP guard (`nbuys < nsells`, `looper/run.go:104`). So the fold
  is a **chronological replay over `Epochs`**: grid epochs contribute their
  limiter fills; each wheel epoch whose position was assigned contributes
  that assignment's per-level attribution (scenario 3) at the point the
  epoch ended.

The result is each level's current holdings and cycle position, from which
the greeler decides, per level independently, whether that level needs a
new `limiter.Limiter` — the same BUY/SELL/STOP decision `Looper` makes.

### 2. Mode-flip mechanics, tied to `Epochs`

project.md already specifies the sequence; this scenario is about which
persisted pieces each step touches:

**Flip to wheel (grid → wheel)**, once the dwell clock
(`Epochs[last].PendingFlip`/`PendingFlipAt`, gobs-story.md decision #8) has
fired:
1. Check qualification from scenario 1's fold: all-cash (CSP) or
   all-shares (CC) — the all-or-nothing rule. If it doesn't hold, don't
   start the flip.
2. Cancel all levels' live limiters (each `limiter.Limiter`'s own
   idempotent cancel) and wait for confirmations — an ordinary
   iteration-to-iteration wait, not a blocking call.
3. Re-check qualification. A fill can race the cancel and leave a level
   mixed; then **the flip is blocked**: the greeler stays in grid mode,
   its levels re-arm through ordinary grid derivation, and the dwell clock
   resets (so the flip can't retry every iteration and churn orders).
4. **Write-ahead:** in one transaction, append the wheel epoch
   `{Mode: "wheel", StartAt: now, PositionID: path.Join(greelerUID,
   "pos-%06d")}` and save the empty position record.
5. Call `position.Open(ctx, fctx, constraint)` — `constraint` is built
   from `GridLevels` per optpos-story.md scenario 1, plus the ladder's
   sibling exclusion if any.

**Flip to grid (wheel → grid):**
1. `greeler.Run` calls `optpos.Position.Check(ctx, fctx)` every iteration
   while the last epoch is `"wheel"` and its position isn't terminal
   (optpos-story.md scenario 2) — **unconditionally**, not gated on spot's
   zone. `Check` needs no spot/zone information because the greeler never
   force-closes a position on a zone change — project.md is explicit that
   "existing orders/positions drift through untouched" in the buffer, and
   the same principle extends past the buffer into wheel mode itself. A
   position only becomes terminal when `RollPolicy` closes it or the
   broker reports it assigned or expired (`Check`'s settlement step,
   optpos-story.md scenario 6) — never because spot re-entered the grid
   band. The flip back to grid is a *consequence* of the position going
   terminal, not something `greeler` commands.
2. Once the position's `Outcome` is non-empty, **append the grid epoch**
   `{Mode: "grid", StartAt: now}`.
3. Derive post-assignment posture (scenarios 1, 3) and create the levels'
   limiters in the new epoch — each limiter's UID appended to
   `LevelLimiterIDs[i]` and saved before it runs (write-ahead, the way
   `Looper.addNewBuy` saves before running).

Both directions are crash-safe because every child is named in persisted
state before it can place an order (gobs-story.md scenario 3a): restart
reads the mode from `Epochs[last]`, finds every child by UID, re-issues
idempotent calls, and converges. Nothing is ever created without a
reference, so nothing can be orphaned or duplicated.

### 3. The allocation rule — decided, closing project.md open item 9

Both CSP and CC assignment need to attribute a 100-share delta to specific
`GridLevels` indices. **One rule serves both directions**: `GridLevels` is
stored in ascending-price order (an invariant established at greeler
creation, gobs-story.md's `GridLevels []*Pair` is never reordered) —
attribute **lowest level first, over all levels**, filling each level's
full size before moving to the next, until the 100 shares are exhausted.

- **CC assigned** (100 shares leave at strike K): this *is* project.md's
  already-proposed rule ("lowest-level-first") — the lowest levels are
  where the earliest, lowest-cost-basis inventory sits, so crediting the
  sale to them first is the natural FIFO reading of "which shares left."
- **CSP assigned** (100 shares arrive at strike K): the same rule, same
  direction. One allocation function, parameterized by direction (deliver
  vs. depart), not two policies to keep in sync. The lowest levels also
  have the lowest sell prices, so the delivered shares cycle back to cash
  soonest.

**Why "over all levels", not "above spot" (revised after design
review):** an earlier draft started CSP attribution at "the lowest level
above spot". Spot changes, so re-deriving later would give a different
split, and the fold (scenario 1) wouldn't be deterministic. Qualification
makes spot unnecessary: at the moment a wheel epoch opens, every level was
cash (CSP) or every level held shares (CC), so attribution over all levels
is fully determined by `GridLevels` sizes alone. *Where sell orders go now*
is a separate, spot-dependent placement decision — each level holding
shares simply places its sell limiter at its own sell price — and isn't
part of attribution.

This attribution is **never stored** — it's recomputed by the same rule
during scenario 1's chronological replay, at the point each assigned wheel
epoch ended, from persisted data only (`GridLevels` and the position's
`Assignment` fact). With spillover (level sizes summing to more than 100),
at most one level ends up partially attributed; the fold treats it like
Looper's partial-fill case.

### 4. `SetOption`: freeze and retire, per-greeler

project.md open item 6 names `freeze=grid|wheel|all` and retire semantics.
Following `Looper`'s existing pattern (`freezeBuysOpt`/`freezeSellsOpt`/
`retireOpt`, parsed from `Options` in `Load`, mirrored in `greeler/
options.go`):

- `freeze=grid`: scenario 1's per-level loop stops creating new limiters
  (existing ones still resolve normally) — the greeler can still flip to
  wheel mode if dwell/hysteresis fire, since that's not "grid activity."
- `freeze=wheel`: scenario 2's flip-to-wheel step never fires (dwell clock
  still accumulates and gets persisted — `PendingFlip`/`PendingFlipAt`
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
`Limiter.Load`/`Looper` already read theirs. Per the `trader.Trader`
contract, options change only while the job isn't running — operators
pause, set, and resume, as with `Looper`.

### 5. Restart: rebuilding from the record alone

The server reloads jobs generically — `server.Load(ctx, r, uid, typename)`
(`server/load.go:75`) gets only a KV reader — so `greeler.Load(ctx, uid,
r)` must rebuild everything from `GreelerStateV1`: `GridLevels`, zone
parameters, and the `ContractSelector`/`RollPolicy` looked up by their
persisted names and built from the persisted `WheelKnobs` (gobs-story.md
decision #11). The one runtime dependency the record can't hold, the
options exchange, comes from `rt.Exchange.(exchange.OptionsExchange)` in
`Run`; positions load lazily there (decision #1). `server.Load` gains
`greeler`/`greelladder` cases.

`trader.Trader` also requires `Actions`, `BudgetAt`, and `GetSummary`. The
accounting model is deferred (project.md open item 2), so these get
minimal implementations — stock-side limiter fills only — until it lands.

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package greeler

import (
    "context"
    "time"

    "github.com/bvk/tradebot/exchange"
    "github.com/bvk/tradebot/gobs"
    "github.com/bvk/tradebot/limiter"
    "github.com/bvk/tradebot/optpos"
    "github.com/bvk/tradebot/point"
    "github.com/bvk/tradebot/timerange"
    "github.com/bvk/tradebot/trader"
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

// Greeler runs one greel: derives per-level posture from spot, limiter
// fills, and the outcomes its positions report. A job (trader.Trader), so
// it's runnable standalone; under a ladder, the ladder drives it.
type Greeler struct {
    uid          string
    exchangeName string
    productID    string

    gridLevels []*point.Pair // ascending price order, never reordered

    gridPct, farPct, hysteresisPct decimal.Decimal
    dwellTime                       time.Duration

    epochs []*Epoch // mirrors gobs.GreelEpoch; last entry is current

    // Rebuilt in Load from the persisted names and WheelKnobs (scenario 5).
    selector optpos.ContractSelector
    policy   optpos.RollPolicy

    // exclude is set by the ladder (greelladder-story); nil standalone.
    exclude func(contractID string) bool

    freezeGridOpt, freezeWheelOpt, retireOpt bool
}

// Epoch mirrors gobs.GreelEpoch as an in-memory working type; Save/Load
// convert to/from the persisted shape directly (gobs-story.md).
type Epoch struct {
    Mode    string
    StartAt time.Time

    PendingFlip   string
    PendingFlipAt time.Time

    PositionID string
    Position   *optpos.Position // loaded lazily, nil until needed

    LevelLimiterIDs [][]string
    LevelLimiters   [][]*limiter.Limiter // loaded lazily per level
}

func (v *Greeler) Run(ctx context.Context, rt *trader.Runtime) error {
    panic("unimplemented")
    // optEx, ok := rt.Exchange.(exchange.OptionsExchange) — required.
    // Loop:
    //   last := v.epochs[len(v.epochs)-1]   // authoritative (write-ahead)
    //   switch last.Mode {
    //   case "grid": v.runGrid(ctx, rt, last)   // scenarios 1, 2
    //   case "wheel": v.runWheel(ctx, rt, last) // scenario 2
    //   }
}

// DerivedStock is the greeler's total current stock inventory — scenario
// 1's per-level fold, summed across all levels. Exported so the ladder's
// delta-based reconciliation (greelladder-story.md scenario 3) can compare
// its change against the account's, without reaching into greeler's own
// derivation internals.
func (v *Greeler) DerivedStock() decimal.Decimal {
    panic("unimplemented")
}

// attribute implements scenario 3's rule: lowest GridLevels index first,
// over all levels, filling each level's size before the next, until shares
// is exhausted. Depends only on GridLevels — never on spot — so the replay
// in scenario 1 is deterministic. Used for both CSP (shares arrive) and CC
// (shares leave) assignments.
func (v *Greeler) attribute(shares decimal.Decimal) []struct {
    LevelIndex int
    Size       decimal.Decimal
} {
    panic("unimplemented")
}

func (v *Greeler) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented")
}

// Load rebuilds a greeler from its record alone — server.Load can't inject
// dependencies (scenario 5).
func Load(ctx context.Context, uid string, r kv.Reader) (*Greeler, error) {
    panic("unimplemented")
}

// Minimal until the accounting model lands (scenario 5).
func (v *Greeler) Actions() []*gobs.Action                        { panic("unimplemented") }
func (v *Greeler) BudgetAt(feePct decimal.Decimal) decimal.Decimal { panic("unimplemented") }
func (v *Greeler) GetSummary(r *timerange.Range) *gobs.Summary     { panic("unimplemented") }

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
3. **Cross-greeler risk gates belong to `greelladder` — TODO, revisit.**
   Max open contracts and max assignment exposure (project.md open item 6)
   sit above `greeler`. How the ladder enforces them is open: the earlier
   plan (calling `SetOption` on running greelers) violates the
   `trader.Trader` contract. `greeler` itself enforces only its own
   freeze/retire (scenario 4).
4. **`DerivedStock()` exported — added for `greelladder`.** The ladder's
   delta-based reconciliation (greelladder-story.md scenario 3) compares
   changes in a greeler's derived stock against changes in the account's
   holding. Rather than have `greelladder` reach into `greeler`'s
   internals, `greeler` exports the scenario 1 fold's sum directly. No new
   persisted state — this is a read of an already-derived value.
5. **Flips are write-ahead — decided after design review.** Each epoch is
   appended before anything is created in it, and every child is saved
   before it can place an order (scenario 2; gobs-story.md decision #9).
6. **A blocked wheel flip leaves the greeler in grid mode — decided after
   design review.** If qualification fails on the re-check after cancel
   (a fill raced it), the greeler re-arms its levels and resets the dwell
   clock (scenario 2).
7. **Attribution is over all levels, not "above spot" — revised after
   design review.** Keeps the replay in scenario 1 deterministic; placement
   stays a separate, spot-dependent decision (scenario 3).
8. **`Load` rebuilds from the record alone — decided after design
   review.** Selector/policy from persisted names and knobs; options
   exchange from `rt.Exchange` in `Run` (scenario 5).
9. **Minimal `Actions`/`BudgetAt`/`GetSummary` until the accounting model
   lands** (scenario 5).
