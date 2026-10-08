# Story: `optlimiter` — the option order state machine

Companion to [project.md](project.md) and [gobs-story.md](gobs-story.md).
Second stage of the dependency order
(`gobs → optlimiter → optpos → greeler → greelladder`).

An `OptLimiter` is **one option order intent** — in v1, "sell to open N
contracts of this contract, no cheaper than this floor." Over its life it
may place several broker orders at different prices, re-pricing toward the
market until the order fills. v1 only sells to open; closes and rolls are
v2 (see the end of this story).

This story replaces an earlier draft that wrapped `limiter.Limiter` behind
an `exchange.Product` adapter. Why that changed is the first section.

---

## Why not `limiter.Limiter` (decided after design review)

`Limiter` is a grid-trading order: "keep one order at price P, but only
while the market is near P." That policy is its main loop, and its price is
fixed for its life (set in `New`, persisted, not changeable — `SetOption`
rejects every option). An option order needs the opposite on every axis:

| | `Limiter` (grid order) | Option order |
|---|---|---|
| When an order exists | Only while the market is near P; a BUY only while price ≥ P (`limiter/run.go:182`) | Always, from the start |
| What triggers placing it | A price update arriving | Immediately, then a re-price timer |
| Price | Fixed for the limiter's life | Moves on every re-price — often, since option spreads are wide |
| After a crash | Can forget an order placed since its last save | Must never forget one: a forgotten sell-to-open can become a second, uncovered contract |

Changing `Limiter` to support both would mean adding all of this inside the
loop every production grid order runs through, in a package with no tests.
A separate state machine duplicates roughly 150–200 lines of order-tracking
logic instead, copied from `Limiter`'s proven patterns and reusing the
pieces already factored out (`idgen`, `exchange.SimpleOrder`), with zero
risk to stock trading. It also removes, from the first draft: the
`OptionsProduct`→`Product` adapter, the far-cancel workaround, the `limiter`
keyspace override, the background finish-time fixer's exposure to option
orders, and the product-ID check mismatch.

---

## Scenario walkthrough

### 1. One order intent

`optpos` creates an `OptLimiter` with: the contract, the number of
contracts (one, in practice — a greeler is sized to one contract), a floor
(`MinPremium`, from the contract selector), and the re-price parameters
(from the greeler's `WheelKnobs`, copied onto the record so a resumed order
behaves the same). The order is **done** when its filled quantity reaches
the target. Its owner stops it by cancelling `Run`'s context; it then
cancels its live broker order and waits for confirmation before returning.

### 2. Placing and re-pricing

During the regular session, `Run`:

1. Fetches a quote: `GetOptionsProduct(ctx, contractID)` returns a snapshot
   with `Bid`/`Ask`. (The price-update feed carries a single price, no
   bid/ask, so it isn't enough.)
2. Computes the price for step *k*, where *k* is the number of orders
   already placed this session:
   `price = max(mid − k × step × spread, bid, MinPremium)`, rounded to
   the exchange's tick, and at least one tick below the previous price.
   - Starts at the mid (*k* = 0).
   - `step` is a fraction of the current spread — 20% by default, so it
     reaches the bid in about three steps whatever the contract's price.
   - Re-anchored on a fresh quote each step, since the market moves.
   - Never below the bid (an order at the bid already fills, so going
     lower gains nothing) or the caller's floor.
3. Places the order with `LimitSellToOpen`.
4. Every re-price interval (2 minutes by default), if the order hasn't
   filled: cancel, confirm, and re-place at the next price (scenario 3).
   Once the price reaches the bid or the floor, the order rests there.

Option orders are **regular-session day orders**: they die at the close.
The next session starts again from the mid (*k* resets). Outside regular
hours `Run` sleeps, reusing etrade's market-hours handling (project.md
open item 4). A good-till-cancelled order would instead sit overnight at a
stale price.

### 3. Never two live orders

The most important invariant. If a re-price sends the new order before the
old one is confirmed cancelled, and the old one fills meanwhile, both can
fill — and a sell-to-open filling twice is 2 contracts sold against 100
shares of collateral: a naked short. So every re-price is:

1. Cancel the live order.
2. Wait until the broker confirms it's done (the `Limiter.cancel` pattern:
   cancel, then poll `Get` until done).
3. Recompute remaining = target − total filled. A fill that raced the
   cancel just reduces it, possibly to zero (done).
4. Only then place the next order.

etrade's native change-order would be faster but needs new interface
surface; deferred.

### 4. Crash recovery: client IDs written ahead

etrade can't dedupe client IDs (`CanDedupOnClientUUID()` returns false), so
re-sending an order after a crash creates a second one. `Limiter` saves its
client-ID counter only every 10 orders and skips ahead on restart — it can
forget an order placed since the last save. That's tolerable for a grid
order (a stray resting limit), not for a sell-to-open.

Client IDs come from `idgen`, seeded with the `OptLimiter`'s UID; the ID at
offset *k* is deterministic. **Before placing each order, the offset is
advanced and saved** — one KV write, then the broker call. On resume:

1. Every client ID below the saved offset that isn't in the order list is
   looked up with `OptionsExchange.GetOptionsOrderByClientID` (added for
   this — it covers filled and cancelled orders, not just open ones; a
   filled order missing from an open-orders listing would otherwise look
   "never placed" and get placed again). Found → adopt it. `os.ErrNotExist`
   → not placed yet as far as the broker shows. A broker can be slow to
   list an order it accepted, so a missing ID is looked up again on each
   start until a lookup at least 10 minutes (`absentSettle`) after its
   failed placement or first miss still misses it; only then does it count
   as never placed. The record keeps each missing ID with that time, and a
   lookup offset below which every ID is adopted or settled, so a restart
   neither forgets an unconfirmed ID nor looks settled ones up again, and a
   run of rejected placements doesn't grow every start's lookups.
2. Every order not yet done is refreshed via `Get` (the
   `Limiter.fetchOrderMap` pattern). A failed lookup stops step 1 but not
   this step, and `Run` still cancels every live order it knows before
   returning the error.
3. Then the loop continues — scenario 3's rule guarantees at most one of
   those orders is live.

A placement that fails may still have reached the broker, so it is never
reported as a clean stop: `Run` returns its owner's cancel cause only once
every order is confirmed done, and a failed placement returns the error
even if the owner cancelled meanwhile. The owner then runs it again, and
recovery finds the order by its client ID and cancels it. While any ID is
missing but not yet settled, `Run` places nothing (a second order could
sell twice the contracts) and returns `ErrUnconfirmed`; a stopped `Run`
returns it instead of the cause, so the owner doesn't take an order the
broker hasn't listed yet for one that was never placed. The owner runs it
again later, and a later lookup either finds the order or settles the ID.

### 5. Units: contracts, and where `ContractSize` applies

Order quantity is in **contracts**, matching `LimitSellToOpen(ctx,
clientID, numContracts, limitPrice)`. Premium is quoted **per share**
(market convention: a $2.50 premium on a 100-share contract costs $250), so
prices stay in the same units as `gobs.OptionContract.Bid`/`Ask`. Anyone
accounting in total dollars multiplies by `ContractSize` (typically 100)
themselves; `optlimiter` carries `ContractSize` on its record so callers
don't have to re-fetch the contract.

### 6. Persistence

Each `OptLimiter` saves its own record under `/optlimiters/`
(`gobs.OptLimiterStateV1`): contract, size, floor, re-price parameters,
`idgen` seed and offset, and every broker order it placed. It no longer
involves the `limiter` package at all, so nothing under `/limiters/` and no
`limiter` background task ever sees an option order. Its owner's tree also
saves it; both writers are the same in-memory instance, guarded by the
`OptLimiter`'s own lock.

### 7. What this requires of the exchange

From the etrade `OptionsExchange` implementation (follow-on work):
- option orders placed as regular-session day orders;
- `GetOptionsProduct` snapshots with live `Bid`/`Ask`;
- `GetOptionsOrderByClientID`, including filled and cancelled orders
  (etrade's client order ID field accepts digits only, so the UUID↔digits
  mapping is etrade's concern).

---

## v2: closes and rolls

Deferred with `RollPolicy` (project.md decision log). Design notes kept for
then:
- **Buy-to-close** (profit-take, loss-close): the same state machine with
  the side flipped — walk from the mid toward the ask, capped by the
  caller's limit. Loss-close likely uncapped: it fires because the price is
  running up, and a fixed cap can be overtaken.
- **Rolls** are one atomic broker order across two contracts, priced on
  net quotes: for a credit roll, mid = new mid − prior mid, worst = new
  bid − prior ask. They need an exchange primitive for a two-contract
  order (sketched earlier as `OptionsExchange.OpenOptionsRollProduct`;
  removed from `exchange/api.go` for v1, decision #8).
- `RollPolicy.Decide` needs a position snapshot (premium collected net of
  rolls, current contract, quote, Greeks) and should return the limit with
  the action.
- The persisted record gains `Intent` (`open | close | roll`) and
  `PriorContractID`; a v1 record decodes with both empty, meaning "open".

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package optlimiter

import (
    "context"
    "sync"
    "time"

    "github.com/bvk/tradebot/exchange"
    "github.com/bvk/tradebot/idgen"
    "github.com/bvkgo/kv"
    "github.com/shopspring/decimal"
)

const DefaultKeyspace = "/optlimiters/"

// OptLimiter is one sell-to-open order intent: it places a broker order
// and re-prices it toward the market until filled. Component, not a job —
// its owner (optpos) runs it.
type OptLimiter struct {
    mu sync.Mutex

    uid          string
    exchangeName string
    contractID   string
    contractSize decimal.Decimal
    numContracts decimal.Decimal
    minPremium   decimal.Decimal // per-share floor (scenario 2)

    repriceStep     decimal.Decimal // fraction of the spread per step
    repriceInterval time.Duration

    idgen  *idgen.Generator                  // offset saved ahead of each order (scenario 4)
    orders map[string]*exchange.SimpleOrder // server order ID -> order
}

func New(uid, exchangeName, contractID string, contractSize, numContracts, minPremium, repriceStep decimal.Decimal, repriceInterval time.Duration) (*OptLimiter, error) {
    panic("unimplemented")
}

// Run places and re-prices until filled or ctx is cancelled; on cancel it
// cancels the live order and waits for confirmation. Saves itself to db
// ahead of each placement.
func (v *OptLimiter) Run(ctx context.Context, optEx exchange.OptionsExchange, product exchange.OptionsProduct, db kv.Database) error {
    panic("unimplemented")
}

func (v *OptLimiter) UID() string                  { return v.uid }
func (v *OptLimiter) FilledSize() decimal.Decimal { panic("unimplemented") }

func (v *OptLimiter) Save(ctx context.Context, rw kv.ReadWriter) error {
    panic("unimplemented")
}

func Load(ctx context.Context, uid string, r kv.Reader) (*OptLimiter, error) {
    panic("unimplemented")
}
```

---

## Decisions made at this checkpoint

1. **A separate option order state machine, not a wrapped
   `limiter.Limiter` — decided after design review.** See the first
   section. Supersedes project.md decision-log #1 and this story's first
   draft (adapter, far-cancel, keyspace override).
2. **Never two live orders** — cancel, confirm, recompute, then place
   (scenario 3).
3. **Client IDs written ahead; lookup by client ID covers completed
   orders** — `OptionsExchange.GetOptionsOrderByClientID` added to
   `exchange/api.go` (scenario 4).
4. **Regular-session day orders** — the order dies at the close and
   re-pricing restarts from the mid each session (scenario 2).
5. **Re-price rule** — start at the mid; step 20% of the current spread
   every 2 minutes, re-anchored on a fresh quote, at least one tick per
   step; never below the bid or the caller's floor; restart each session.
   Step and interval are `WheelKnobs`, copied onto each record (scenario 2).
6. **v1 sells to open only**; closes and rolls are v2.
7. **`ContractSize` is carried for callers**, never applied inside
   `optlimiter` (scenario 5).
8. **`OpenOptionsRollProduct`/`OptionsRollProduct` removed from
   `exchange/api.go` until v2 — decided.** v1 never rolls, and every
   `OptionsExchange` implementation would otherwise have to provide them;
   the roll design may change in v2 anyway.

No open questions remain in this module.
