# Story: `greelladder` — spawning, the positions poll, risk gates

Companion to [project.md](project.md) and the four prior stories. Final
stage of the dependency order
(`gobs/greel.go → optlimiter → optpos → greeler → greelladder`). Unlike the
others, this module isn't primarily new mechanism — it's mostly the
existing `Waller`-over-`Looper` spawner/aggregator pattern, plus exactly
one genuinely new responsibility: the positions poll, which is where
project.md open item 1 (assignment detection) and gobs-story.md's
assignment-fact design (scenario 5) finally meet a concrete mechanism.

---

## Scenario walkthrough

### 1. Spawning and aggregation: `Waller`, not reinvented

`waller.Waller` (`waller/waller.go`) has **no `Run` method at all** — it's
purely a spawner/aggregator: `New` constructs child `Looper`s and holds
them; `Save`/`Load` persist `LooperIDs` and reload children; `GetSummary`/
`Actions`/`BudgetAt`/`Fees`/`BoughtValue`/`SoldValue`/`UnsoldValue` all just
loop over children and sum. Each `Looper` is independently job-registered
(its own goroutine, its own `trader.Runtime`) — `Waller` never drives one
directly.

`GreelLadder` follows this exactly for the spawning/aggregation half:
`New` constructs child `Greeler`s (one per price band) and holds them;
`Save`/`Load` persist `GreelerIDs` (`GreelLadderStateV1`, gobs-story.md);
aggregation methods sum across children, deferred in the same way and for
the same reason `LifetimeSummary` was deferred at every lower level
(decision #3, gobs-story.md) — there's no new accounting shape to invent
here beyond what those deferrals already punted on.

**Where it diverges from `Waller`:** `GreelLadder` *does* need a real
`Run(ctx, rt)` — scenarios 2 and 3 are active responsibilities `Waller`
never had, because spot trading has nothing analogous to assignment
detection or cross-greeler risk exposure.

### 2. The positions poll: where assignment detection actually happens

gobs-story.md scenario 5 already fixed *how* an assignment is applied —
one KV transaction, touching only the assigned `OptPositionState` record,
idempotent because `Outcome` starting empty and ending non-empty is itself
the guard. What was left open (project.md item 1) is *how the poll
observes it in the first place*. Decided here:

**Primary signal: transaction records, not position deltas.** E*TRADE (and
brokers generally) expose an order/transaction history that reports an
assignment or exercise as its own distinct entry — this is more precise
and more immediate than inferring assignment from a stock-position delta,
which could be confused by unrelated activity (a manual trade, a greeler's
own ordinary limiter fills) happening in the same poll window. Early
assignment around ex-dividend (project.md's stated risk) is exactly the
case where waiting for a position-delta poll cycle to notice a mismatch is
too slow — a transaction record is the direct signal.

**Fallback signal: a position-delta reconciliation, run less frequently.**
Belt-and-suspenders against a missed or malformed transaction record: if
total stock held (as reported by the account) ever disagrees with what
every greeler's own derived inventory (greeler-story.md scenario 1's fold)
says it should be, that mismatch is itself evidence an assignment happened
that the transaction-record poll didn't catch. This doesn't need its own
correlation logic — it just triggers a forced re-check of the transaction
poll for the underlying involved, reusing the primary path rather than
duplicating it.

**Correlating a detected assignment to the right `OptPositionState`:** the
poll doesn't need a lookup index — it just walks every greeler's `Epochs`
(each held by `GreelLadder` via its spawned children) for the last entry
where `Mode == "wheel"` and the position isn't terminal, and matches the
transaction record's contract ID against that position's `Contract.
ContractID`. Exactly one greeler can be wheeling a given contract at a
time, so this is never ambiguous.

**No new checkpoint state.** The poll doesn't track "last processed
transaction ID" anywhere — it re-scans open positions and recent
transactions every cycle, and relies entirely on the same idempotency
guard gobs-story.md already designed (`Outcome` empty → non-empty). A
redundant detection just finds `Outcome` already set and no-ops. This
keeps `GreelLadderStateV1` free of any poll-progress field — consistent
with derivation-over-state, and one less thing that can drift from truth.

### 3. Risk gates: pulled via `SetOption`, not a hard dependency

project.md open item 6 (max open contracts, max assignment exposure) is
confirmed as `GreelLadder`'s responsibility (greeler-story.md decision #3)
— but `Greeler` is explicitly "fully usable standalone" (project.md), so a
`Greeler` can't *require* querying its ladder before every mode flip
without breaking that. Resolved by reusing a lever that already exists
instead of adding a new one: `GreelLadder.Run`'s poll iteration also
evaluates aggregate exposure across its `[]*greeler.Greeler` (open
positions × their per-contract share/cash exposure, summed) and, when a
configured limit is being approached, calls `SetOption("freeze", "wheel")`
on the newest/offending greeler(s) — the exact same `trader.Trader.
SetOption` mechanism greeler-story.md scenario 4 already defined for
manual operator control.

A standalone `Greeler` (no ladder) simply never receives this call and
stays unrestricted — matching "fully usable standalone" exactly, since
nothing about `Greeler` itself changes; the ladder is just another caller
of a control surface that already existed for a person to use manually.

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
    "context"
    "path"
    "time"

    "github.com/bvk/tradebot/exchange"
    "github.com/bvk/tradebot/gobs"
    "github.com/bvk/tradebot/greeler"
    "github.com/bvk/tradebot/trader"
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

const DefaultKeyspace = "/greelladders/"

// GreelLadder spawns/aggregates greelers across price bands (Waller-to-
// Looper pattern, scenario 1) and runs the positions poll (scenario 2)
// plus risk gates (scenario 3) — the one genuinely new active
// responsibility Waller never had.
type GreelLadder struct {
    uid          string
    exchangeName string
    productID    string

    greelers []*greeler.Greeler

    maxOpenContracts   int
    maxAssignmentValue decimal.Decimal
}

func New(uid, exchangeName, productID string /* band configs */) (*GreelLadder, error) {
    panic("unimplemented") // mirrors waller.New's per-band spawn loop
}

// Run drives only the positions poll and risk gates (scenario 2, 3) —
// spawning/aggregation needs no Run, matching Waller.
// pollInterval is the transaction-record poll's cadence — frequent enough
// to catch early assignment same-day, well clear of any plausible broker
// rate limit (decision #1).
const pollInterval = time.Hour

func (v *GreelLadder) Run(ctx context.Context, rt *trader.Runtime) error {
    panic("unimplemented")
    // Loop, every pollInterval:
    //   for each greeler: check last wheel epoch (if any) against recent
    //     transaction records for its ContractID; on match, write the
    //     Assignment fact directly onto that OptPositionState record.
    //   periodically: reconcile account stock holding vs. the sum of each
    //     greeler's DerivedStock() (fallback signal, decision #3); on
    //     mismatch, force a transaction re-check.
    //   evaluate aggregate exposure against maxOpenContracts/
    //     maxAssignmentValue (constructor config, decision #2);
    //     SetOption("freeze", "wheel") on offending greelers if over.
}

func (v *GreelLadder) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented") // mirrors waller.Save: save each greeler, persist GreelerIDs
}

func Load(ctx context.Context, uid string, r kv.Reader, optEx exchange.OptionsExchange) (*GreelLadder, error) {
    panic("unimplemented") // mirrors waller.Load
}

var _ trader.Trader = (*GreelLadder)(nil)
```

---

## Decisions made at this checkpoint

1. **Poll interval: hourly — decided.** A reasonable default for a
   transaction-record poll: frequent enough that even an early-assignment
   scenario (project.md's ex-dividend risk) is caught same-day, infrequent
   enough to stay well clear of any plausible broker rate limit across all
   greelers sharing one `GreelLadder`. Exact tuning (and whether it should
   back off under rate-limit pressure) is still implementation detail, but
   the interval itself doesn't need further design review.
2. **`maxOpenContracts`/`maxAssignmentValue` are constructor config, not
   `Options`-tunable — decided.** Unlike `freeze`/`retire` (which are
   meant to be flipped live, by an operator reacting in the moment), risk
   limits are a standing policy decision made when the ladder is set up,
   not something to adjust on the fly mid-session. Kept as plain fields on
   `GreelLadder`, matching the skeleton as originally sketched.
3. **Fallback reconciliation calls an exported `Greeler` method — decided.**
   `greeler.Greeler` now exports `DerivedStock() decimal.Decimal`
   (greeler-story.md decision #4, added specifically for this) — the
   scenario 1 fold's sum, already computed, just exposed rather than
   `GreelLadder` reaching past `Greeler`'s own derivation to recompute it
   independently.
