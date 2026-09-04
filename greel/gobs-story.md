# Story: `gobs/greel.go` — serializable state for the greel job tree

Companion to [project.md](project.md). This module defines every struct that
greel persists in the KV store. It is the foundation stage: everything later
(`optlimiter`, `optpos`, `greeler`, `greelladder`) loads and saves only types
defined here.

The guiding principle (project.md, design principle 1) is **derivation over
stored state**: each scenario below names what is persisted and, just as
importantly, what is deliberately *not*. A field earns its place only if it
cannot be re-derived from spot price, limiter fill records, or a position's
own reported outcome.

---

## Scenario walkthrough

### 1. A greeler is born

The ladder (or a subcommand, for standalone use) creates a greeler with its
configuration: exchange, underlying product, its slice of adjacent grid levels
(a buy/sell price pair and size per level, reusing `gobs.Pair`), and its zone
parameters (`g`, `f`, `h` as percentages, dwell `d` as a duration).

**Persisted:** all of the above — configuration is static state, set once at
creation. Zone parameters are copied into the greeler (not referenced from the
ladder) so a greeler is fully self-describing and standalone-runnable. Also
seeded here: `Epochs` starts with one entry, `{Mode: "grid", StartAt:
creation time}` — see scenario 3a.

### 2. Grid mode: levels trade through limiters

Each level cycles buy → sell → buy… through successive `limiter.Limiter`
children. A limiter is single-shot, so each cycle step is a new limiter; the
greeler must be able to find every limiter a level has ever owned, because the
level's cycle position and inventory are *derived* by folding over those
limiters' fill records (merged with reported assignment facts, scenario 5).

**Where that list lives moved once already this story** (see scenario 3a):
a level's limiter history is not one flat list spanning the greeler's whole
life — it's scoped per grid epoch, since a level can pass through several
separate grid stints over time, interrupted by wheel excursions. `GridLevels`
holds only the static per-level config (`Pair`); the append-only
limiter history for level *i* during grid epoch *j* is
`Epochs[j].LevelLimiterIDs[i]`. A level's *entire* history, if ever needed,
is the concatenation of that slice across every grid epoch in order — the
same walk over `Epochs` that scenario 5's assignment fold already does,
just accumulating a second thing per grid entry instead of a first thing
per wheel entry.

**Persisted:** `GridLevels`, static config only (`Pair` per level, reusing
`WallerStateV2.TradePairs`' shape: `[]*Pair`, no wrapper struct needed once
the dynamic field is gone). Per grid epoch, `LevelLimiterIDs [][]string`
— index-aligned with `GridLevels` — append-only, and frozen once that epoch
ends (the next `Epochs` entry starts a fresh, empty one).
**Not persisted:** cycle position, inventory, "bought at" markers — all
derived, same as before.

### 3. The dwell clock ticks toward a mode flip

Spot crosses the far threshold `S·(1±f)` and must stay there for dwell `d`
before the greeler flips to wheel mode (and symmetrically back past the
hysteresis band toward grid mode). The crossing timestamp cannot be derived —
price history is not stored — so it is one of the two pieces of true greeler
state named in the design.

**Persisted:** the pending flip direction and the wall-clock time spot first
crossed the threshold; zeroed whenever spot retreats. These fields live on
`Epochs[last]` — the currently open epoch — not as separate top-level
greeler fields; see scenario 3a for why watching for its own termination is
the current epoch's job, structurally, not the greeler's.
**Not persisted:** the current mode *as a control-flow fact* — that stays
derived each iteration from spot plus open children (a live option position
⇒ wheel; live limiters ⇒ grid), exactly as before. What changes below is
that completed transitions get an append-only journal for reporting, which
is a different thing from trusting a stored flag to drive behavior.

### 3a. Mode history: `Epochs`, a journal of transitions already derived

The dwell clock (above) says *when a flip is pending*; nothing so far
records *when a flip actually completed*, or gives an easy way to answer
"how much of this greeler's life has been spent in wheel mode" or "walk its
transitions in order." Reconstructing that from scratch would mean loading
every position and correlating it against every level's limiter timestamps
— exactly the kind of scatter-read the earlier `Events`-vs-position
discussion argued against.

**The fix folds into the same list that already tracked positions.**
`PositionIDs []string` (the flat, greeler-wide list from scenario 4/5)
becomes `Epochs []*GreelEpoch`: each
entry marks one mode period — `Mode` ("grid" | "wheel"), `StartAt`, and, for
wheel periods only, the `PositionID` that covers it. This isn't an addition
alongside `PositionIDs`, it's a replacement that carries strictly more
information: filtering `Epochs` for `Mode == "wheel"` and collecting
`PositionID` reproduces exactly what `PositionIDs` gave before.

- **Time in mode and transition traversal become a single pass:** walk
  `Epochs` once; each entry's duration is the next entry's `StartAt` (or
  `now`, for the last one) minus its own — no `EndAt` field.
- **The real invariant isn't "every field write-once" — it's "closed
  epochs are frozen; only the last (open) epoch may still be written
  to."** `StartAt`, `Mode`, and `PositionID` happen to be write-once
  under that rule, and `LevelLimiterIDs` (below) is monotonic — grows
  only while its epoch is last. The pending-flip clock is the one field
  that doesn't fit either pattern, which is exactly why it belongs
  nested in the epoch rather than floating at the top of the greeler:
  `PendingFlip`/`PendingFlipAt` (scenario 3) has no meaning independent
  of "the epoch that might be about to end" — it's that epoch's own
  bookkeeping for watching its own termination condition, not a
  greeler-wide fact. It resets freely while its epoch is last (spot
  crosses, retreats, crosses again), same as before; once a new epoch
  gets appended, whatever it last held is simply frozen — in effect a
  small piece of history explaining *why* that epoch ended, not just
  when. It's also mode-symmetric in a way `PositionID`/
  `LevelLimiterIDs` aren't: the dwell clock gates flips in both
  directions, so it's meaningful on either kind of epoch, not scoped to
  one `Mode`.
- **The greeler is born already inside a grid epoch** (scenario 1) — seeded
  at creation with one `{Mode: "grid", StartAt: creation time}` entry, so
  `Epochs` is never empty and "current mode" is always `Epochs[last].Mode`
  by construction, no special-casing an empty list.
- **Crucial scoping rule, to avoid reintroducing what the last redesign
  removed:** `Epochs` is a **journal of transitions the greeler has
  already derived**, not the authority that decides current mode. The
  authority is still exactly what scenario 3 (and design principle 1) say
  it is: cross-check the last wheel epoch's `PositionID` against that
  position's own `Outcome`. Appending the *next* epoch happens on the
  greeler's own next iteration, once it notices (by loading that position)
  that a transition occurred — not atomically with the position's own
  terminal write. That keeps position termination a single-record
  transaction, exactly as decided in scenario 5; a crash between "position
  went terminal" and "greeler appends the next epoch" leaves `Epochs` one
  entry short for at most one iteration — a reporting lag, self-healing on
  the next run, never a correctness bug (design principle 2's "crash
  mid-flip is harmless," unchanged).
- **Grid epochs are self-contained the same way wheel epochs are.** A wheel
  epoch's `PositionID` points at everything that happened during it; a grid
  epoch's `LevelLimiterIDs` does the same job for the levels — each grid
  entry owns its own per-level limiter lists directly (index-aligned with
  `GridLevels`), appended fresh when the epoch begins and frozen when it
  ends. Nothing is copied: a limiter UID is recorded in exactly the one
  grid epoch it was created during, never duplicated into a separate flat
  per-level list. `GridLevels` itself shrinks to pure static config (`Pair`
  only) as a result — see scenario 2.

### 4. Wheel mode: a written-option position opens

After qualification, the greeler opens an `optpos.Position` (CSP or CC). The
position is a component whose record is the append-only order chain — the
`Legs` list is the whole point of this scenario, so it's worth spelling out
its shape precisely.

**The leg chain has a fixed grammar**, following directly from project.md's
lifecycle description ("born via sell-to-open… rolls are internal… dies with
exactly one terminal outcome") — corrected here from the first draft to
reflect that **a roll executes as one atomic broker order**, not as a
separate buy-to-close followed by a separate sell-to-open:

```
sell-to-open   roll*         (buy-to-close)?
└─ opens the   └─ each roll  └─ optional: only present
   position       is ONE       if the position is
                   order,       voluntarily closed
                   one fill,
                   replacing
                   the contract
```

This is a genuine broker fact, not a modeling choice: E*TRADE (and brokers
generally) submit a roll as a single multi-leg order — one client/server
order ID, one net-credit-or-debit fill — closing the old contract and
opening the new one together. Modeling it as two independent legs would
claim a bookkeeping state (old contract closed, new one not yet open) that
can never actually occur, and would need a pairing rule to say which
buy-to-close belongs with which sell-to-open. Collapsing a roll to one leg
removes both problems and is simply more accurate:

- **One leg, one order, one fill.** `OptLimiterStateV1` gains `Intent =
  "roll"` and a `PriorContractID` field alongside the existing
  `ContractID` (which for a roll leg names the *new*, post-roll contract).
  The wrapped `limiter.Limiter` still records exactly one order and one
  fill/price — a roll's net premium is just that fill's price, credit or
  debit, no different in shape from any other leg's fill.
- **No pairing/matching logic, ever.** Walking `Legs` in order and reading
  each optlimiter's `Intent` directly tells you whether that step opened,
  rolled, or closed the position — `roll` is unambiguous on its own, unlike
  the old two-leg encoding where a bare `buy-to-close` needed a lookahead
  to know if it was "half of a roll" or "the final close."
- **No mid-roll transient state to tolerate.** Design principle 2 already
  promises a crash mid-flip is harmless because "the greeler cycles by
  re-deriving desired posture, never by remembering where it was." With an
  atomic roll, there is no broker-truth window where the position has zero
  live contracts — the fold's "current contract" (last leg's `ContractID`)
  is always live, simplifying that guarantee rather than merely satisfying
  it.
- **Assigned or expired positions still have no closing leg** — unchanged
  from the first draft: both are things that happen *to* the position from
  outside (no exchange mutation for assignment; expiry is a calendar fact),
  so the terminal outcome is recorded separately (below), not as a leg.
- **The top-level `Contract` field is a cache of the current contract**,
  refreshed on open and on every roll — unchanged from the first draft; the
  authoritative contract for leg *i* is always that leg's own `optlimiter`
  record.
- **`Legs` stays `[]string` of optlimiter UIDs**, no separate leg struct —
  unchanged from the first draft; nothing about the roll fix reopens that
  question, since intent (now including `roll`) still lives entirely on the
  referenced `optlimiter` record.

**Exchange-layer gap, flagged not solved here:** `exchange.OptionsProduct`
(exchange/api.go) currently exposes only the four single-contract verbs
(`LimitBuyToOpen`/`SellToOpen`/`BuyToClose`/`SellToClose`); there is no
combo/multi-leg order primitive to place an atomic roll against. Placing
one will need either a new `OptionsExchange` method taking both contract
IDs and a net price, or a synthetic two-contract product that `optlimiter`
can still wrap through the existing `limiter.Limiter`/`exchange.Product`
shape (mapping net-credit/net-debit onto `LimitSell`/`LimitBuy`, the same
way `optlimiter` already remaps the 4 option verbs onto 2). This is a
mechanism question for the `optlimiter` module story — the persisted shape
here (`Intent = "roll"`, `ContractID` + `PriorContractID`, one wrapped
limiter) is agnostic to which mechanism wins.

**Persisted:**
- On the greeler: a wheel entry in `Epochs` (scenario 3a) carrying the
  position's UID (the last wheel epoch's position may be open; all earlier
  ones are terminal).
- Per position: a cached snapshot of the *current* contract, the ordered
  `Legs []string` (optlimiter UIDs), terminal outcome + timestamp, and (for
  assignment) the fact of what happened — see scenario 5.
- Per optlimiter (own record, own keyspace — see below): intent (including
  `roll`), contract identity (`ContractID` + `PriorContractID` for rolls),
  contract size, and the wrapped limiter's own state.

**Not persisted:** collected premium, position P&L — derived from the legs'
fill records. Put-vs-call side — derived from the contract. Roll boundaries —
derived by reading each leg's `Intent` directly (`== "roll"`); no pairing or
lookahead needed.

### 5. Assignment lands (the crux)

Reframed from the first draft: **the position was handed collateral
responsibility when it opened** (a CSP is backed by the levels' cash, a CC
by the levels' stock, per design principle 3 — the greeler still *owns* that
collateral, but the position is the one thing watching what happens to it),
**so the position is the one that reports back what happened to it.** This
is exactly project.md's own phrase for a terminal fact — "assigned: +100
shares at strike K" — taken literally: that whole clause, shares and strike
included, is the position's outcome, not a separate record correlated to it
from outside.

The ladder's positions poll detects an assignment and applies it in **one
KV transaction, touching one record — the position's own**: set
`Outcome = "assigned"` plus the fact (shares delta, strike, and a key for
audit/idempotency). `Outcome` starting empty and ending non-empty *is* the
idempotency guard — a duplicate poll delivery finds it already set and
no-ops. Nothing else is patched; the greeler's posture is derived, next
iteration, by walking `Epochs`' wheel entries and folding over whichever
referenced positions report `Outcome == "assigned"` — the same "parent
holds an ordered UID list, child holds the facts" shape already used for
grid epochs' `LevelLimiterIDs` and `OptPositionStateV1.Legs` above, now
applied consistently a third time. No greeler-level event log is needed at all:
there is nothing left for it to hold that the position doesn't already own.

**Persisted (on `OptPositionStateV1`, alongside `Outcome`/`OutcomeAt`):** an
`Assignment` fact, present only when `Outcome == "assigned"` — a key
(broker transaction ID, or a deterministic synthetic key from contract ID +
expiry, for audit and idempotency), the share delta (+100 for a CSP
assignment, -100 for a CC assignment), and the strike price.
**Not persisted:** any resulting inventory/cash adjustment, and no separate
greeler-owned copy of the fact. The position's report *is* the adjustment;
the greeler's fold applies it fresh every iteration by reading it off
`Epochs`.

### 5a. Sequencing: `Legs` freezes when `Outcome` is set

With the fact living on the position itself, sequencing is simpler than the
first draft's two-record version: there is exactly one append-only sequence
per position (`Legs`), and one terminal marker (`Outcome`/`OutcomeAt`/
`Assignment`) on that same record — no second sequence to correlate against.

- **`Legs` is a strict, single-threaded chain.** By the grammar in scenario
  4, a position has exactly one *live* contract at any time: the one named
  by its most recent leg. There is never a moment with two live contracts
  (legs are sequential, not concurrent) or — thanks to atomic rolls — a
  moment with zero live contracts between a close and a reopen.
- **`Legs` freezes once `Outcome` is set.** Whether termination is an
  assignment (`Outcome` and `Assignment` set together) or expiry/voluntary
  close (`Outcome` set alone), no further legs are ever appended
  afterward — enforced operationally by `optpos` (a terminal position
  places no more orders), not by the schema. This is what makes "the
  position's current contract" and "the contract the terminal fact is
  about" provably the same contract: whichever contract `Legs`' last entry
  names when `Outcome` gets set.
- **The greeler never needs to ask "when."** Because the fact lives with
  the chain it terminates, on the same record, in the same transaction,
  there's no cross-record ordering question left to answer — unlike the
  first draft, where an event on a different record (the greeler) needed an
  explicit argument for why it always came after the position's last leg.

### 6. Crash and restart

Restart loads the greeler record, re-derives mode and per-level posture, and
resumes children by UID. Nothing in any struct is a "phase pointer" that a
missed fill or assignment could invalidate. This scenario adds no fields — it
is the test that the previous five persisted the right (minimal) set.

### 7. The ladder above

`GreelLadder` is a thin spawner/aggregator (as `Waller` is to `Looper`):
it persists its children's UIDs and its own configuration. Risk-gate and
budget plumbing details are deferred to the `greelladder` story; the struct
here reserves only the obvious fields.

---

## Proposed structs

Follows house style: version-wrapped (`XState { V1 *XStateV1 }`) with an
`Upgrade()` method from day one, `decimal.Decimal` for money/size,
`Options map[string]string` on job states for `SetOption` support.

```go
// Copyright (c) 2026 BVK Chaitanya

package gobs

import (
    "time"

    "github.com/shopspring/decimal"
)

// OptLimiterState persists one option order: a limiter.Limiter wrapped with
// a fixed open/close intent and contract-unit scaling. Lives in
// "/optlimiters/" so "/limiters/"-scanning tasks skip it. The wrapped
// limiter is a *reference* (its own LimiterState record, saved under this
// same "/optlimiters/" keyspace via a limiter-package keyspace override —
// see the embed-vs-reference decision below), not an embedded copy.
type OptLimiterState struct {
    V1 *OptLimiterStateV1
}

type OptLimiterStateV1 struct {
    // ContractID is the contract this order acts on. For Intent ==
    // "roll" this is the *new*, post-roll contract; PriorContractID
    // names the contract it replaces in that same atomic order.
    // PriorContractID is empty for every other intent.
    ContractID      string
    PriorContractID string
    ExchangeName    string

    // Intent is one of "sell-to-open", "buy-to-close", "buy-to-open",
    // "sell-to-close", "roll"; fixed at construction. "roll" is a
    // single atomic broker order (one fill, one net credit/debit) that
    // closes PriorContractID and opens ContractID together — never
    // modeled as separate close/open legs.
    Intent string

    // ContractSize scales contract/premium units at the wrapper
    // boundary (typically 100).
    ContractSize decimal.Decimal

    // LimiterID is the UID of the wrapped limiter.Limiter's own record,
    // stored alongside this one under "/optlimiters/" (not
    // "/limiters/"). Loaded via limiter.Load the same way a Looper
    // loads its buy/sell limiters. For "roll", the wrapped limiter
    // trades a synthetic two-contract product, not a plain
    // OptionsProduct — see the exchange-layer gap noted in scenario 4.
    LimiterID string
}

// OptPositionState persists one written-option position from open to its
// single terminal outcome. Lives in "/optpositions/".
type OptPositionState struct {
    V1 *OptPositionStateV1
}

type OptPositionStateV1 struct {
    Options map[string]string

    ExchangeName string
    Underlying   string

    // Contract is a cache of the *current* contract this position has
    // written — refreshed on open and on every roll, not a second
    // source of truth. A roll can change the contract mid-chain; the
    // authoritative contract for any given leg is whatever that leg's
    // own optlimiter record says. Pricing fields are point-in-time and
    // not authoritative.
    Contract *OptionContract

    // Legs is the append-only, ordered list of optlimiter UIDs:
    // sell-to-open, then zero or more "roll" legs (each one atomic
    // broker order replacing the contract), then an optional final
    // buy-to-close if the position was voluntarily closed. Intent,
    // contract, and fill data for each leg live on its own
    // OptLimiterState record, not duplicated here.
    Legs []string

    // Outcome is empty while open; exactly one of
    // "expired" | "assigned" | "closed" once terminal.
    Outcome   string
    OutcomeAt time.Time

    // Assignment is set iff Outcome == "assigned" — the position's own
    // report of what happened to the collateral it was holding.
    // Setting Outcome and Assignment together, in one transaction on
    // this record, is the whole "apply" step; Outcome starting empty
    // and ending non-empty is the idempotency guard, so no separate
    // greeler-level event log is needed.
    Assignment *AssignmentFact
}

type AssignmentFact struct {
    // Key is the audit/idempotency key: the broker transaction ID, or
    // a deterministic synthetic key (contract ID + expiry).
    Key string

    // Shares is the stock delta (+100 for a CSP assignment, -100 for a
    // CC assignment); Price is the strike it happened at.
    Shares decimal.Decimal
    Price  decimal.Decimal
}

// GreelerState persists one greeler: static configuration, the dwell
// clock, and Epochs (mode-transition journal + position UIDs). Everything
// else — including assignment history — is derived by walking Epochs and
// reading each referenced position's own outcome. Lives in "/greelers/".
type GreelerState struct {
    V1 *GreelerStateV1
}

type GreelerStateV1 struct {
    // --- Config: static after creation, except Options (runtime-
    // mutable via SetOption/freeze, but not trading-derived — it
    // doesn't cleanly fit "state" either). ---

    Options map[string]string

    ProductID    string
    ExchangeName string

    // GridLevels are the adjacent grid levels this greeler owns; sizes
    // sum to >= 100 shares (one contract). Purely config, no dynamic
    // field, so no wrapper struct is needed (mirrors
    // WallerStateV2.TradePairs). Grid-defined, but also read by wheel
    // mode: optpos anchors CSP/CC strikes to these same levels
    // (project.md: "strike is largely anchored to the greeler's
    // levels"). Per-level trading history lives inside whichever grid
    // Epoch it happened in, not here — see below.
    GridLevels []*Pair

    // Zone geometry, percentages of spot (project.md): grid half-width,
    // far threshold, hysteresis; dwell is the threshold *duration* the
    // clock in Epochs compares against. Static config, unlike the
    // clock itself, which is why it stays here rather than moving into
    // Epochs alongside PendingFlip/PendingFlipAt.
    GridPct       decimal.Decimal
    FarPct        decimal.Decimal
    HysteresisPct decimal.Decimal
    DwellTime     time.Duration

    // --- State: the one dynamic field. Everything else behavioral
    // (mode, posture, inventory, assignment history) is derived, not
    // stored — project.md's own claim that a greeler persists "almost
    // nothing: a dwell-clock timestamp and an append-only event log"
    // has fully collapsed into this single field (scenario 3a). ---

    // Epochs is the append-only journal of mode periods, seeded at
    // creation with one {Mode: "grid", StartAt: creation time} entry
    // (scenario 3a) so it is never empty. It replaces a bare
    // PositionIDs list: filtering for Mode == "wheel" and collecting
    // PositionID reproduces the position history, while StartAt gives
    // transition timing for free. Each grid entry also owns that
    // stint's per-level limiter history (LevelLimiterIDs) — the only
    // place that history is recorded; GridLevels above holds config
    // only. It is a journal of transitions the greeler has already
    // derived, not the authority for current mode — see scenario 3a's
    // scoping rule.
    Epochs []*GreelEpoch
}

type GreelEpoch struct {
    // Mode is "grid" | "wheel".
    Mode string

    // StartAt is when this epoch began. No EndAt: an epoch's duration
    // is the next epoch's StartAt (or now, for the last one) minus its
    // own. Write-once, like PositionID below — unlike PendingFlip/
    // PendingFlipAt and LevelLimiterIDs, which mutate while this is
    // the last (open) epoch and freeze once it isn't (scenario 3a: the
    // real invariant is "closed epochs are frozen," not "every field
    // write-once").
    StartAt time.Time

    // PositionID is set iff Mode == "wheel" — the position covering
    // this epoch. Empty for "grid" epochs.
    PositionID string

    // PendingFlip/PendingFlipAt track a not-yet-committed transition
    // attempt: which flip direction ("wheel-put" | "wheel-call" |
    // "grid") spot is dwelling toward, and when it first crossed the
    // triggering threshold. Meaningful, and mutable, only while this
    // is the last epoch — zeroed whenever spot retreats before
    // DwellTime elapses, re-armed on the next crossing. Meaningful on
    // either Mode, unlike PositionID/LevelLimiterIDs: the dwell clock
    // gates flips in both directions. Once a new epoch is appended,
    // whatever these last held is frozen — a record of what finally
    // triggered this epoch's own termination.
    PendingFlip   string
    PendingFlipAt time.Time

    // LevelLimiterIDs is set iff Mode == "grid": LevelLimiterIDs[i] is
    // this epoch's append-only limiter history for GridLevels[i], index-
    // aligned with GridLevels. Seeded as one empty slice per level when
    // the epoch begins (nil is fine — append handles a nil slice) and
    // frozen once the epoch ends; nothing is ever copied out of it, so
    // a limiter UID exists in exactly one place: the one epoch it was
    // created during. Empty for "wheel" epochs — no level ever gets a
    // new limiter while an option position is open (project.md: "all
    // stock limiters are dark").
    LevelLimiterIDs [][]string
}

// GreelLadderState persists the ladder job: configuration plus child
// greeler UIDs. Lives in "/greelladders/".
type GreelLadderState struct {
    V1 *GreelLadderStateV1
}

type GreelLadderStateV1 struct {
    Options map[string]string

    ProductID    string
    ExchangeName string

    GreelerIDs []string
}
```

### Registration and keyspaces

- `gobs.NewByTypename` gains cases for `OptLimiterState`,
  `OptPositionState`, `GreelerState`, `GreelLadderState`. `LimiterState`
  is already registered — no new case needed for the wrapped limiter.
- Keyspaces: `/optlimiters/`, `/optpositions/`, `/greelers/`,
  `/greelladders/` (constants live in their owning packages, mirroring
  `limiter.DefaultKeyspace`).
- The wrapped `limiter.Limiter` inside each `optlimiter.OptLimiter` also
  lives under `/optlimiters/` — not `/limiters/`. Today `limiter.Save`/
  `Load` hardcode `DefaultKeyspace` ("/limiters/"), so this needs one
  small, additive change to the `limiter` package: an optional keyspace
  override (e.g. a `New(..., WithKeyspace(ks))` option, defaulting to
  `DefaultKeyspace` for every existing caller). `cleanUID` already
  special-cases multiple prefixes (`/wallers/`, `/limiters/`,
  `/loopers/`); adding `/optlimiters/` to that list is the matching
  other half. See the embed-vs-reference decision below for why this is
  worth the touch.

### Deliberately absent (derived, per design principle 1)

- A live "current mode" flag — control flow still derives it each
  iteration (scenario 3). `Epochs`' per-entry `Mode` (scenario 3a) is a
  different kind of field: an append-only journal of transitions already
  derived, never itself consulted to decide what to do next.
- An `EndAt` on `GreelEpoch` — computed at read time from the next entry's
  `StartAt` (or now), keeping every field write-once.
- A flat, top-level `LimiterIDs` per level spanning the greeler's whole
  life — it would duplicate exactly what each grid epoch's
  `LevelLimiterIDs` already records; a level's full history is the
  concatenation across grid epochs, not a separately maintained copy.
- Per-level cycle position, inventory or cash ledger, contribution lists.
- Collected premium / P&L aggregates (derived from limiter and optlimiter
  fills; see open question 3).
- Collateral ownership itself stays with the greeler (design principle 3);
  only the *report* of what happened to it lives on the position.
- A greeler-level event log — the position's own `Outcome`/`Assignment`
  already is the single source; a copy on the greeler would just be a
  second place the same fact could disagree with itself.
- A leg-level pointer alongside `Assignment` — the live leg is always
  `Legs`' last entry when `Outcome` gets set (scenario 5a), so naming it
  again would be redundant.
- Separate open/close pairing per roll — `Intent == "roll"` on one leg *is*
  the pairing; no matching logic needed.

---

## Decisions made at this checkpoint

1. **Reference the wrapped limiter, don't embed it — decided.**

   | | Embed (`*LimiterStateV2` field, first draft) | Reference (`LimiterID string`, decided) |
   |---|---|---|
   | Record shape | One `/optlimiters/<uid>` record holds everything | Two records, same keyspace: `OptLimiterStateV1` + the limiter's own `LimiterState` |
   | Save/Load logic | `optlimiter` must duplicate `limiter.Limiter`'s ~90 lines of encode/decode: order-map compaction, gob round-trip, idgen seed/offset handling — and keep that copy in sync by hand as `limiter.go` evolves | `optlimiter.OptLimiter` holds a real `*limiter.Limiter` and calls its unmodified `Save`/`Load` — zero duplicated logic |
   | Matches codebase idiom? | No — nothing else here embeds a child's state struct | Yes — this is exactly how `Looper.Save` persists its buy/sell limiters (`looper/looper.go:291-308`): each child does `child.Save(ctx, rw)` into its own key, the parent stores only the child's UID, and both writes land in the same transaction (the same `rw`) so it's atomic without merging records |
   | Satisfies the `/optlimiters/` keyspace-isolation goal (project.md leak mitigation)? | Yes, trivially — there's only one record and it's already under `/optlimiters/` | Not for free — `limiter.Save`/`Load` hardcode `/limiters/` today, so isolation requires the small keyspace-override change described above |
   | Cost of the fix | None needed | One additive option on `limiter.New`/`Save`/`Load` plus one line in `cleanUID`; every existing caller (`Looper`, `Waller`, standalone limiters) is unaffected since the default keyspace is unchanged |

   **Why reference wins:** the only thing embedding buys you is skipping that
   one small `limiter` package change — everything else favors reference.
   Duplicating Save/Load is real, ongoing risk (the two copies drift the
   next time someone touches order-map compaction or the idgen scheme),
   and it breaks the one-component-one-owner-of-its-own-persistence pattern
   every other job/component in this codebase follows. The keyspace override
   is small, backward-compatible, and mirrors work `cleanUID` already does
   for `/wallers/`, `/limiters/`, `/loopers/`.

2. **The assignment fact lives on the position, not a separate greeler-level
   log — revised from the first draft, which had a `GreelEvent` type on
   `GreelerStateV1`.**

   | | `GreelEvent` on the greeler (first draft) | `Assignment` on the position (decided) |
   |---|---|---|
   | Who reports the fact? | The greeler, correlated to a position via `PositionID` | The position itself — matches project.md's own phrasing, "it only reports terminal facts ('assigned: +100 shares at strike K')," taken literally |
   | Records touched to apply | Two: append to the greeler's `Events`, *and* set `OptPositionState.Outcome` so the position knows it's terminal | One: set `Outcome` + `Assignment` on the position |
   | Redundancy | `Outcome` and the matching `Events` entry both claim to record the same terminal fact — a partial write between the two leaves them disagreeing | None — one fact, one record |
   | Idempotency mechanism | A `Key` field compared against past log entries | `Outcome` empty → non-empty is itself the guard; no comparison needed |
   | Consistency with decision #3 below ("No ledger module") | A `GreelEvent` log, even scoped to assignments only, is still a small ledger — a second place a position's terminal fact is recorded | Keeps one source of truth per fact, the same principle that eliminated the portfolio ledger |
   | Fold shape | Greeler fold reads its own `Events` slice directly | Greeler fold walks `Epochs`' wheel entries and reads each position's `Outcome`/`Assignment` — the same "parent holds UID list, child holds facts" shape already used for grid epochs' `LevelLimiterIDs` and `Legs`, now used consistently everywhere |

   **Why the position wins:** project.md already frames assignment as
   something the position reports, not something recorded about it from
   outside — the first draft split that single sentence into two records
   (a bare `Outcome` string on the position, a richer fact on the greeler)
   for no reason that survives scrutiny. Collapsing them back into one
   record removes a redundant write, a redundant idempotency mechanism, and
   a small ledger the design's own decision-log #3 argues against keeping.
   The greeler still *owns* the portfolio the fact describes (principle 3
   is unchanged) — it just no longer needs its own copy of the fact to
   prove it.

3. **`LifetimeSummary` — deferred, confirmed.** Omitted from `GreelerStateV1`
   for now. `gobs.Summary` is stock-centric and premium/assignment cost
   basis don't fit it; the accounting model extension is picked up in the
   `greeler`/`greelladder` stories (project.md open item 2) once the shape
   of what needs summarizing is concrete.
4. **Ladder configuration.** Whether the ladder persists band-generation
   parameters (for re-centering proposals) or only its children is deferred
   to the `greelladder` story; `GreelLadderStateV1` reserves only the
   obvious fields.
5. **`PositionIDs` replaced by `Epochs` — decided.** Raised as: the flat
   position list gives no cheap way to see how much time a greeler has
   spent in each mode, or to walk its transitions in order, without
   reconstructing that from every position's and every level's own
   timestamps.

   | | `PositionIDs []string` (superseded) | `Epochs []*GreelEpoch` (decided) |
   |---|---|---|
   | What it records | Wheel periods only (position UIDs, in order) | Every mode period, grid and wheel, with `StartAt` |
   | Time-in-mode / transition traversal | Not derivable without loading every position and correlating against level limiter timestamps | Single pass over one list |
   | Reproduces the old list? | — | Yes exactly: filter `Mode == "wheel"`, collect `PositionID` |
   | Authority for current mode | N/A — mode was already derived from spot + children (scenario 3), unchanged | Still N/A — `Epochs` is a journal of already-derived transitions, explicitly not consulted to decide current mode, so this doesn't reopen decision-log #2's dual-source concern |
   | Transactional cost of appending | One record (the greeler), on flip | Same — one record, one append, on the greeler's own next iteration after it notices (via the referenced position's `Outcome`) that a transition occurred; not atomic with the position's own terminal write |

   **Why it's a strict upgrade, not just an addition:** `Epochs` carries
   everything `PositionIDs` did (as a filter) plus transition timing, at no
   extra transactional cost and no new dual-source risk — appending stays
   scoped as a best-effort journal entry, never something control-flow
   trusts without cross-checking the position it names. A crash that skips
   an append leaves the journal briefly incomplete, never wrong.

6. **Per-level limiter history moves into grid epochs, not a top-level
   `GreelLevel.LimiterIDs` — decided.** Raised as: if a wheel epoch is
   self-contained (its `PositionID` points at everything that happened
   during it), shouldn't a grid epoch be too, instead of pointing at a
   separate flat structure?

   | | Flat `GridLevels[i].LimiterIDs`, offset markers into it (first counter-proposal) | `Epochs[j].LevelLimiterIDs[i]`, owned by the epoch (decided) |
   |---|---|---|
   | Where a limiter UID is recorded | Once, in a top-level list; a grid epoch's slice of it is computed via a boundary index | Once, directly inside the one grid epoch that created it — nothing to slice |
   | New field needed | `LevelOffsets []int` per grid epoch, plus care that offsets only ever grow | None — appending is just `append` on the current epoch's own slice |
   | Self-containment, matching wheel epochs | Partial — still requires reaching into a second structure and computing a range | Full — a grid epoch's `LevelLimiterIDs` *is* the complete record of that stint, same shape as a wheel epoch's `PositionID` |
   | `GridLevels` after the change | Still holds `Pair` + a growing `LimiterIDs` | Shrinks to config only (`[]*Pair`, no wrapper struct — mirrors `WallerStateV2.TradePairs`) |
   | A level's full lifetime history, if ever needed | Already flat: `GridLevels[i].LimiterIDs` | Concatenate `LevelLimiterIDs[i]` across every grid epoch in order — one extra loop level, same walk over `Epochs` the assignment fold (scenario 5) already performs |

   **Why owned-by-the-epoch wins:** it's strictly simpler (no offset
   bookkeeping, no invariant to maintain about offsets being monotonic) and
   makes the two `Epochs` kinds symmetric — the whole reason `Epochs` was
   introduced was to make "what happened in each period" a single, natural
   read; leaving grid periods needing a second structure and a range
   computation would have only half-delivered that.

7. **Field named `GridLevels`, not `Levels` or `GridPairs` — decided.**
   Raised as: wheel mode also reads this field (`optpos` anchors CSP/CC
   strikes to it), so does a `Grid` prefix misdescribe it as grid-only?

   | | `Levels` (bare) | `GridPairs` | `GridLevels` (decided) |
   |---|---|---|
   | Matches domain vocabulary already used throughout both docs ("adjacent grid levels," per-level sizing, zone-threshold language) | Yes, but drops the origin | No — names the field after its storage type (`Pair`) instead of its role | Yes, and states the origin |
   | Accurate about wheel mode's read of it | Implies mode-neutral, which overstates it | Same problem as `Levels` | Accurate — these are grid levels wheel mode borrows to anchor strikes against, not a mode-neutral concept either side owns equally |

   **Why `GridLevels` wins:** wheel mode doesn't have its own notion of
   levels — it constrains strike selection using the grid's pre-existing
   geometry, the same way a warehouse's inventory list doesn't get renamed
   for being read by a second department. `GridLevels` names what the
   field fundamentally is (project.md's own first-mention phrase, "adjacent
   grid levels") without needing the doc comment to carry that nuance
   alone; the comment adds the one line about wheel mode's read of it, but
   the name itself is honest even without it.

8. **`PendingFlip`/`PendingFlipAt` move into `GreelEpoch`, out of
   `GreelerStateV1` — decided, reversing my first placement.** Raised as:
   isn't watching for its own termination and triggering the next epoch
   the current epoch's job, not the greeler's?

   | | Top-level greeler fields (first draft) | Nested in `GreelEpoch` (decided) |
   |---|---|---|
   | What the field means without context | Nothing — only meaningful relative to whichever epoch is currently last, but that relationship is positional, not structural | Explicit: it's this epoch's own bookkeeping for its own possible termination |
   | Fits "every `GreelEpoch` field is write-once"? | N/A — not on `GreelEpoch` at all, which was exactly the objection that kept it out at first | No — and that objection was too strict. The real invariant is "closed epochs are frozen, only the last is writable," which `LevelLimiterIDs` (monotonic) already didn't satisfy as literal write-once either |
   | Mode-scoped like `PositionID`/`LevelLimiterIDs`? | N/A | No — meaningful on either `Mode`, since the dwell clock gates flips in both directions; the one field on `GreelEpoch` that isn't mode-exclusive |
   | What happens to it once the epoch closes | N/A (lived elsewhere, got overwritten for the next attempt regardless of which epoch was "current") | Frozen — incidentally becomes a small audit trail: whatever it held is *why* that epoch ended |

   **Why nested wins:** my first-draft objection — that `GreelEpoch`'s
   fields are all write-once so a resettable field doesn't fit — proved to
   be the wrong invariant, not a real conflict. Once restated as "closed
   epochs are frozen, only the last is live," the pending clock fits
   exactly like `LevelLimiterIDs` does: mutable while current, frozen on
   close. Leaving it at the top of the greeler only worked by implicit
   position (it always happened to describe "whatever's last in `Epochs`")
   instead of saying so structurally — the same gap `GridLevels`'
   `LevelLimiterIDs` closed for grid-mode history, now closed for the
   dwell clock too.
