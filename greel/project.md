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
            │                      not a job); terminal: expired|assigned|unfilled
            └── optlimiter.OptLimiter   one option order; re-prices until filled
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
be terminal first (settled by broker truth, or abandoned `unfilled` if its
opening order never filled) → append the grid epoch → create stock limiters per derived
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

### optlimiter — one option order (its own state machine)

One `OptLimiter` is one order intent — in v1, "sell to open N contracts, no
cheaper than this floor." It places a broker order immediately and
re-prices it toward the market on a timer until it fills (option spreads
are wide, so re-pricing is routine). An earlier design wrapped
`limiter.Limiter`; that took this section's own escape hatch, because
`Limiter` is a grid order — it holds an order only while the market is near
its fixed price, so it can't re-price and won't place a buy below market.
See optlimiter-story for the full comparison.

- **Never two live orders**: cancel, confirm, recompute remaining, then
  place — two fills of a sell-to-open would be a naked short.
- **Crash-safe client IDs**: saved ahead of each order; on resume, any
  unmatched ID is looked up at the broker by client ID (etrade can't
  dedupe client IDs).
- **Regular-session day orders**; re-pricing restarts from the mid each
  session.
- Reuses `idgen` and `exchange.SimpleOrder`; the `limiter` package is not
  involved, so no `/limiters/` task ever sees an option order.

### optpos — one written-option position (component, not a job)

Manages a short option position from open to termination; put-vs-call side is
derived from the contract (as `Limiter` derives buy/sell from its point).

- Born: sell-to-open via `optlimiter` (contract picked by injected selector).
  The contract is re-selected at each session start while the opening order
  hasn't filled.
- Lives (v1): holds until settlement. Past expiry the position is
  *settling*: waiting for broker truth.
- Dies: exactly one terminal outcome — `expired` | `assigned` (broker
  truth) | `unfilled` (the greeler abandoned it before its opening order
  ever filled) — recorded truthfully, forever. Never owns collateral or
  stock.
- v2: a `RollPolicy` decides, each check, whether to hold, close
  (buy-to-close — profit-take or loss-close), or roll (one atomic broker
  order); adds the `closed` outcome.

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
  plus re-pricing: `reprice-step` (fraction of the spread, default 0.20)
  and `reprice-interval` (default 2 minutes). v2 adds `roll-dte`,
  `profit-take-pct`, `loss-close-multiple`.
- **Layer 2 — interfaces** (the real contract):

  ```go
  // ContractSelector picks the next contract to open for one position,
  // with the minimum per-share premium worth selling it for.
  type ContractSelector interface {
      Select(ctx context.Context, chain []*gobs.OptionContract,
          constraint *Constraint) (*Selection, error)
  }
  ```

  v2 adds `RollPolicy` (hold | close | roll). New strategies are new
  implementations registered by name; compiled-in. The greeler persists the
  chosen name and the resolved knob values, so a restart rebuilds the same
  selector.
- **Layer 3 — expression DSL**: deferred until Layer 2 proves insufficient.

---

## Design decision log

1. **optlimiter reuses limiter.Limiter** via a Product adapter (2026-07-06).
   Superseded by #9.
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
8. **v1 holds positions to settlement; `RollPolicy` is v2.** Closes
   (profit-take, loss-close) and rolls are deferred. Holding to settlement
   fits greel: strikes sit at the levels, usually out of the money, and
   assignment is how shares get into the grid.
9. **optlimiter is its own option order state machine** (supersedes #1).
   `Limiter` holds an order only near its fixed price, so it can't re-price
   — routine for options — and won't place a buy below market. Changing it
   would mean reworking the loop every production grid order runs through,
   in an untested package; a separate machine reuses `idgen` and
   `exchange.SimpleOrder` and leaves stock trading untouched.

---

## Module structure

| Module | Package / files | Responsibility |
|---|---|---|
| **persistence** | `gobs/optlimiter.go`, `gobs/optpos.go`, `gobs/greeler.go`, `gobs/greelladder.go` | each split into creation-time `Config` and trading-written `Progress`: `GreelerState` (config; progress `Epochs`), `OptPositionState` (exchange + underlying; progress legs + outcome/assignment fact), `OptLimiterState` (order intent; progress orders), `GreelLadderState` (config + greeler UIDs; progress empty) |
| **optlimiter** | `optlimiter/optlimiter.go` | `OptLimiter`: option order state machine — re-pricing, never two live orders, crash-safe client IDs |
| **optpos** | `optpos/position.go`, `optpos/selector.go` | `Position` lifecycle incl. broker settlement checks, re-selection, `Abandon`; `ContractSelector`, presets (`RollPolicy` in v2) |
| **greeler** | `greeler/greeler.go`, `greeler/run.go`, `greeler/options.go` | Per-level posture derivation; mode flips (buffer/hysteresis/dwell); assignment attribution; `SetOption` (retire/freeze) |
| **greelladder** | `greelladder/greelladder.go`, `greelladder/run.go` | Spawning and driving greelers; sibling contract exclusion; stock reconciliation (alert-only); risk gates (TODO); aggregate status |

Dependency and implementation order (each stage independently runnable):

```
gobs → optlimiter → optpos → greeler → greelladder
```

Each module runs the full workflow cycle: story → execution state → function
skeletons → implementation (~200-line chunks) → tests → coverage, with
developer checkpoints at every step.

---

## Open items (decide during module design)

1. **Assignment detection** — *resolved* (optpos-story): each position asks
   the broker how its own contract stands; only broker truth writes
   `expired`/`assigned`, via `OptionsExchange.GetOptionsSettlement` (added).
2. **Accounting model** — *resolved* (accounting-story): `gobs.Summary` is stock-centric. Premium, assignment
   cost basis, and per-greeler netting (e.g., CC sale recorded at greeler
   level against level cost bases) need an extended P&L model
   (`GetSummary`, `BudgetAt`, status/summary subcommands). Until then,
   greeler and ladder still implement minimal `Actions`/`BudgetAt`/
   `GetSummary`, since `trader.Trader` requires them.
3. **Runtime plumbing** — *resolved*: the greeler gets its
   `OptionsExchange` from `rt.Exchange` and passes it down; option orders
   take it directly rather than a `trader.Runtime` (optpos-story).
4. **Market hours**: options trade regular hours only. Option orders are
   regular-session day orders and sleep outside the session (optlimiter-
   story); reuse etrade's market-hours handling for that.
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
11. **Keyspace isolation gaps** — *no longer applies*: option orders no
    longer use the `limiter` package (decision #9).
12. **Option order execution** — *resolved* (optlimiter-story): an option
    order state machine that places immediately and re-prices on a timer.
13. **`OpenOptionsRollProduct`/`OptionsRollProduct`** — *resolved*: removed
    from `exchange/api.go` until v2, so v1 `OptionsExchange` implementations
    don't need them (optlimiter-story decision #8).

---

## Story Placeholders

- [x] `gobs` story — drafted in [gobs-story.md](gobs-story.md), reviewed; implemented
- [x] `optlimiter` story — rewritten in [optlimiter-story.md](optlimiter-story.md) as the option order state machine, reviewed
- [x] `optpos` story — drafted in [optpos-story.md](optpos-story.md), reviewed (design-review items resolved)
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
│   ├── optlimiter.go        ← OptLimiterState
│   ├── optpos.go            ← OptPositionState, AssignmentFact
│   ├── greeler.go           ← GreelerState, WheelKnobs, GreelEpoch
│   └── greelladder.go       ← GreelLadderState
├── optlimiter/
│   └── optlimiter.go        ← option order state machine
├── optpos/
│   ├── position.go          ← single written-option position lifecycle
│   └── selector.go          ← ContractSelector, presets
├── greeler/
│   ├── greeler.go           ← Greeler struct, trader.Trader impl, Save/Load
│   ├── run.go               ← derivation loop, mode flips, assignment attribution
│   └── options.go           ← SetOption handlers
└── greelladder/
    ├── greelladder.go       ← GreelLadder struct, trader.Trader impl
    └── run.go               ← drive greelers, reconciliation
```
