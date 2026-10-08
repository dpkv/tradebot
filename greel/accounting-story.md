# Story: accounting — premium, assignment cost basis, and greel P&L

Companion to [project.md](project.md) (open item 2) and the five prior
stories. All four build-order packages are merged; this story replaces the
"minimal until the accounting model lands" `Actions`/`BudgetAt`/
`GetSummary` in `greeler` and `greelladder` with a model that counts
everything a greel earns: grid round trips, option premium, and the stock
that assignments move in and out.

It keeps design principle 1: there is still no ledger. Every number below is
a fold over records that already exist — limiter fills, position legs
(optlimiter orders), and assignment facts — in `Epochs` order, the same
replay `Greeler.fold` already does for holdings.

---

## Scenario walkthrough

### 1. What today's summary gets wrong

`Greeler.GetSummary` (`greeler/greeler.go:326`) sums stock limiter fills
only. Per level it counts net bought − sold as unsold (valued at the level's
buy price) or oversold (at its sell price), and `gobs.Summary.Profit`
leaves both out. Two full wheel cycles show why that undercounts:

| | CSP cycle | CC cycle |
|---|---|---|
| Events | Put assigned: +100 sh at strike 10, premium 0.50/sh. Grid sells 100 at 11. | Grid buys 100 at 10. Call premium 0.60/sh. Call assigned: −100 sh at strike 11.50. |
| Real P&L | 1100 − 1000 + 50 − fees = **150 − fees** | 1150 − 1000 + 60 − fees = **210 − fees** |
| Today | sold 100 > bought 0 → all 100 oversold → **−(sell fee)** | bought 100 > sold 0 → all 100 unsold → **−(buy fee)** |

Three separate gaps produce that:

1. **Premium is invisible.** Nothing reads the legs' `OptLimiter.FilledValue`/
   `FilledFee` (`optlimiter/optlimiter.go:176,188`); `optpos.Position` has
   no premium accessor at all.
2. **Assignments are not fills.** A put assignment brings shares in at the
   strike and a call assignment takes them out, but neither appears as a
   buy or sell, so the grid leg on the other side has nothing to pair with.
3. **Unpaired fees still count.** Unsold/oversold sizes and values are set
   (`greeler/greeler.go:361-368`) but `UnsoldFees`/`OversoldFees` are not,
   so `Profit()` subtracts the fee of a fill whose value it excludes.

### 2. Lots: each level's cost basis, folded

Each level keeps a FIFO of lots `(size, price, fee, time)`, built by the
same epoch replay as `fold`:

- **In:** a grid buy fill (its fill price and fee), or a put assignment's
  share of that level (at the strike).
- **Out:** a grid sell fill pairs with the level's oldest lot; a call
  assignment's share of that level closes its oldest lots at the strike.

Assignments split over levels with `Greeler.attribute` (lowest level first,
greeler-story decision on open item 9), exactly as `fold` does, so lots and
holdings can never disagree. Because the fold already rejects a level
holding below zero, oversold is impossible for a greeler and stays zero.

The constraint bounds keep this tidy: a put strikes at or below every
level's buy price and a call at or above every level's sell price
(`greeler/run.go:323-337`), so assigned-in shares never cost more than the
level planned to pay, and assigned-out shares never leave for less than it
planned to sell.

### 3. Assignments are synthetic fills in `gobs.Summary`

With lots, an assignment is just a fill at the strike: a put assignment is
a buy, a call assignment a sell, per level, timed at the position's
`OutcomeAt`. They go into `Bought*`/`Sold*` like any limiter fill, and the
existing pairing does the rest. `Unsold*` becomes the open lots at their own
cost (not the level's buy price, which a put-assigned lot doesn't have) and
carries their fees, fixing gap 3.

Time ranges keep `Looper`'s rule (as `Greeler.GetSummary` already does): a
sell inside the range is paired with its lot counted in full even if the
lot was opened before the range.

### 4. Premium: its own line, held out of profit while the position is open

New fields on `gobs.Summary`, zero for every other job type:

```go
// Premium* hold option premium collected (contracts × per-share fill ×
// ContractSize), by fill time.
PremiumFees  decimal.Decimal
PremiumValue decimal.Decimal

// OpenPremium* hold the part of Premium* whose position hasn't settled.
OpenPremiumFees  decimal.Decimal
OpenPremiumValue decimal.Decimal
```

`Profit()` adds `PremiumValue − OpenPremiumValue` and subtracts the matching
fees: the same shape as `Unsold*`. A written option is a liability until it
expires or is assigned, so premium is held out of profit while its position
is still open when the summary is computed; once the position settles, the
premium counts in the range where it was filled.
`Add` sums the new fields, so the ladder's aggregate and any summary
persisted elsewhere keep working (gob and JSON both tolerate added fields).

Premium stays a separate line rather than reducing the assigned lots' cost
basis. Once the shares are sold the total profit is the same either way;
keeping it separate lets status show grid profit and premium side by side,
and keeps a put-assigned lot's cost equal to the strike the broker reports.

`optpos` gains `Facts` (outcome, assignment, and one `Premium` per filled
sell-to-open order: contracts × per-share fill × `ContractSize`, its fee and
fill time). `Position.Facts()` snapshots a running position; only its last
leg can have filled (gobs-story scenario 5a). `optpos.ReadFacts` reads the
same from saved records, every leg included, for a greeler that isn't
running. The greeler keeps one snapshot per wheel epoch: read in `Load`,
refreshed by `Run` after every step while the position is open. Accounting
reads only those snapshots, never a position another goroutine drives.

### 5. Fees

- Option leg fees: `PremiumFees`, from each order's `Fee`.
- Assignment fees: some brokers charge one. `gobs.AssignmentFact` gains
  `Fee decimal.Decimal`, written by the position from the settlement
  transaction; zero on old records. It goes into the synthetic fill's fee.
- Stock fees: unchanged, from the limiters.

### 6. `Actions`

The level's actions gain the assignment as a synthetic `gobs.Order` (side
BUY or SELL, `FilledSize` the level's share, `FilledPrice` the strike,
`ServerOrderID` the fact's `Key`, `CreateTime`/`FinishTime` the outcome
time), so `job actions` pairs a put-assigned buy with its grid sell.
Premium is not an action in v1: `gobs.Action` is a stock point, and the
summary already reports premium.

### 7. `BudgetAt`

Unchanged: the levels' buy value plus fees. A put's collateral is strike ×
shares, and the strike is at or below the lowest buy price (scenario 2), so
it never exceeds the level budget. Premium doesn't change what the greel
needs up front.

### 8. Reporting: `tradebot summary` and status

`tradebot summary` skips greeler and greelladder jobs today
(`subcmds/summary.go:125-143` falls to `default: return nil`), because
there is no `greeler.Summary` and `LifetimeSummary` was deferred
(gobs-story decision 3). Add `greeler.Summary(ctx, r, uid, period)` and
`greelladder.Summary(...)`, which load the records read-only and fold. That
needs a child load path without an options exchange: limiters already load
from KV alone; positions and optlimiters only need their records to answer
`Premium`, `Outcome` and `Assignment`. `LifetimeSummary` stays deferred:
computed on demand, as `-recalculate` does for wallers.

`tradebot status` and the Telegram profit report keep only
`trader.Statuser` jobs. `Greeler.Status` converts the same summary into a
`trader.Status`, and `GreelLadder.Status` sums its greelers' as
`Waller.Status` does. `trader.Summary` gains the same `Premium*` and
`OpenPremium*` fields, and its `Profit`, `Fees` and `FeePct` count them the
same way.

Per-greeler status (open item 8: mode, band, open strike, collected premium)
reads the same fold and is left to that item.

---

## Code

- `gobs/summary.go`: `Premium*`/`OpenPremium*`; `Add`, `Profit`, `Fees`,
  `FeePct` and `IsZero` include them.
- `gobs/optpos.go`, `exchange/api.go`: `Fee` on `AssignmentFact` and
  `OptionsSettlement`; `optpos` copies it at settlement.
- `optpos/facts.go`: `Facts`, `Premium`, `Position.Facts`, `ReadFacts`.
- `greeler/accounting.go`: the replay (`fills`), per-level lots
  (`addLevel`), premium (`addPremiums`), `GetSummary`, `Actions`, and
  `Summary(ctx, r, uid, period)`.
- `greelladder.Summary`, and the greeler and greelladder cases in
  `subcmds/summary.go`.

---

## Decisions made at this checkpoint

All eight proposals were approved as written on 2026-10-08.

1. **Assignments are synthetic fills at the strike**, per level by the
   allocation rule, timed at `OutcomeAt` (scenario 3).
2. **Per-level FIFO lots**; unsold valued at lot cost with its fees
   (scenarios 2 and 3).
3. **Premium is its own `Summary` line**, not a cost-basis reduction
   (scenario 4).
4. **Premium counts toward profit once the position settles**; until then
   it sits in `OpenPremium*`, like `Unsold*` (scenario 4).
5. **`AssignmentFact` gains `Fee`** (scenario 5).
6. **`Actions` carry assignments but not premium** in v1 (scenario 6).
7. **`BudgetAt` unchanged** (scenario 7).
8. **`tradebot summary` covers greelers and ladders by folding on demand**;
   `LifetimeSummary` stays deferred (scenario 8).
