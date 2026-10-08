# Story: `optpos` — one written-option position

Companion to [project.md](project.md), [gobs-story.md](gobs-story.md), and
[optlimiter-story.md](optlimiter-story.md). Third stage of the dependency
order (`gobs → optlimiter → optpos → greeler → greelladder`).

**v1 scope:** a position opens with a sell-to-open and holds until the
broker settles it — expired or assigned — or ends `unfilled` if its opening
order never fills and the greeler gives up. Closes, rolls, and the
`RollPolicy` that decides them are v2 (see the end of this story). Holding
to settlement fits greel: strikes sit at the greeler's levels, usually out
of the money, and an assignment isn't a failure — it's how shares get into
the grid.

---

## Grounding: how a component runs without job registration

project.md's job hierarchy table is explicit: `optpos.Position` and
`optlimiter.OptLimiter` are **components** — "passive, owner-driven, own
persisted records but no job registration." Neither goes through
`job.Runner.Add`/`Scan`. But `OptLimiter.Run` is a **blocking** loop — it
re-prices until the order fills or its context is cancelled — so it needs
its own goroutine, or it would stall the greeler's iteration loop for as
long as the order takes.

`job.Run(fn job.Func, fctx context.Context) *job.Job` (`job/job.go`) is
exactly this, already built, and independent of the registration
machinery: it spawns `fn` in a goroutine and returns a handle with
`Wait`/`Cancel`. `optpos` calls
`job.Run(func(ctx) error { return leg.Run(ctx, optEx, product, db) }, fctx)`
and holds the returned `*job.Job` — not registered anywhere, exactly
"owner-driven."

---

## Scenario walkthrough

### 1. Position born: contract selection, then sell-to-open

By the time `Position.Open` is called, the greeler has already appended the
wheel epoch naming this position's UID and saved the position record, its
`Config` (exchange, underlying) set and its `Progress` empty (write-ahead,
gobs-story.md scenario 3a and decision #17). Everything `Open` and `Check`
write below lands in `Progress`. `Open` then does, in order:

1. Fetch the chain: `optEx.GetOptionsChain(ctx, underlying)`.
2. `ContractSelector.Select(ctx, chain, constraint)` returns a `Selection`:
   the contract, plus the minimum premium (per share) worth selling it
   for, from `min-premium-yield`. `constraint` carries the greeler-derived
   bounds (strike ≤ greeler's levels / ≤ cash÷100 for a CSP, strike ≥
   levels' sell prices for a CC) plus the ladder's sibling exclusion when
   there is one (greelladder-story). DTE/liquidity knobs live on the
   selector itself (decision #1).
3. `optEx.OpenOptionsProduct(ctx, contract.ContractID)` opens the product.
4. `optlimiter.New(legUID, exchangeName, contract.ContractID, contract.ContractSize, numContracts, sel.MinPremium, knobs.RepriceStep, knobs.RepriceInterval)`
   builds the opening attempt, with a deterministic
   `legUID = path.Join(positionUID, "leg-%06d")`.
5. **Write-ahead:** append the leg's UID to `Legs`, set `Contract` (the
   cache field) from the selection, and save — one transaction, *before*
   anything is placed. A crash after this point resumes the same attempt
   against the same contract (scenario 5) instead of re-selecting.
6. Only then does `job.Run` spawn the leg (per Grounding above).

### 2. Position lives: hold until settlement

The greeler calls `Check(ctx, fctx, constraint)` every iteration while the
position is open. It runs these steps in order and stops at the first that
applies:

1. `Outcome` already set → terminal; nothing to do.
2. **Settlement check** (scenario 6, at most hourly): if the broker reports
   the contract assigned or expired, record it (scenario 4) and stop.
3. **Past `Contract.Expiry`** → the position is *settling*: wait for step 2
   to see broker truth. The calendar alone can't say how it ended — an
   in-the-money contract is assigned at expiry and the broker reports it
   the next day.
4. **Opening order not yet filled, and a new session has started** → re-run
   the selector. The chosen contract ages as days pass (fewer days to
   expiry), so a stale choice could otherwise hold the greeler in wheel
   mode doing nothing.
   - Same contract → nothing to do; the attempt itself restarts from the
     mid (its day order died at the close; optlimiter-story scenario 2).
   - Different contract → stop the current attempt (its live order is
     cancelled and confirmed). If it filled in the meantime, the position
     is open — done. Otherwise ask the sibling exclusion again, since the
     claim taken while selecting can lapse while the cancel is confirmed
     (asking renews it). If a sibling took the contract, the old attempt
     stays stopped and the next `Check` selects again; else start a new
     attempt: a new leg, written ahead exactly as in scenario 1.
5. Otherwise → hold.

So in v1, `Legs` is a list of sell-to-open attempts: any number that ended
with zero fills (stopped by re-selection or `Abandon`), and at most one that
filled — always the last. The fold skips zero-fill attempts.

### 3. A position that never opens: `Abandon`

With a premium floor, an opening order can rest unfilled indefinitely.
Nothing is open, so nothing will ever settle — something has to end the
position, and since `Position` doesn't look at zones (decision #3), the
greeler makes the call: when its normal flip-back rule (hysteresis + dwell
toward grid) fires before the opening order has filled, it calls
`Abandon`. This isn't force-closing a position — nothing was ever open.

`Abandon` stops the live attempt (cancel, confirm), then checks fills. If
the order filled in that window, abandoning fails: the position is open
and holds to settlement like any other. Otherwise `Outcome = "unfilled"`,
and the greeler flips back to grid with exactly the holdings it had.

### 4. Position dies: one writer, three v1 outcomes

Every terminal outcome is written by the position itself, inside its
greeler's tree — nothing outside that tree (not the ladder) ever writes a
position record:

- **`"assigned"`**: the settlement check reports an assignment. `Outcome`,
  `OutcomeAt`, and the `Assignment` fact (broker transaction key; shares =
  contracts × `ContractSize`, positive for a put, negative for a call;
  price = strike) are set in one transaction.
- **`"expired"`**: the settlement check reports an explicit expiration
  record. Never inferred from the calendar, and never from the contract
  merely disappearing from the account — absence alone could be a
  reporting lag.
- **`"unfilled"`**: `Abandon` (scenario 3).

Once `Outcome` is set, `Legs` is frozen and `Check` is a no-op.

### 5. Crash and resume: load, find the live attempt, reattach

Restart loads `OptPositionState`. If `Outcome` is still empty:

- No legs yet (the greeler crashed between saving the empty record and
  `Open`) → run `Open` again.
- Otherwise `Legs[len(Legs)-1]` is the current attempt. `optlimiter.Load`
  restores it, `OpenOptionsProduct` reopens its contract, and `job.Run`
  starts it again; the `OptLimiter` recovers its own broker orders
  (optlimiter-story scenario 4). `Contract` is refreshed from that leg,
  since it's only a cache.

Before the greeler's `Run` returns it calls `Stop`, which waits until the
attempt's live order is cancelled and confirmed. A leg that isn't running —
not started since a restart, or reaped after it failed — may still have an
order live, so it is run just long enough to recover it by client ID and
cancel it, unless the leg is done or its last run already confirmed its
orders done. A run that ends with an error instead of the cancel cause
(say, a placement that failed after the broker accepted it) is run once
more for the same reason. `Abandon` and re-selection stop the attempt the
same way.

Nothing here is new state — the same "re-derive, re-issue idempotent calls,
converge" pattern the whole design relies on.

### 6. Settlement check: broker truth about the contract

`OptionsExchange.GetOptionsSettlement(ctx, contractID)` (in
`exchange/api.go`) returns an `OptionsSettlement`: status
(`open | assigned | expired`), the broker transaction key, the number of
contracts, and when. `"assigned"` and `"expired"` come only from explicit
broker records.

`Check` calls it at most **hourly** — often enough to catch early
assignment (ex-dividend risk) the same day, and expiry-time assignment the
morning after it's reported. The last-check time is in memory only; after
a restart the first `Check` checks immediately.

---

## v2: closes and rolls

Deferred with `RollPolicy` (project.md decision log). Design notes kept for
then:
- `RollPolicy` decides hold / close (profit-take or loss-close) / roll on
  each `Check`. `Decide` needs a position snapshot — premium collected net
  of rolls, current contract, quote, Greeks — and returns the action with
  its limit price. Loss-close likely uncapped.
- `Check` re-asks `Decide` while a leg is in flight: same action → leave
  it; different action → stop it (leaving a zero-fill leg, as v1 already
  allows).
- New outcome `"closed"` (the position's own buy-to-close filled). A roll
  is one atomic leg (optlimiter-story v2 notes); `Contract` is refreshed
  when it fills.
- Knobs: `roll-dte`, `profit-take-pct`, `loss-close-multiple`.

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
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

// Constraint bounds contract selection to what the owning greeler allows —
// derived from its levels, not a knob (project.md contract-selection
// section). The greeler passes it to Open and Check.
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

// Selection is the contract to open and the minimum per-share premium
// worth selling it for.
type Selection struct {
    Contract   *gobs.OptionContract
    MinPremium decimal.Decimal
}

// ContractSelector picks the next contract to open. Its knobs (target-
// delta, dte-range, min-premium-yield, min-open-interest, max-spread-pct)
// are injected at construction; Constraint is what varies call to call.
type ContractSelector interface {
    Select(ctx context.Context, chain []*gobs.OptionContract, c *Constraint) (*Selection, error)
}

// Position is one written-option position: opened by a sell-to-open,
// held until settled (v1). Component, not a job — driven by its owning
// greeler.
type Position struct {
    uid          string
    exchangeName string
    underlying   string

    selector ContractSelector
    knobs    *gobs.WheelKnobs // re-price step/interval for each attempt

    contract *gobs.OptionContract // cache: current attempt's contract

    legIDs []string // sell-to-open attempts, in order — gobs-story's Legs

    outcome   string
    outcomeAt time.Time

    active *job.Job // the attempt in flight, nil if none

    lastSettlementCheck time.Time // in memory only (scenario 6)

    // Held at construction (New/Load), not re-passed to every method.
    optEx exchange.OptionsExchange
    db    kv.Database
}

func New(uid, exchangeName, underlying string, selector ContractSelector, knobs *gobs.WheelKnobs, optEx exchange.OptionsExchange, db kv.Database) *Position {
    panic("unimplemented")
}

// Open selects a contract and starts the sell-to-open attempt. The greeler
// has already saved this position's empty record under its wheel epoch;
// Open saves the attempt's UID before it can place an order.
func (v *Position) Open(ctx context.Context, fctx context.Context, c *Constraint) error {
    panic("unimplemented") // scenario 1
}

// Check runs settlement, settling, and re-selection, in that order.
// Called by the owning greeler every iteration while the position is open;
// a no-op once Outcome is set (scenario 2).
func (v *Position) Check(ctx context.Context, fctx context.Context, c *Constraint) error {
    panic("unimplemented")
}

// Abandon ends a position whose opening order never filled, with Outcome
// "unfilled". Fails if the order filled — the position is then open
// (scenario 3).
func (v *Position) Abandon(ctx context.Context) error {
    panic("unimplemented")
}

// Outcome is empty while open or settling; terminal otherwise.
func (v *Position) Outcome() string { return v.outcome }

// Save writes Config (exchangeName, underlying) and Progress (contract,
// legIDs, outcome, assignment) as gobs.OptPositionStateV1.
func (v *Position) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented")
}

func Load(ctx context.Context, uid string, r kv.Reader, selector ContractSelector, knobs *gobs.WheelKnobs, optEx exchange.OptionsExchange, db kv.Database) (*Position, error) {
    panic("unimplemented") // scenario 5
}
```

---

## Decisions made at this checkpoint

1. **One knob struct per preset — decided.** Presets (Layer 0) are "named
   bundles of Layer-1 knobs" per project.md — one bundle. A preset fills a
   single `gobs.WheelKnobs`; the `ContractSelector` closes over its
   selection knobs, and each opening attempt takes its re-price step and
   interval.
2. **`Position` holds its exchange/db dependencies at construction —
   decided.** `New`/`Load` take them once; `Open`/`Check` take only what
   varies per call (`Constraint`).
3. **`Check` needs no spot/zone information — decided.** The greeler calls
   `Check` unconditionally while the position is open and never
   force-closes a position on a zone change ("existing orders/positions
   drift through untouched"). The one zone-driven call, `Abandon`, applies
   only before anything opened (scenario 3).
4. **Terminal outcomes come only from broker truth (or `Abandon`); past
   expiry the position is settling — decided after design review.**
   Replaces a calendar-based `"expired"`, which would have recorded every
   expiry-time assignment as an expiry and then ignored the broker's
   assignment report as a duplicate.
5. **Assignment detection lives in the position, driven by its greeler —
   decided after design review.** Replaces the ladder's positions poll; the
   greeler's tree is the only writer of position records, and a standalone
   greeler detects its own assignments. Hourly cadence (scenario 6).
6. **Legs are written ahead with deterministic UIDs — decided after design
   review.** Saved before an attempt can place an order, so a crash can't
   orphan an order or re-select a different contract on restart.
7. **Sibling exclusion via `Constraint.Exclude` — decided after design
   review**, provided by the ladder; nil when standalone. TODO: revisit the
   restriction (greelladder-story).
8. **`OptionsExchange.GetOptionsSettlement` added — approved and
   implemented** (scenario 6); the etrade implementation is follow-on work.
9. **v1 holds to settlement; `RollPolicy`, closes, and rolls are v2 —
   decided.** `Check` only settles, waits, or re-selects.
10. **Zero-fill attempts stay in `Legs`; the contract is re-selected at
    each session start — decided.** The fold skips zero-fill attempts
    (scenario 2).
11. **`Abandon` and the `"unfilled"` outcome — decided.** Called by the
    greeler when its normal flip-back rule fires before the opening order
    fills (scenario 3).

No open questions remain in this module.
