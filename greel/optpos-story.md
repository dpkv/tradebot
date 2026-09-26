# Story: `optpos` — one written-option position

Companion to [project.md](project.md), [gobs-story.md](gobs-story.md), and
[optlimiter-story.md](optlimiter-story.md). Third stage of the dependency
order (`gobs/greel.go → optlimiter → optpos → greeler → greelladder`). This
module owns the position lifecycle project.md already named — born via
sell-to-open, lives through profit-take/roll/expiry, dies with exactly one
terminal outcome — and is the module most likely to pressure-test the
persisted shapes decided so far, since it's the direct consumer of
`OptPositionStateV1`, `OptLimiter`, and `OptionsRollProduct`.

---

## Grounding: how a component runs without job registration

project.md's job hierarchy table is explicit: `optpos.Position` and
`optlimiter.OptLimiter` are **components** — "passive, owner-driven, own
persisted records but no job registration." Concretely, this means neither
goes through `job.Runner.Add`/`Scan` (the persistent, UID-tracked job list
that `Limiter`/`Looper`/`Waller`/`Greeler`/`GreelLadder` use). But driving
`OptLimiter.Run` — which is `limiter.Limiter.Run` underneath, a **blocking**
loop that doesn't return until the order's pending size hits zero or `ctx`
is canceled (`limiter/run.go:96-196`) — still needs its own goroutine, or it
would stall whatever calls it (the greeler's own iteration loop) for as long
as the option order takes to fill.

`job.Run(fn job.Func, fctx context.Context) *job.Job` (`job/job.go`) is
exactly this, already built, and independent of the registration machinery:
it spawns `fn` in a goroutine and returns a handle with `Wait`/`Cancel` —
nothing about it requires `Runner.Add`. `optpos` uses this directly: when a
leg needs to run, it builds the `trader.Runtime` (mirroring
`Server.Runtime(product)` in `server/server.go:221-228` — same shape,
`Exchange`/`Database`/`Product`/`Messenger`, just with `Product` set to the
`optlimiter.NewProduct` adapter instead of a spot product) and calls
`job.Run(func(ctx) error { return optLimiter.Run(ctx, rt) }, fctx)`. The
returned `*job.Job` is held by `optpos`, not registered anywhere — exactly
"owner-driven," matching the design's own words for what a component is.

This resolves the concurrency question implicitly left open by "optpos
drives OptLimiter": it drives it *asynchronously*, holding a handle, not by
calling a blocking method inline.

---

## Scenario walkthrough

### 1. Position born: contract selection, then sell-to-open

`Position.Open` (called by the owning `greeler` once qualification holds —
gobs-story.md's all-or-nothing rule) does, in order:

1. Fetch the chain: `optionsExchange.GetOptionsChain(ctx, underlying)`.
2. `ContractSelector.Select(ctx, chain, constraint)` picks one contract —
   `constraint` carries the greeler-derived bounds (strike ≤ greeler's
   levels / ≤ cash÷100 for a CSP, strike ≥ levels' sell prices for a CC;
   project.md's contract-selection section), not DTE/liquidity knobs, which
   live on the selector itself (Layer 1, injected at construction — see
   Open questions).
3. `optionsExchange.OpenOptionsProduct(ctx, contract.ContractID)` opens the
   live product.
4. `optlimiter.New(uid, exchangeName, contract.ContractID, optlimiter.IntentOpen, contract.ContractSize, numContracts, limitPrice)`
   builds the leg (limit price from the selector's own premium target, not
   re-derived here).
5. `job.Run` spawns it (per Grounding above); `Position` records the
   `OptLimiter` UID as `Legs[0]` and saves — this is the leg-chain's first
   entry, matching gobs-story's grammar (`sell-to-open roll* (buy-to-close)?`).
6. `Position.Contract` (the cache field, gobs-story.md) is set from
   `contract` directly — no round-trip through the exchange needed since
   the selector already returned the full snapshot.

### 2. Position lives: one decision function, asked repeatedly

Project.md names three live-position behaviors — profit-take, close-at-
threshold, expiry countdown — plus rolling. Rather than encode each as
separate `Position` logic, all four collapse into **one decision the
`RollPolicy` makes**, asked on every check (mirroring how `greeler`'s mode
derivation re-asks "what should be true now" every iteration instead of
tracking a phase):

```go
type RollAction string

const (
    ActionHold  RollAction = "hold"
    ActionClose RollAction = "close"
    ActionRoll  RollAction = "roll"
)

// RollPolicy decides what should happen to an open position right now.
// Nil disables all of it — hold forever until expiry or external
// assignment (project.md: "a nil RollPolicy disables rolling").
type RollPolicy interface {
    Decide(ctx context.Context, contract *gobs.OptionContract, greeks *exchange.Greeks) (RollAction, error)
}
```

This folds profit-take-pct, loss-close-multiple, and roll-dte (Layer 1
knobs, project.md) *into* the policy's own decision, rather than `Position`
checking each threshold itself — `Position` only needs to know what to *do*
with the answer:

- `ActionHold`: nothing.
- `ActionClose`: place a `buy-to-close` `OptLimiter` leg (append to `Legs`,
  same shape as scenario 1's sell-to-open but `Intent = "close"`), and once
  it fills, set `Outcome = "closed"` (scenario 4).
- `ActionRoll`: scenario 3.

Expiry itself isn't a `RollPolicy` decision — it's a calendar fact
(`Contract.Expiry` vs. wall clock, gobs-story.md's own framing for why it's
not a `GreelEvent`/`Assignment`-style external fact either). `Position`
checks this directly on every call, ahead of consulting `RollPolicy` at
all: past expiry, the position is already terminal by the time anyone asks.

### 3. Rolling: one `OptionsRollProduct` leg, not two orders

When `RollPolicy.Decide` returns `ActionRoll`, `Position`:

1. Picks the replacement contract — `ContractSelector.Select` again, same
   `constraint` (the greeler's levels haven't moved).
2. `optionsExchange.OpenOptionsRollProduct(ctx, currentContract.ContractID, newContract.ContractID)`
   — the new interface method from optlimiter-story.md decision #1.
3. `optlimiter.New(uid, exchangeName, "" /* no single ContractID */, optlimiter.IntentRoll, ...)`
   with `PriorContractID`/`ContractID` set per gobs-story.md's
   `OptLimiterStateV1` shape, wrapping the `OptionsRollProduct` via
   `optlimiter.NewProduct` (which already accepts any `exchange.
   OptionsProduct`-shaped wrapped value per its signature — a
   `RollProduct`/`OptionsRollProduct` is `exchange.Product` directly, so
   the roll leg's `OptLimiter` doesn't even go through the adapter at all;
   see optlimiter-story.md scenario 6 — it wraps a `*limiter.Limiter` built
   straight against the `OptionsRollProduct`).
4. `job.Run` spawns it; on fill, `Position.Contract` is refreshed to the
   new contract (gobs-story.md: "refreshed on every roll") and `Legs` gets
   the one new roll-leg UID appended — never two.

No separate close-then-open bookkeeping exists anywhere in `optpos` — the
one atomic roll leg *is* the whole operation, matching the broker reality
gobs-story.md's decision required.

### 4. Position dies: two different writers, one struct

gobs-story.md already decided where each terminal outcome is written, but
`optpos`'s story is what has to *respect* that boundary operationally:

- **`"closed"`** (scenario 2's `ActionClose` path) and **`"expired"`**
  (scenario 2's calendar check) are both detected and written by `optpos`
  itself — `Outcome`/`OutcomeAt` set directly, no `Assignment` fact (that
  field is `nil` for these two outcomes).
- **`"assigned"`** is written by the ladder's positions poll, reaching
  directly into `OptPositionState` — not through any `optpos` code path at
  all (gobs-story.md scenario 5). `optpos` never initiates this.

The operational consequence: every `Position` method that's about to act
(scenario 2's live check, scenario 1's open) must **load-and-check `Outcome`
first**, since it can go non-empty behind `optpos`'s back between one
greeler iteration and the next. Discovering `Outcome == "assigned"` on
what `optpos` thought was still an open position isn't an error — it's
exactly the crash-safe, externally-caused termination gobs-story.md
designed for. `Position` just stops: no more legs, nothing to place,
report terminal upward.

### 5. Crash and resume: load, find the live leg, reattach

Restart loads `OptPositionState`, and if `Outcome` is still empty, the
position is live: `Legs[len(Legs)-1]` names the current leg's `OptLimiter`.
`optlimiter.Load` resumes it exactly as `limiter.Load` already does
(order-map reconciliation against the live exchange, gobs-story.md's
embed-vs-reference decision #1) — `optpos` just needs to re-open the
product (`OpenOptionsProduct`/`OpenOptionsRollProduct` again, using
`Contract`'s cached identity, or `ContractID`/`PriorContractID` off the
resumed `OptLimiter` itself), rebuild the adapter, and `job.Run` it again.
Nothing here is new state — it's the same "re-derive, re-issue idempotent
calls, converge" pattern the whole design already relies on.

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package optpos

import (
    "context"
    "time"

    "github.com/bvk/tradebot/exchange"
    "github.com/bvk/tradebot/gobs"
    "github.com/bvk/tradebot/job"
    "github.com/bvk/tradebot/optlimiter"
    "github.com/bvk/tradebot/trader"
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

// Constraint bounds contract selection to what the owning greeler allows —
// derived from its levels, not a Layer 1 knob (project.md contract-
// selection section).
type Constraint struct {
    Underlying string
    OptionType string // "PUT" for CSP, "CALL" for CC

    MaxStrike decimal.Decimal // CSP: <= levels and <= cash/100
    MinStrike decimal.Decimal // CC: >= levels' sell prices
}

// ContractSelector picks the next contract to open for one position.
// Layer 1 knobs (target-delta, dte-range, min-premium-yield, min-open-
// interest, max-spread-pct) are injected into the selector at
// construction, not passed per-call — Constraint is the only thing that
// varies call to call, because it comes from the greeler, not policy.
type ContractSelector interface {
    Select(ctx context.Context, chain []*gobs.OptionContract, c *Constraint) (*gobs.OptionContract, error)
}

type RollAction string

const (
    ActionHold  RollAction = "hold"
    ActionClose RollAction = "close"
    ActionRoll  RollAction = "roll"
)

// RollPolicy decides what should happen to an open position right now.
// Nil disables rolling (and profit-take/loss-close) entirely — position
// holds until expiry or external assignment. Layer 1 knobs (profit-take-
// pct, loss-close-multiple, roll-dte) live behind this interface, not on
// Position — see scenario 2.
type RollPolicy interface {
    Decide(ctx context.Context, contract *gobs.OptionContract, greeks *exchange.Greeks) (RollAction, error)
}

// Position is one written-option position: sell-to-open through exactly
// one terminal outcome. Component, not a job — no jobs.Register entry;
// driven by its owning greeler via job.Run, not through the persistent
// Runner (see Grounding above).
type Position struct {
    uid          string
    exchangeName string
    underlying   string

    selector ContractSelector
    policy   RollPolicy // nil disables rolling/close

    contract *gobs.OptionContract // cache, refreshed on open/roll

    legIDs []string // ordered optlimiter UIDs — gobs-story.md's Legs

    outcome   string
    outcomeAt time.Time

    active *job.Job // the currently running leg, nil if none in flight

    // Held at construction (New/Load), not re-passed to every method —
    // decided, see decision #2. runtime builds each leg's trader.Runtime.
    optEx exchange.OptionsExchange
    db    kv.Database
    msg   trader.Messenger
}

// runtime builds the trader.Runtime for one leg's OptLimiter.Run call —
// same shape as Server.Runtime(product) in server/server.go, with Product
// set to the optlimiter adapter instead of a spot product.
func (v *Position) runtime(p exchange.Product) *trader.Runtime {
    return &trader.Runtime{Exchange: v.optEx, Database: v.db, Product: p, Messenger: v.msg}
}

func New(uid, exchangeName, underlying string, selector ContractSelector, policy RollPolicy, optEx exchange.OptionsExchange, db kv.Database, msg trader.Messenger) *Position {
    panic("unimplemented")
}

func (v *Position) Open(ctx context.Context, fctx context.Context, c *Constraint) error {
    panic("unimplemented") // scenario 1
}

// Check re-derives what should happen right now — expiry, then RollPolicy,
// then acts. Called by the owning greeler every iteration while this
// position is open; a no-op once Outcome is set by anyone (scenario 4).
func (v *Position) Check(ctx context.Context, fctx context.Context) error {
    panic("unimplemented") // scenario 2
}

func (v *Position) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented")
}

func Load(ctx context.Context, uid string, r kv.Reader, selector ContractSelector, policy RollPolicy, optEx exchange.OptionsExchange, db kv.Database, msg trader.Messenger) (*Position, error) {
    panic("unimplemented") // scenario 5
}
```

---

## Decisions made at this checkpoint

1. **Layer 1 knobs reach `ContractSelector`/`RollPolicy` via one shared
   knob struct, injected at construction — decided.** Presets (Layer 0,
   `--wheel-profile=conservative|balanced|aggressive`) are "named bundles
   of Layer-1 knobs" per project.md's own phrase — one bundle, not two
   separately-shaped ones. A preset constructs a single knob struct
   (`target-delta`, `dte-range`, `min-premium-yield`, `min-open-interest`,
   `max-spread-pct`, `roll-dte`, `profit-take-pct`, `loss-close-multiple`),
   and both the `ContractSelector` and `RollPolicy` implementations close
   over it — avoids two separate configs drifting apart for knobs that
   conceptually belong to the same preset (e.g. `dte-range` plausibly
   matters to both initial selection and roll timing).
2. **`Position` holds its exchange/db/messenger dependencies at
   construction, not per-call — decided.** `New`/`Load` take them once;
   `Open`/`Check` shrink to just the arguments that vary per call
   (`Constraint`, nothing). `runtime(p)` becomes a method reading the held
   fields instead of a free function taking five parameters. Reflected in
   the skeleton above.

3. **`Check` needs no spot/zone information — resolved by the `greeler`
   story.** greeler-story.md scenario 2 settles this: `greeler` calls
   `Check` unconditionally every iteration while the last epoch is
   `"wheel"` and its position isn't terminal, never gated on spot's zone.
   The greeler never force-closes a position on a zone change — project.md
   already says "existing orders/positions drift through untouched" in the
   buffer, and that principle extends past the buffer into wheel mode
   itself. The flip back to grid is a *consequence* of `RollPolicy`/expiry/
   assignment making the position terminal, never something `greeler`
   commands directly. No open questions remain in this module.
