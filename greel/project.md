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
 │                                 positions poll, risk gates, aggregation
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

1. **Derivation over stored state.** A greeler stores almost nothing: a
   dwell-clock timestamp and an append-only event log (assignments/terminal
   outcomes, with idempotency keys). Mode, per-level cycle position, and all
   inventory are derived each iteration from spot price, limiter fill records,
   and the event log. There is no mode flag, no contribution list, no ledger.

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
  must stay past the threshold for `d` before a flip. The dwell clock's
  crossing timestamp is one of the two pieces of persisted greeler state.
- **When an option position is open, all stock limiters are dark** (canceled),
  including levels holding spillover shares. No resting spillover sells.
- Far-away greelers (spot moved multiples away) write near-zero-premium
  contracts pinned at their own levels — their collateral cannot chase strikes
  toward spot. This is intentional: the cost of staying in the game until
  price returns to that band.

## Mode-flip mechanics

Flip to wheel mode: cancel all stock limiters → await cancel confirmations →
verify qualification (all-cash or all-shares) → open `optpos.Position`, all
journaled in greeler state. Flip to grid mode: position must be terminal
first; then stock limiters are created per derived posture. A crash mid-flip
is harmless: restart re-derives desired mode, re-issues idempotent cancels,
and converges.

## Assignment handling (crash-safe by construction)

Assignment handover involves **no exchange mutation** — the broker already
moved the stock; ours is pure bookkeeping:

1. **Detect**: GreelLadder's positions poll observes the assignment (option
   position gone / stock delta; event key = broker transaction ID, or a
   deterministic synthetic key from contract ID + expiry).
2. **Apply in one KV transaction**: append the event (idempotency key) to the
   owning greeler's log. Nothing else — no state to patch, since everything
   downstream is derived.
3. **Converge**: the greeler's next iteration derives the new posture and
   creates the appropriate limiters, which place real orders through
   limiter's existing idempotent machinery.

Crash windows: before commit → the poll re-detects from broker truth and
reprocesses; after commit → children resume idempotently; duplicate poll
delivery → rejected by the idempotency key.

Post-assignment postures (derived, not restored):

- **CSP assigned** (100 shares arrive at strike K): the portfolio is now
  all-stock, no cash. Derived grid posture: sell limiters at the levels above
  spot (deterministic allocation of exactly 100 shares across levels); buy
  limiters below spot arm **as sells fill and free cash**. No opportunity is
  lost — the "waiting" buys require cash that does not yet exist.
- **CC assigned** (100 shares leave at strike K ≥ every level's sell price):
  recorded as completed sales at K against the levels (deterministic
  lowest-level-first allocation — a rule, not state). Portfolio is all-cash;
  buy limiters below spot arm immediately, or a new CSP arms if spot is far
  above (zone decision).

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
- Lives: track the position — profit-take, close-at-threshold, expiry
  countdown. **Rolls are internal**: a `RollPolicy` may replace the contract
  (buy-to-close + sell-to-open) within the same position chain; a **nil
  RollPolicy disables rolling** (greel passes nil so nothing happens behind
  the supervisor's back).
- Dies: exactly one terminal outcome — `expired` | `assigned` | `closed` —
  recorded truthfully, forever. Never owns collateral or stock.

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

  // RollPolicy decides whether/how to roll an open position. Nil disables.
  type RollPolicy interface { ... }
  ```

  New strategies are new implementations registered by name; compiled-in.
- **Layer 3 — expression DSL**: deferred until Layer 2 proves insufficient.

---

## Design decision log

1. **optlimiter reuses limiter.Limiter** via a Product adapter (2026-07-06).
2. **Exchange interface keeps 4 option order verbs** — broker-validated
   intent; explicit open/close turns stale-state bugs into order rejections.
3. **No ledger module.** Early designs centered on a global cash/lot
   ownership ledger. Per-greeler ownership plus all-or-nothing qualification
   plus derivation-over-state eliminated it; the surviving descendant is the
   single-transaction event append per greeler.
4. **Greeler is built on limiters directly, not loopers** (2026-07-07).
   `Looper`'s control flow derives purely from its own limiters' fills
   (`looper/run.go` bought/sold derivation), so it cannot absorb external
   events (assignment granting/removing inventory) without state forgery or
   retire-and-recreate churn. The cycle logic is ~90 lines of derivation —
   the complexity worth reusing lives in `Limiter`. Greeler's per-level cycle
   is the same derivation generalized to merge two event sources: limiter
   fills and recorded assignment events. The `looper` package stays untouched.
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
| **persistence** | `gobs/greel.go` | `GreelerState`, `PositionState`, event records with idempotency keys |
| **optlimiter** | `optlimiter/product.go`, `optlimiter/optlimiter.go` | `OptionsProduct`→`Product` adapter; `OptLimiter` wrapping `limiter.Limiter` (units, keyspace) |
| **optpos** | `optpos/position.go`, `optpos/selector.go` | `Position` lifecycle; `ContractSelector`, `RollPolicy`, presets |
| **greeler** | `greeler/greeler.go`, `greeler/run.go`, `greeler/options.go` | Per-level posture derivation; mode flips (buffer/hysteresis/dwell); assignment bookkeeping; `SetOption` (retire/freeze) |
| **greelladder** | `greelladder/greelladder.go`, `greelladder/run.go` | Spawning greelers; positions poll → event routing; risk gates; aggregate status/P&L |

Dependency and implementation order (each stage independently runnable):

```
gobs/greel.go → optlimiter → optpos → greeler → greelladder
```

Each module runs the full workflow cycle: story → execution state → function
skeletons → implementation (~200-line chunks) → tests → coverage, with
developer checkpoints at every step.

---

## Open items (decide during module design)

1. **Assignment detection**: how etrade surfaces assignments/exercises —
   positions-delta poll vs transaction records; early assignment (around
   ex-dividend) must be detected promptly. May extend `OptionsExchange`.
2. **Accounting model**: `gobs.Summary` is stock-centric. Premium, assignment
   cost basis, and per-greeler netting (e.g., CC sale recorded at greeler
   level against level cost bases) need an extended P&L model
   (`GetSummary`, `BudgetAt`, status/summary subcommands).
3. **Runtime plumbing**: `trader.Runtime` carries a single `rt.Product`; a
   greeler spans the stock product plus option contract products, so it
   manages product handles for its children itself.
4. **Market hours**: options trade regular hours only; reuse etrade's
   extended-hours handling to avoid overnight thrash.
5. **Corporate actions**: dividends (early-assignment risk), earnings dates
   (optional entry skip), splits (detect and freeze at minimum).
6. **Risk limits / kill switch**: max open contracts, max assignment
   exposure, per-greeler freeze (`freeze=grid|wheel|all`), retire semantics
   (stop new entries, let positions finish).
7. **Paper trading / simulation**: a simulated `OptionsExchange` for engine
   testing and future backtesting.
8. **Observability**: per-greeler status (mode, band, open strike, collected
   premium); messenger notifications on assignment/roll/terminal events.
9. **TODO (deferred)**: deterministic allocation rule details for CC-assigned
   share attribution (lowest-level-first proposed); CSP disposal-sell level
   allocation for the assigned 100 shares.

---

## Story Placeholders

- [ ] `gobs/greel.go` story — TBD
- [ ] `optlimiter` story — TBD
- [ ] `optpos` story — TBD
- [ ] `greeler` story — TBD
- [ ] `greelladder` story — TBD

---

## File Structure

```
tradebot/
├── greel/
│   └── project.md           ← this file
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
    └── run.go               ← spawn/supervise greelers, positions poll
```
