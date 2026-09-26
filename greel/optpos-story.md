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

By the time `Position.Open` is called, the greeler has already appended the
wheel epoch naming this position's UID and saved the empty position record
(write-ahead, gobs-story.md scenario 3a). `Open` then does, in order:

1. Fetch the chain: `optionsExchange.GetOptionsChain(ctx, underlying)`.
2. `ContractSelector.Select(ctx, chain, constraint)` picks one contract —
   `constraint` carries the greeler-derived bounds (strike ≤ greeler's
   levels / ≤ cash÷100 for a CSP, strike ≥ levels' sell prices for a CC;
   project.md's contract-selection section), plus the ladder's sibling
   exclusion when there is one (greelladder-story) — not DTE/liquidity
   knobs, which live on the selector itself (decision #1).
3. `optionsExchange.OpenOptionsProduct(ctx, contract.ContractID)` opens the
   live product.
4. `optlimiter.New(legUID, exchangeName, contract.ContractID, optlimiter.IntentOpen, "SELL", contract.ContractSize, numContracts, limitPrice)`
   builds the leg, with a deterministic `legUID = path.Join(positionUID,
   "leg-%06d")` (limit price from the selector's own premium target, not
   re-derived here).
5. **Write-ahead:** append the leg's UID as `Legs[0]`, set `Contract` (the
   cache field) from the selected contract, and save — one transaction,
   *before* anything is placed. A crash after this point resumes the same
   leg against the same contract (scenario 5) instead of re-selecting.
6. Only then does `job.Run` spawn the leg (per Grounding above).

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
- `ActionClose`: place a buy-to-close `OptLimiter` leg (`Intent = "close"`,
  side `"BUY"`; UID appended to `Legs` and saved before it runs, like
  scenario 1), and once it fills, set `Outcome = "closed"` (scenario 4).
- `ActionRoll`: scenario 3.

Each `Check` runs in this order, and stops at the first step that applies:

1. `Outcome` already set → terminal; nothing to do.
2. **Settlement check** (scenario 6, at most hourly): if the broker reports
   the contract assigned or expired, record it (scenario 4) and stop.
3. **Past `Contract.Expiry`** → the position is *settling*: no decisions,
   no new legs, just wait for step 2 to see broker truth. Expiry is not a
   calendar fact — an in-the-money contract is assigned at expiry and the
   broker reports it the next day, so the calendar alone can't say which
   way it ended.
4. A leg already in flight → wait for it.
5. Otherwise, ask `RollPolicy` (skipped if nil) and act on the answer.

### 3. Rolling: one `OptionsRollProduct` leg, not two orders

When `RollPolicy.Decide` returns `ActionRoll`, `Position`:

1. Picks the replacement contract — `ContractSelector.Select` again, same
   `constraint` (the greeler's levels haven't moved).
2. `optionsExchange.OpenOptionsRollProduct(ctx, currentContract.ContractID, newContract.ContractID)`
   — the interface method from optlimiter-story.md decision #1.
3. `optlimiter.NewRoll(legUID, exchangeName, rollProduct.ProductID(), currentContract.ContractID, newContract.ContractID, side, ...)`
   — side `"SELL"` for a net credit, `"BUY"` for a net debit. The roll leg
   does **not** go through `optlimiter.NewProduct`: an `OptionsRollProduct`
   already is an `exchange.Product`, so it's passed directly as the leg's
   `trader.Runtime.Product` (optlimiter-story.md scenario 6).
4. **Write-ahead:** append the one roll-leg UID to `Legs` and save, then
   `job.Run` spawns it. On fill, `Contract` is refreshed to the new
   contract; since it's only a cache, `Load` also refreshes it from the
   last filled leg, so a crash between fill and save loses nothing.

No separate close-then-open bookkeeping exists anywhere in `optpos` — the
one atomic roll leg *is* the whole operation, matching the broker reality
gobs-story.md's decision required.

### 4. Position dies: one writer, three outcomes

Every terminal outcome is written by the position itself, inside its
greeler's tree — nothing outside that tree (not the ladder) ever writes a
position record, so there's no stale-copy overwrite to guard against:

- **`"closed"`**: its own buy-to-close leg filled (scenario 2) — local
  truth, our own order. `Assignment` stays nil.
- **`"assigned"`**: the settlement check (scenario 6) reports an
  assignment. `Outcome`, `OutcomeAt`, and the `Assignment` fact (broker
  transaction key; shares = contracts × `ContractSize`, positive for a
  put, negative for a call; price = strike) are set in one transaction.
- **`"expired"`**: the settlement check reports an explicit expiration
  record. Never inferred from the calendar, and never from the contract
  merely disappearing from the account — absence alone could be a
  reporting lag.

Once `Outcome` is set, `Legs` is frozen and `Check` is a no-op.

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
calls, converge" pattern the whole design already relies on. A position
with no legs yet (the greeler crashed between saving the empty record and
`Open`) simply runs `Open` again.

### 6. Settlement check: broker truth about the contract

The one piece of new exchange surface this module needs — **proposed, not
yet signed off** (touches `exchange/api.go`):

```go
// OptionsSettlement is broker truth about a contract this account wrote.
type OptionsSettlement struct {
    Status    string          // "open" | "assigned" | "expired"
    Key       string          // broker transaction ID, for assigned/expired
    Contracts decimal.Decimal // contracts assigned or expired
    At        time.Time
}

// GetOptionsSettlement reports how a written contract stands. "expired"
// and "assigned" come only from explicit broker records.
GetOptionsSettlement(ctx context.Context, contractID string) (*OptionsSettlement, error)
```

`Check` calls this at most **hourly** — the interval decided earlier for
the ladder's poll, which this replaces. That catches early assignment
(ex-dividend risk) the same day, and expiry-time assignment the morning
after it's reported. The last-check time is in memory only; after a
restart the first `Check` just checks immediately.

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

    // Exclude reports contracts a sibling greeler already holds or is
    // selecting, so two siblings never write the same contract series
    // (greelladder-story). Nil when the greeler runs standalone.
    Exclude func(contractID string) bool
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

    lastSettlementCheck time.Time // in memory only; hourly cadence (scenario 6)

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

// Open selects a contract and places the sell-to-open leg. The greeler has
// already saved this position's empty record under its wheel epoch
// (write-ahead); Open saves the leg's UID before the leg can place an order.
func (v *Position) Open(ctx context.Context, fctx context.Context, c *Constraint) error {
    panic("unimplemented") // scenario 1
}

// Check re-derives what should happen right now — settlement, then expiry
// (settling), then RollPolicy — and acts. Called by the owning greeler
// every iteration while this position is open; a no-op once Outcome is set
// (scenarios 2, 4).
func (v *Position) Check(ctx context.Context, fctx context.Context) error {
    panic("unimplemented") // scenario 2
}

// Outcome is empty while open or settling; terminal otherwise.
func (v *Position) Outcome() string { return v.outcome }

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
   commands directly.
4. **Terminal outcomes come only from broker truth or the position's own
   fill; past expiry the position is settling — decided after design
   review.** Replaces the first draft's calendar-based `"expired"`, which
   would have recorded every expiry-time assignment as an expiry and then
   ignored the broker's assignment report as a duplicate (scenarios 2, 4).
5. **Assignment detection lives in the position, driven by its greeler —
   decided after design review.** Replaces the ladder's positions poll.
   The greeler's tree becomes the only writer of position records (no
   stale-copy overwrites), and a standalone greeler detects its own
   assignments. The hourly cadence moved here from the ladder (scenario 6).
6. **Legs are written ahead with deterministic UIDs — decided after design
   review.** A leg's UID (`path.Join(positionUID, "leg-%06d")`) is saved
   before it can place an order, so a crash can't orphan an order or
   re-select a different contract on restart (scenarios 1, 3).
7. **Sibling exclusion via `Constraint.Exclude` — decided after design
   review**, provided by the ladder; nil when standalone. TODO: revisit
   the restriction (greelladder-story).

## Open questions for this checkpoint

1. **`OptionsExchange.GetOptionsSettlement` (scenario 6) — proposed, needs
   sign-off** before touching `exchange/api.go`.
