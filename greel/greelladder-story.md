# Story: `greelladder` — spawning, sibling coordination, reconciliation

Companion to [project.md](project.md) and the four prior stories. Final
stage of the dependency order
(`gobs/greel.go → optlimiter → optpos → greeler → greelladder`). This
module is mostly the existing `Waller`-over-`Looper` pattern. After the
design review it carries less than the first draft did: assignment
detection moved into each greeler's own position (optpos-story scenario 6),
so the ladder never writes positions. What's left is coordination only a
parent can do — keeping siblings off the same contract, and a cross-check
of stock movement — plus risk gates, which are a TODO.

---

## Scenario walkthrough

### 1. Spawning, driving, and aggregation: `Waller`, not reinvented

`waller.Waller` constructs its child `Looper`s in `New`, persists
`LooperIDs` in `Save`, reloads them in `Load`, and **drives them itself**
in `Run` (`waller/run.go:35`). Children are never independent jobs:
`server.LoadAll` only loads top-level job keys, so a looper under a waller
runs only because its waller runs it. (The first draft of this story said
`Waller` had no `Run` and that loopers were independently registered —
both wrong.)

`GreelLadder` follows this exactly: `New` constructs one child `Greeler`
per price band; `Save`/`Load` persist `GreelerIDs` (`GreelLadderStateV1`,
gobs-story.md) and reload the children; `Run` drives them; aggregation
(`Actions`/`BudgetAt`/`GetSummary`) sums across children, minimal until
the accounting model lands (project.md open item 2).

Because the ladder holds the very greeler instances it runs, it can hand
them hooks (scenario 2) and read their derived state (scenario 3) directly —
no KV round-trips, no stale copies.

### 2. Sibling contract exclusion

Sibling greelers on the same underlying can select the same strike and
expiry. The broker nets them into one position, so assigning 1 of 2
contracts would be ambiguous — there's no way to tell which greeler was
assigned. The ladder prevents it: it gives each greeler an `Exclude` hook
(`optpos.Constraint.Exclude`) that answers from its siblings' open
positions plus any selection in flight, tracked under the ladder's own
lock, so two siblings can't claim the same contract concurrently. A
standalone greeler has no hook and no siblings.

**TODO — revisit the restriction.** It can force a greeler onto a worse
strike or expiry than it would otherwise pick. Alternatives include a
deterministic rule for splitting a partial assignment across siblings. It
also doesn't cover collisions the ladder can't see: two ladders, standalone
greelers, or manual trades on the same account and contract.

### 3. Stock reconciliation: delta-based, alert-only

A cross-check, not a detector — each greeler's position detects its own
assignment. Every hour the ladder compares the **change** in the account's
stock holding for the symbol against the **change** in its greelers'
summed `DerivedStock()` over the same interval. A mismatch sends a
messenger alert; nothing is written.

Deltas, not totals: the account may hold the same stock for other reasons
(other jobs, manual holdings), so totals would disagree permanently. Deltas
flag only unexplained movement — a concurrent manual trade can still cause
a false alarm, which is acceptable for an alert. The account's share count
for the symbol comes from the exchange; whether the existing balance
updates suffice is an implementation question.

### 4. Risk gates — TODO, revisit

Max open contracts and max assignment exposure (project.md open item 6)
belong at the ladder. The first draft enforced them by calling
`SetOption("freeze", "wheel")` on running greelers. That's invalid: the
`trader.Trader` contract only allows option changes while a job isn't
running, and the ladder couldn't tell its own freezes apart from an
operator's. A likely replacement is another hook the ladder hands the
greelers it runs, like `Exclude`, but that's to be decided.

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package greelladder

import (
    "context"
    "sync"
    "time"

    "github.com/bvk/tradebot/gobs"
    "github.com/bvk/tradebot/greeler"
    "github.com/bvk/tradebot/timerange"
    "github.com/bvk/tradebot/trader"
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

const DefaultKeyspace = "/greelladders/"

// reconcileInterval is the stock-reconciliation cadence (scenario 3).
const reconcileInterval = time.Hour

// GreelLadder spawns, drives, and aggregates greelers across price bands
// (Waller-to-Looper pattern, scenario 1), keeps siblings off the same
// contract (scenario 2), and cross-checks stock movement (scenario 3).
type GreelLadder struct {
    uid          string
    exchangeName string
    productID    string

    greelers []*greeler.Greeler

    mu      sync.Mutex
    claimed map[string]string // contractID -> greeler UID (scenario 2)
}

func New(uid, exchangeName, productID string /* band configs */) (*GreelLadder, error) {
    panic("unimplemented") // mirrors waller.New's per-band spawn loop
}

// Run drives the ladder's greelers (as Waller.Run drives its loopers) and
// runs the hourly reconciliation (scenario 3).
func (v *GreelLadder) Run(ctx context.Context, rt *trader.Runtime) error {
    panic("unimplemented")
}

func (v *GreelLadder) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented") // mirrors waller.Save: save each greeler, persist GreelerIDs
}

// Load rebuilds the ladder and its greelers from their records alone, then
// sets each greeler's Exclude hook (scenario 2).
func Load(ctx context.Context, uid string, r kv.Reader) (*GreelLadder, error) {
    panic("unimplemented") // mirrors waller.Load
}

// Minimal sums across children until the accounting model lands.
func (v *GreelLadder) Actions() []*gobs.Action                        { panic("unimplemented") }
func (v *GreelLadder) BudgetAt(feePct decimal.Decimal) decimal.Decimal { panic("unimplemented") }
func (v *GreelLadder) GetSummary(r *timerange.Range) *gobs.Summary     { panic("unimplemented") }

var _ trader.Trader = (*GreelLadder)(nil)
```

---

## Decisions made at this checkpoint

1. **Assignment detection moved out of the ladder — decided after design
   review.** Each greeler's position checks its own contract's settlement
   (optpos-story scenario 6, decision #5). This supersedes the ladder's
   positions poll. It removes the second writer on position records, and
   a standalone greeler now detects its own assignments. The hourly
   interval moved with it.
2. **The ladder drives its greelers, as `Waller.Run` drives its loopers —
   corrected after design review.** The first draft wrongly said `Waller`
   has no `Run`.
3. **Sibling contract exclusion via the `Exclude` hook — decided after
   design review.** TODO: revisit the restriction (scenario 2).
4. **Reconciliation is delta-based and alert-only — decided after design
   review.** It uses `greeler.DerivedStock()` (greeler-story decision #4),
   runs hourly, and never writes (scenario 3).
5. **Risk gates — TODO, revisit** (scenario 4). The earlier decision to
   keep `maxOpenContracts`/`maxAssignmentValue` as constructor config is
   parked with it.
