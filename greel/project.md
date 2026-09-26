# Greel — Project Overview

## Summary

Greel is a hybrid trading strategy combining grid trading and option wheeling.
One **greel** = a band of grid price levels plus one written-option leg over
the same levels:

- **Spot near the levels** → grid trading: buy/sell limit orders at the
  levels, using the existing `limiter.Limiter` machinery.
- **Spot far above the levels** → the levels' capital is idle cash: sell a
  cash-secured put (CSP) struck at the levels to collect premium. If assigned,
  stock arrives at the strike and seeds the grid's sell legs.
- **Spot far below the levels** → the levels hold bought inventory: sell a
  covered call (CC) struck at/above the levels' sell prices. If assigned,
  the inventory leaves at least at its planned exit price plus premium.

Inputs: exchange name, underlying ID, level definitions (prices/sizes), zone
parameters, option selection criteria, upfront budget.

Outputs: continuous premium/profit collection in both regimes. State persists
in the KV store so runs resume after restart.

Constraints: requires an `exchange.OptionsExchange`. Underlying must have a
listed options chain (equities/ETFs via etrade; not crypto).

---

## Job hierarchy

```
greelladder.GreelLadder (job)      ladder of greelers across price bands;
 │                                 sibling exclusion, reconciliation, aggregation
 └── greeler.Greeler (job) × M     runs one greel: derives per-level posture
      │                            from spot + fills + recorded events
      ├── limiter.Limiter × N      one stock order intent (reused untouched)
      └── optpos.Position × 0..1   one written-option position (component,
            │                      not a job); terminal: expired|assigned|closed
            └── optlimiter.OptLimiter   one option order (wraps limiter.Limiter)
```

- **Jobs** (`trader.Trader`, agent nouns, independently runnable):
  `GreelLadder`, `Greeler`, `Limiter`. A `Greeler` is fully usable standalone —
  the ladder is a thin spawner/aggregator, as `Waller` is to `Looper`.
- **Components** (passive, owner-driven, own persisted records but no job
  registration): `optpos.Position`, `optlimiter.OptLimiter`.
- The `looper` package is **not used** by greel (see design decisions).

---

## Core design principles

1. **Derivation over stored state.** Beyond its config, a greeler stores
   only an append-only `Epochs` list: one entry per grid or wheel period,
   naming the children created in it, with the dwell clock on the current
   entry. Per-level cycle position and all inventory are derived each
   iteration from spot price, limiter fill records, and the outcomes positions
   report about themselves. There is no contribution list and no ledger.

2. **Single-shot atoms; only the supervisor cycles.** `Limiter` is one stock
   order intent; `optpos.Position` is one written-option position. Neither
   carries a phase pointer that external events can invalidate. The greeler is
   the sole cyclic component, and it cycles by re-deriving desired posture,
   never by remembering where it was.

3. **The greeler owns the portfolio.** Cash and shares belong to the greeler,
   not to its children. An option position never "holds" collateral or
   assigned stock — it only reports terminal facts ("assigned: +100 shares at
   strike K"). This is what makes assignments ordinary bookkeeping instead of
   cross-component handover protocols.

4. **All-or-nothing qualification.** A greeler arms a CSP only when every
   level is flat (all cash) and a CC only when every level holds its bought
   shares. Qualification is by definition of greeler sizing (see below), so
   no per-level contribution tracking is ever needed.

---

## Greeler geometry and sizing

- A greeler owns the **smallest number of adjacent grid levels whose sizes sum
  to ≥ 100 shares** (one option contract). Spillover shares beyond 100 are
  allowed and tracked implicitly by the levels that hold them.
- Zone parameters (percentages of spot, so they scale): grid half-width `g`,
  far threshold `f > g`, hysteresis `h`, dwell time `d`. Interpretation is
  **per greeler**, relative to its own levels:
  - Levels within `S·(1±g)` → grid mode: stock limiters active.
  - Levels beyond `S·(1±f)` → wheel mode eligible: CSP (levels below spot,
    all-cash) or CC (levels above spot, all-shares).
  - The buffer `(g, f)` enforces strict mutual exclusion: nothing new opens
    there; existing orders/positions drift through untouched.
- **Mode flips are gated** by hysteresis `h` and dwell `d` (per greeler): spot
  must stay past the threshold for `d` before a flip. The dwell clock is
  persisted on the current epoch.
- **When an option position is open, all stock limiters are dark** (canceled),
  including levels holding spillover shares. No resting spillover sells.
- Far-away greelers (spot moved multiples away) write near-zero-premium
  contracts pinned at their own levels — their collateral cannot chase strikes
  toward spot. This is intentional: the cost of staying in the game until
  price returns to that band.

## Mode-flip mechanics

Flip to wheel mode: check qualification (all-cash or all-shares) → cancel all
stock limiters → await cancel confirmations → re-check qualification → in one
transaction, append the wheel epoch naming the position's (deterministic) UID
and save the empty position record → open the position. If the re-check fails
(e.g. a partial fill raced the cancel), the flip is blocked: the greeler stays
in grid mode and the dwell clock resets. Flip to grid mode: the position must
be terminal first (settled by broker truth, or closed by its own
buy-to-close) → append the grid epoch → create stock limiters per derived
posture. Every child is recorded before it can place an order, so a crash
mid-flip is harmless: restart finds every child by UID, re-issues idempotent
cancels, and converges.

## Assignment and expiry handling (crash-safe by construction)

Assignment involves **no exchange mutation** — the broker already moved the
stock; ours is pure bookkeeping, and only broker truth decides how a written
contract ended:

1. **Detect**: the greeler, through its open position, asks the broker how its
   own contract stands (at most hourly): still open, assigned, or expired.
   The calendar alone decides nothing — past `Expiry` the position is
   *settling* until the broker reports assignment or expiration, because
   in-the-money contracts are assigned at expiry and reported the next day.
2. **Apply in one KV transaction, touching one record**: the position records
   the fact on itself (`Outcome`; for assignment also shares delta, strike,
   and the broker transaction key). The greeler's own tree is the only
   writer of that record. Nothing else — no ledger to patch; the greeler's
   posture is derived by walking its epochs and reading what each position
   reports.
3. **Converge**: the greeler appends a grid epoch and creates the appropriate
   limiters, which place real orders through limiter's existing idempotent
   machinery.

Crash windows: before commit → the next check re-reads broker truth and
reprocesses; after commit → children resume idempotently; a repeated
detection → no-op because the position's outcome is already set.

Post-assignment postures (derived, not restored):

- **CSP assigned** (100 shares arrive at strike K): the shares are attributed
  to levels lowest-first by size, over all levels (every level was cash, by
  qualification) — a rule, not state. Each level holding shares places its
  sell limiter; buy limiters arm **as sells fill and free cash**. No
  opportunity is lost — the "waiting" buys require cash that does not yet
  exist.
- **CC assigned** (100 shares leave at strike K ≥ every level's sell price):
  recorded as completed sales at K against the levels, attributed
  lowest-first over all levels (every level held shares, by qualification).
  Portfolio is all-cash; buy limiters below spot arm immediately, or a new
  CSP arms if spot is far above (zone decision).

Never reshape the portfolio with market orders; all buying/selling happens
via limit orders at designated level prices.

---

## Option atoms

### optlimiter — one option order (reuses limiter.Limiter)

`optlimiter` is a thin wrapper around the existing `limiter.Limiter`, not a
new order state machine. The hardened parts of an executor (idempotent client
IDs, crash resume via order-map reconciliation, live-order sanity checks) are
not options-specific, and `exchange.OptionsProduct` already returns the same
`Order`/`OrderDetail` types as `exchange.Product`.

- **Downstream adapter** (`optlimiter/product.go`): adapts
  `exchange.OptionsProduct` to `exchange.Product`. Collapses the 4 option
  verbs (buy/sell × open/close — kept separate in the exchange interface
  because the broker validates them differently and explicit intent turns
  stale-state mistakes into rejections) into `LimitBuy`/`LimitSell` via an
  open/close intent fixed at construction (one wrapped limiter = one order
  intent, matching its single-shot design).
- **Leak mitigations**, all local: cancel-offset machinery neutralized by a
  far-side cancel price (order placed once, rests); contract/premium units
  scaled by `ContractSize()` at the wrapper boundary; own keyspace
  (`/optlimiters/`) so `/limiters/`-scanning background tasks skip them.
- **Escape hatch**: if the wrapper ever needs to reach into `Limiter`
  internals or suppress (rather than translate) a behavior, stop and fork a
  bespoke state machine inside `optlimiter`.

### optpos — one written-option position (component, not a job)

Manages a short option position from open to termination; put-vs-call side is
derived from the contract (as `Limiter` derives buy/sell from its point).

- Born: sell-to-open via `optlimiter` (contract picked by injected selector).
- Lives: each check, a `RollPolicy` decides whether to hold, close
  (buy-to-close — profit-take or loss-close), or roll. **A roll is one atomic
  broker order** replacing the contract within the same position chain. A nil
  `RollPolicy` holds until settlement. Past expiry the position is
  *settling*: no decisions, just waiting for broker truth.
- Dies: exactly one terminal outcome — `expired` | `assigned` (broker
  truth) | `closed` (its own buy-to-close filled) — recorded truthfully,
  forever. Never owns collateral or stock.

A future standalone classic-wheel bot (`wheeler` — the name fits there, since
that job runs the full cycle) would compose `optpos.Position` components the
way `Looper` composes `Limiter`s.

### Contract selection (layered; lives in optpos)

Strike is largely anchored to the greeler's levels (CSP strike ≤ its levels
and ≤ greeler cash / 100; CC strike ≥ its levels' sell prices), so selection
is mostly expiry/DTE plus liquidity guards.

- **Layer 0 — presets**: `--wheel-profile=conservative|balanced|aggressive`,
  named bundles of Layer-1 knobs.
- **Layer 1 — knobs** (defaults; presets pre-fill): `target-delta`,
  `dte-range`, `min-premium-yield`, `min-open-interest`, `max-spread-pct`,
  `roll-dte`, `profit-take-pct`, `loss-close-multiple`.
- **Layer 2 — interfaces** (the real contract):

  ```go
  // ContractSelector picks the next contract to open for one position.
  type ContractSelector interface {
      Select(ctx context.Context, chain []*gobs.OptionContract,
          constraint *Constraint) (*gobs.OptionContract, error)
  }

  // RollPolicy decides hold | close | roll for an open position.
  // Nil holds until settlement.
  type RollPolicy interface {
      Decide(ctx context.Context, contract *gobs.OptionContract,
          greeks *exchange.Greeks) (RollAction, error)
  }
  ```

  New strategies are new implementations registered by name; compiled-in.
  The greeler persists the chosen names and the resolved knob values, so a
  restart rebuilds the same selector and policy.
- **Layer 3 — expression DSL**: deferred until Layer 2 proves insufficient.

---

## Design decision log

1. **optlimiter reuses limiter.Limiter** via a Product adapter (2026-07-06).
   TODO: likely superseded by a small option order state machine in
   `optlimiter` — `Limiter` only places a BUY order while the ticker is at
   or above its limit price (blocking buy-to-close), and can't re-price,
   which will be routine for options (optlimiter-story TODO).
2. **Exchange interface keeps 4 option order verbs** — broker-validated
   intent; explicit open/close turns stale-state bugs into order rejections.
3. **No ledger module.** Early designs centered on a global cash/lot
   ownership ledger. Per-greeler ownership plus all-or-nothing qualification
   plus derivation-over-state eliminated it; the surviving descendant is the
   outcome each position records about itself in a single transaction.
4. **Greeler is built on limiters directly, not loopers** (2026-07-07).
   `Looper`'s control flow derives purely from its own limiters' fills
   (`looper/run.go` bought/sold derivation), so it cannot absorb external
   events (assignment granting/removing inventory) without state forgery or
   retire-and-recreate churn. The cycle logic is ~90 lines of derivation —
   the complexity worth reusing lives in `Limiter`. Greeler's per-level cycle
   is the same derivation generalized to merge two sources: limiter fills and
   the assignment facts positions report. The `looper` package stays
   untouched.
5. **optpos.Position is a component, not a job.** Jobs are agent nouns that
   act; a position is passive and owner-driven. The single-shot position atom
   (rather than a cyclic "wheeler") means external events never invalidate
   its state — same principle that removed loopers.
6. **Naming**: unit job `greeler.Greeler` (runs one greel), parent
   `greelladder.GreelLadder` (laddering is real trading vocabulary), option
   atoms `optlimiter.OptLimiter` (order) / `optpos.Position` (position) —
   the order-vs-position pairing is deliberate.
7. **Budget is upfront and static.** New bands on re-centering are funded by
   user-guaranteed new budget; old greelers persist in low-yield wheel mode
   intentionally. Budget management may be plumbed later.

---

## Module structure

| Module | Package / files | Responsibility |
|---|---|---|
| **persistence** | `gobs/greel.go` | `GreelerState` (config + `Epochs`), `OptPositionState` (legs + outcome/assignment fact), `OptLimiterState`, `GreelLadderState` |
| **optlimiter** | `optlimiter/product.go`, `optlimiter/optlimiter.go` | `OptionsProduct`→`Product` adapter; `OptLimiter` wrapping `limiter.Limiter` (units, keyspace) |
| **optpos** | `optpos/position.go`, `optpos/selector.go` | `Position` lifecycle incl. broker settlement checks; `ContractSelector`, `RollPolicy`, presets |
| **greeler** | `greeler/greeler.go`, `greeler/run.go`, `greeler/options.go` | Per-level posture derivation; mode flips (buffer/hysteresis/dwell); assignment attribution; `SetOption` (retire/freeze) |
| **greelladder** | `greelladder/greelladder.go`, `greelladder/run.go` | Spawning and driving greelers; sibling contract exclusion; stock reconciliation (alert-only); risk gates (TODO); aggregate status |

Dependency and implementation order (each stage independently runnable):

```
gobs/greel.go → optlimiter → optpos → greeler → greelladder
```

Each module runs the full workflow cycle: story → execution state → function
skeletons → implementation (~200-line chunks) → tests → coverage, with
developer checkpoints at every step.

---

## Open items (decide during module design)

1. **Assignment detection** — *resolved* (optpos-story): each position asks
   the broker how its own contract stands; only broker truth writes
   `expired`/`assigned`. Needs an `OptionsExchange` settlement query —
   proposed, pending sign-off.
2. **Accounting model**: `gobs.Summary` is stock-centric. Premium, assignment
   cost basis, and per-greeler netting (e.g., CC sale recorded at greeler
   level against level cost bases) need an extended P&L model
   (`GetSummary`, `BudgetAt`, status/summary subcommands). Until then,
   greeler and ladder still implement minimal `Actions`/`BudgetAt`/
   `GetSummary`, since `trader.Trader` requires them.
3. **Runtime plumbing** — *resolved*: the greeler gets its
   `OptionsExchange` from `rt.Exchange`; optpos builds each option leg's own
   `trader.Runtime` (optpos-story).
4. **Market hours**: options trade regular hours only; reuse etrade's
   extended-hours handling to avoid overnight thrash.
5. **Corporate actions**: dividends (early-assignment risk), earnings dates
   (optional entry skip), splits (detect and freeze at minimum).
6. **Risk limits / kill switch** — *TODO, revisit*: ladder-level max open
   contracts / max assignment exposure. The earlier design (ladder calls
   `SetOption` on running greelers) violates the `trader.Trader` contract —
   options change only while a job isn't running. Per-greeler freeze
   (`freeze=grid|wheel|all`) and retire stay as operator controls.
7. **Paper trading / simulation**: a simulated `OptionsExchange` for engine
   testing and future backtesting.
8. **Observability**: per-greeler status (mode, band, open strike, collected
   premium); messenger notifications on assignment/roll/terminal events.
9. **Allocation rule** — *resolved* (greeler-story scenario 3): lowest level
   first by size, over all levels, for both CSP and CC assignment.
10. **Sibling contract restriction** — *TODO, revisit*: the ladder stops two
    of its greelers from writing the same contract series (greelladder-story).
11. **Keyspace isolation gaps** — *TODO*: limiter's background finish-time
    fixer still reaches option limiters (optlimiter-story scenario 7).
12. **Option order execution** — *TODO, before implementing optlimiter*:
    replace the wrapped `Limiter` with an option order state machine that
    places immediately and re-prices on a timer (optlimiter-story TODO).
    Would remove item 11 and change `OptLimiterStateV1`.

---

## Story Placeholders

- [x] `gobs/greel.go` story — drafted in [gobs-story.md](gobs-story.md), reviewed
- [ ] `optlimiter` story — drafted in [optlimiter-story.md](optlimiter-story.md); reopened by design review: TODO — option order state machine with re-pricing
- [ ] `optpos` story — drafted in [optpos-story.md](optpos-story.md); reopened by design review: `GetOptionsSettlement` interface needs sign-off
- [x] `greeler` story — drafted in [greeler-story.md](greeler-story.md), reviewed
- [x] `greelladder` story — drafted in [greelladder-story.md](greelladder-story.md), reviewed

---

## File Structure

```
tradebot/
├── greel/
│   ├── project.md           ← this file
│   ├── gobs-story.md        ← persisted shapes
│   ├── optlimiter-story.md
│   ├── optpos-story.md
│   ├── greeler-story.md
│   └── greelladder-story.md
├── gobs/
│   └── greel.go             ← serializable state structs
├── optlimiter/
│   ├── product.go           ← OptionsProduct → exchange.Product adapter
│   └── optlimiter.go        ← OptLimiter wrapping *limiter.Limiter
├── optpos/
│   ├── position.go          ← single written-option position lifecycle
│   └── selector.go          ← ContractSelector, RollPolicy, presets
├── greeler/
│   ├── greeler.go           ← Greeler struct, trader.Trader impl, Save/Load
│   ├── run.go               ← derivation loop, mode flips, event application
│   └── options.go           ← SetOption handlers
└── greelladder/
    ├── greelladder.go       ← GreelLadder struct, trader.Trader impl
    └── run.go               ← drive greelers, reconciliation
```
