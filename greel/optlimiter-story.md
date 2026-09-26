# Story: `optlimiter` — one option order, adapting `exchange.OptionsProduct` into `limiter.Limiter`

Companion to [project.md](project.md) and [gobs-story.md](gobs-story.md). This
module builds the second stage of the dependency order
(`gobs/greel.go → optlimiter → optpos → greeler → greelladder`): a thin
`Product` adapter plus a wrapper that lets an *option* order reuse
`limiter.Limiter`'s hardened single-order machinery (idempotent client IDs,
crash-resume via order-map reconciliation, ticker-driven cancel/recreate)
completely unmodified.

The persisted shape (`gobs.OptLimiterStateV1` — `ContractID`,
`PriorContractID`, `Intent`, `ContractSize`, `LimiterID`) is already decided
in gobs-story.md. This story is about the *behavior*: how `OptLimiter`
constructs and drives the wrapped `*limiter.Limiter`, and what adapter code
has to exist so that works.

---

## Grounding: what the wrapped limiter actually needs

Read directly from `limiter/run.go` and `trader/trader.go`, not assumed:

- `trader.Runtime` carries exactly **one** `Product exchange.Product` field.
  `Limiter.Run` calls `rt.Product.LimitBuy`/`LimitSell`/`Get`/`Cancel`/
  `GetPriceUpdates`/`GetOrderUpdates`/`BaseMinSize`/`ProductID` directly —
  nothing else. It also asserts `rt.Product.ProductID() == v.productID` at
  the top of `Run` and fails fast (`os.ErrInvalid`) otherwise.
- `point.Point.Side()` derives BUY vs SELL purely from comparing `Cancel` to
  `Price` (`Cancel < Price` ⇒ SELL, else ⇒ BUY) — there's no separate side
  field. `Point.Check()` rejects `Cancel == Price` but otherwise doesn't
  constrain how far apart they are.
- `Limiter.Run`'s ticker loop actively cancels the live order and recreates
  it whenever price crosses `Cancel` (see `run.go:140-193`) — this is grid
  trading's "chase the market" behavior, and is exactly what a written
  option order must *not* do (project.md: "cancel-offset machinery
  neutralized by a far-side cancel price — order placed once, rests").

This means: whoever owns an `OptLimiter` (`optpos`) must construct its own
`trader.Runtime` with `Product` set to the adapter, and hand it to the
wrapped `*limiter.Limiter`'s own `Run`/`Save` unmodified — matching
project.md's open item 3 ("a greeler spans the stock product plus option
contract products, so it manages product handles for its children itself"),
now concretely: `optpos` does this for each `OptLimiter` leg it owns.

---

## Scenario walkthrough

### 1. optpos opens a position: sell-to-open

`optpos` picks a contract (via `ContractSelector`, see the `optpos` story)
and constructs one `OptLimiter` with intent `"open"`, side `"SELL"`,
`numContracts`, and a limit premium. `New` builds the wrapped
`*limiter.Limiter` with:

- `productID` = the contract's `ContractID` (this is *why* it must match:
  `Limiter.Run`'s `rt.Product.ProductID() != v.productID` check means the
  adapter's `ProductID()` must also return `ContractID`, unmodified; roll
  legs differ, see scenario 6).
- `point.Size` = `numContracts` (contract units, not shares — see scenario
  3 for why).
- `point.Price` = the limit premium per contract.
- `point.Cancel` = a far-side price on the given side, chosen so the ticker
  realistically never crosses it (scenario 4).

Side is an explicit constructor argument, encoded into the point (a SELL
point has `Cancel < Price`), so `Limiter.Run`'s `create()` calls the
adapter's `LimitSell`, which the adapter maps to `LimitSellToOpen`. greel
only ever writes options, so it opens with SELL and closes with BUY; a
future debit strategy opening long would pass BUY → `LimitBuyToOpen` — the
mapping is symmetric.

### 2. The adapter must satisfy `exchange.Product` exactly

Nothing in `limiter.Limiter` changes — the adapter has to be a complete,
literal `exchange.Product`. Method by method, wrapping an
`exchange.OptionsProduct` plus a fixed `intent`:

| `exchange.Product` method | Adapter behavior |
|---|---|
| `ProductID()` | `optionsProduct.ContractID()` (roll legs don't use the adapter — scenario 6) |
| `ExchangeName()` | passthrough |
| `BaseMinSize()` | `decimal.NewFromInt(1)` — one contract, the natural minimum; the exchange doesn't express fractional contracts |
| `GetPriceUpdates()` / `GetOrderUpdates()` | passthrough |
| `LimitBuy(ctx, clientID, size, price)` | `intent == "open"` → `LimitBuyToOpen`; `intent == "close"` → `LimitBuyToClose` |
| `LimitSell(ctx, clientID, size, price)` | `intent == "open"` → `LimitSellToOpen`; `intent == "close"` → `LimitSellToClose` |
| `Get(ctx, serverID)` / `Cancel(ctx, serverID)` | passthrough |
| `Close()` | passthrough |

This is the whole adapter — no state of its own beyond the wrapped
`OptionsProduct` and the fixed `intent` string, which is exactly the "one
wrapped limiter = one order intent, matching its single-shot design"
principle from project.md.

### 3. Units: contracts, not shares — and where `ContractSize` actually applies

The wrapped limiter's `point.Size` is in **contracts**, matching
`exchange.OptionsProduct.LimitBuyToOpen(ctx, clientID, numContracts,
limitPrice)`'s own signature directly — the adapter's `LimitBuy`/`LimitSell`
pass `size` straight through as `numContracts`, no conversion.

`OptLimiterStateV1.ContractSize` (gobs-story.md) isn't for that call — it's
for the boundary between `optlimiter` and its *caller*. Options premium is
quoted **per share** (standard market convention: a $2.50 premium on a
100-share contract costs $250, not $2.50) — `optlimiter` keeps `point.Price`
in that per-share, per-market-convention quote (so it matches
`gobs.OptionContract.Price`/`Bid`/`Ask` unmodified, and so the exchange
calls receive exactly what the broker API expects). Anyone accounting in
total dollars — `optpos` tracking premium collected, `greeler` tracking
cash — multiplies by `ContractSize` (typically 100) themselves. `optlimiter`
never does this multiplication internally; it only carries `ContractSize`
on the persisted record so callers don't have to re-fetch the contract to
find it.

### 4. Cancel-offset neutralization: a formula, not just "far side"

`Limiter.Run`'s ticker loop cancels the live order once price crosses
`Cancel` and stays there past `CancelOffsetTimeout` (one minute) — correct
for grid trading, wrong for a written option that should simply rest until
filled or `optpos` deliberately cancels it. Since `point.Side()` is derived
from `Cancel` vs. `Price`, the adapter can't just omit `Cancel` — it has to
pick one the option's own ticker will not plausibly cross:

- **SELL** (`Cancel < Price`, e.g. sell-to-open, sell-to-close): set
  `Cancel` near zero (a small positive epsilon — `Point.Check()` rejects
  zero itself) rather than a percentage offset from `Price`. An option's
  price can move far more violently than the underlying (a put can 10x on
  a crash), so a small percentage below `Price` is not actually safe from
  being crossed; near-zero is the only bound that's safe regardless of how
  the premium moves.
- **BUY** (`Cancel > Price`): set `Cancel` to a large multiple of `Price`
  (e.g. `Price × 1000`) for the same reason in reverse — a fixed percentage
  above `Price` is not obviously safe against a premium spike.

This is worth a named helper (`farCancel(side, price) decimal.Decimal`) in
the adapter/constructor rather than inlined, since both scenario 1 (open)
and scenario 5 (close) need it identically.

**This only neutralizes the cancel side.** `Limiter.Run` also gates
*placement* on the ticker: a BUY order is created only while
`Price <= ticker < Cancel` (`limiter/run.go:182`), and any order is created
only when a price update arrives. A buy-to-close at limit P therefore isn't
placed while the option trades below P. See the TODO at the end of this
story.

### 5. optpos closes a position: buy-to-close

Same construction as scenario 1, `intent = "close"`, side `"BUY"` (a short
position is closed by buying back), same `ContractID` (the position hasn't
rolled). `Limiter.Run` calls the adapter's `LimitBuy` → `LimitBuyToClose` —
subject to the BUY placement gating noted in scenario 4.

### 6. Rolling: the exchange-layer gap, resolved here

gobs-story.md flagged this and deferred it explicitly to this story: a roll
is one atomic broker order across two contracts (`PriorContractID` →
`ContractID`, one fill, one net credit/debit) — `exchange.OptionsProduct`
can't express that; it's scoped to a single contract.

**Resolution — approved and implemented:** `exchange.OptionsExchange`
(exchange/api.go) gained one narrow method, already committed:

```go
type OptionsExchange interface {
    Exchange
    GetOptionsChain(ctx context.Context, underlying string) ([]*gobs.OptionContract, error)
    GetOptionsProduct(ctx context.Context, contractID string) (*gobs.OptionContract, error)
    OpenOptionsProduct(ctx context.Context, contractID string) (OptionsProduct, error)

    // OpenOptionsRollProduct opens a two-contract roll: closing
    // priorContractID and opening contractID as a single atomic broker
    // order (one fill, one net credit/debit). OptionsRollProduct is
    // exchange.Product itself, so an unmodified limiter.Limiter can trade
    // it directly: LimitSell places the roll for a net credit, LimitBuy
    // for a net debit.
    OpenOptionsRollProduct(ctx context.Context, priorContractID, contractID string) (OptionsRollProduct, error)
}

// OptionsRollProduct is exchange.Product, scoped to one roll order.
type OptionsRollProduct = Product
```

Because `OptionsRollProduct` is defined to be exactly `exchange.Product`'s
shape (a type alias, not a new interface), a roll leg needs **no adapter
code beyond `optlimiter` already having one** — `OptLimiter` for `Intent ==
"roll"` wraps a `*limiter.Limiter` built directly against the
`OptionsRollProduct`, with `productID` set to **`rollProduct.ProductID()`**
(decision #4). `Limiter.Run` refuses to run unless the product's ID equals
the limiter's, and a roll product's ID is defined by the exchange, so the
limiter takes whatever ID the exchange returned rather than inventing one.
Side here means something different than for a plain option order — SELL =
enter for a net credit, BUY = enter for a net debit — but mechanically it's
identical: one order, one fill, and the same far-cancel neutralization from
scenario 4 applies (as does its BUY-gating caveat, for net-debit rolls).

This keeps the "optlimiter reuses limiter.Limiter, unmodified" decision
(project.md decision-log #1) intact even for rolls — the new surface is
entirely at the exchange layer (one method, one type alias), not in
`limiter` or `optlimiter` itself. The `etrade` package implementing
`OpenOptionsRollProduct` against E*TRADE's real multi-leg order API is
separate, follow-on work — out of scope for this story, which only needed
the interface to exist.

### 7. Keyspace isolation: a prerequisite, not a detail

gobs-story.md's decision #1 already specified this and it's worth repeating
as a blocking dependency: `limiter.Save`/`Load` hardcode `DefaultKeyspace`
("/limiters/"). Before `OptLimiter.New`/`Save`/`Load` can exist, `limiter`
needs the additive keyspace-override option described there (e.g.
`New(..., WithKeyspace(ks))`, defaulting to `DefaultKeyspace` for every
existing caller) and `cleanUID` needs `/optlimiters/` added to its known
prefixes. This is the first implementation task for this module, before
`optlimiter.go` itself.

**TODO — isolation gaps found in design review.** Keyspace scans aren't
the only path in. Every `Limiter.Run` registers itself with limiter's
background finish-time fixer (`asyncUpdateFinishTime`), which works from
the in-memory instance, not a scan (`limiter/fix-finish-times.go`):
- it saves the limiter through its own `Save` — so the keyspace must be a
  field on the `Limiter` instance, not just a `Save`/`Load` parameter, or
  option limiters get written into `/limiters/`;
- it calls `ex.GetOrder(ctx, v.productID, id)` on the spot exchange with an
  option contract ID (or a roll product's ID). If that call fails for
  option orders, the fixer retries every 5 seconds indefinitely.

### 8. Leak mitigation recap (from project.md, now concrete)

- **Cancel-offset machinery neutralized** — scenario 4's far-cancel formula
  (cancel side only; placement gating is the TODO at the end).
- **Contract/premium units scaled by `ContractSize()` at the wrapper
  boundary** — scenario 3: never inside `optlimiter`, always by its caller,
  using the field `optlimiter` carries but doesn't consume itself.
- **Own keyspace (`/optlimiters/`)** — scenario 7, so `/limiters/`-scanning
  background tasks (`limiter/all.go`, `limiter/fix-finish-times.go`'s scan)
  skip option orders; the fixer's in-memory path is a TODO (scenario 7).

---

## Proposed code skeleton

```go
// Copyright (c) 2026 Deepak Vankadaru

package optlimiter

import (
    "context"
    "fmt"

    "github.com/bvk/tradebot/exchange"
    "github.com/bvk/tradebot/gobs"
    "github.com/bvk/tradebot/limiter"
    "github.com/bvk/tradebot/point"
    "github.com/bvk/tradebot/trader"
    "github.com/bvkgo/kv"
    "github.com/google/uuid"
    "github.com/shopspring/decimal"
    "github.com/visvasity/topic"
)

const DefaultKeyspace = "/optlimiters/"

// Intent is fixed at construction — one wrapped limiter, one order intent.
// These are also the values persisted in gobs.OptLimiterStateV1.Intent;
// side (BUY/SELL) is encoded in the wrapped limiter's point, not here.
type Intent string

const (
    IntentOpen  Intent = "open"  // sell-to-open / buy-to-open
    IntentClose Intent = "close" // buy-to-close / sell-to-close
    IntentRoll  Intent = "roll"  // one atomic order, two contracts
)

// product adapts one exchange.OptionsProduct (plus a fixed Intent) into
// exchange.Product, so an unmodified limiter.Limiter can trade it. Holds
// no state beyond what it wraps — see scenario 2. ExchangeName() is a
// straight passthrough, per decision #2's OptionsProduct.ExchangeName()
// addition.
type product struct {
    wrapped exchange.OptionsProduct
    intent  Intent
}

var _ exchange.Product = (*product)(nil)

// NewProduct builds the exchange.Product adapter for one option order.
// optpos constructs one of these per OptLimiter leg and passes it as
// trader.Runtime.Product when calling the wrapped limiter's Run — see
// "Grounding" above.
func NewProduct(wrapped exchange.OptionsProduct, intent Intent) exchange.Product {
    return &product{wrapped: wrapped, intent: intent}
}

func (p *product) ProductID() string    { return p.wrapped.ContractID() }
func (p *product) ExchangeName() string { return p.wrapped.ExchangeName() }
func (p *product) BaseMinSize() decimal.Decimal { return decimal.NewFromInt(1) }
func (p *product) Close() error         { return p.wrapped.Close() }

func (p *product) GetPriceUpdates() (*topic.Receiver[exchange.PriceUpdate], error) {
    return p.wrapped.GetPriceUpdates()
}
func (p *product) GetOrderUpdates() (*topic.Receiver[exchange.OrderUpdate], error) {
    return p.wrapped.GetOrderUpdates()
}

func (p *product) LimitBuy(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
    if p.intent == IntentOpen {
        return p.wrapped.LimitBuyToOpen(ctx, clientID, size, price)
    }
    return p.wrapped.LimitBuyToClose(ctx, clientID, size, price)
}

func (p *product) LimitSell(ctx context.Context, clientID uuid.UUID, size, price decimal.Decimal) (exchange.Order, error) {
    if p.intent == IntentOpen {
        return p.wrapped.LimitSellToOpen(ctx, clientID, size, price)
    }
    return p.wrapped.LimitSellToClose(ctx, clientID, size, price)
}

func (p *product) Get(ctx context.Context, serverID string) (exchange.OrderDetail, error) {
    return p.wrapped.Get(ctx, serverID)
}
func (p *product) Cancel(ctx context.Context, serverID string) error {
    return p.wrapped.Cancel(ctx, serverID)
}

// OptLimiter is one option order: a limiter.Limiter wrapped with a fixed
// Intent and contract identity. Component, not a job — owner-driven, no
// jobs.Register entry (project.md job hierarchy).
type OptLimiter struct {
    uid string

    contractID      string
    priorContractID string // set only for IntentRoll
    intent          Intent
    contractSize    decimal.Decimal

    limiter *limiter.Limiter
}

// New constructs an OptLimiter for a single-contract order (IntentOpen or
// IntentClose). side is "BUY" or "SELL"; greel opens with SELL and closes
// with BUY (scenarios 1, 5).
func New(uid, exchangeName, contractID string, intent Intent, side string, contractSize, numContracts, limitPrice decimal.Decimal) (*OptLimiter, error) {
    p := &point.Point{Size: numContracts, Price: limitPrice, Cancel: farCancel(side, limitPrice)}
    lim, err := limiter.New(uid, exchangeName, contractID, p /*, limiter.WithKeyspace(DefaultKeyspace) */)
    if err != nil {
        return nil, fmt.Errorf("could not create wrapped limiter: %w", err)
    }
    return &OptLimiter{
        uid: uid, contractID: contractID, intent: intent,
        contractSize: contractSize, limiter: lim,
    }, nil
}

// NewRoll constructs an OptLimiter for one atomic roll order. productID
// must be rollProduct.ProductID() (scenario 6, decision #4). side is
// "SELL" for a net credit, "BUY" for a net debit.
func NewRoll(uid, exchangeName, productID, priorContractID, contractID string, side string, contractSize, numContracts, netPrice decimal.Decimal) (*OptLimiter, error) {
    panic("unimplemented") // same as New, with IntentRoll and priorContractID set
}

// Run drives the wrapped limiter. rt.Product is NewProduct(...) for open/
// close legs, or the OptionsRollProduct itself for roll legs.
func (v *OptLimiter) Run(ctx context.Context, rt *trader.Runtime) error {
    return v.limiter.Run(ctx, rt)
}

func (v *OptLimiter) UID() string { return v.uid }

// farCancel picks a cancel price the ticker will not plausibly cross, so
// Limiter.Run's cancel/recreate loop never fires — see scenario 4.
func farCancel(side string, price decimal.Decimal) decimal.Decimal {
    if side == "SELL" {
        return decimal.NewFromFloat(0.0001)
    }
    return price.Mul(decimal.NewFromInt(1000))
}

func (v *OptLimiter) Save(ctx context.Context, rw kv.ReadWriter) error {
    // Saves v.limiter (under DefaultKeyspace, via the pending keyspace-
    // override option) then this record's own ContractID/PriorContractID/
    // Intent/ContractSize/LimiterID — see gobs-story.md's OptLimiterStateV1.
    panic("unimplemented")
}

func Load(ctx context.Context, uid string, r kv.Reader) (*OptLimiter, error) {
    panic("unimplemented")
}
```

Left as `panic("unimplemented")` deliberately — `Save`/`Load` are function
skeletons per the workflow stage this story is for; the shapes above are
what the next stage fills in.

---

## Decisions made at this checkpoint

1. **`OpenOptionsRollProduct`/`OptionsRollProduct` addition to
   `exchange/api.go` — approved and implemented.** The one piece of this
   story that reaches outside `optlimiter` itself, into an already-
   committed file — signed off and landed in scenario 6's shape (one new
   `OptionsExchange` method, `OptionsRollProduct` as a type alias for
   `exchange.Product`, no new adapter code needed for roll legs). Named to
   match the package's existing `Options`-prefixed convention
   (`OptionsProduct`, `OptionsExchange`, `OpenOptionsProduct`) rather than
   the first-draft `RollProduct`/`OpenRollProduct`. The corresponding
   `etrade` implementation of `OpenOptionsRollProduct` against E*TRADE's
   real multi-leg order API is separate follow-on work this story doesn't
   cover.
2. **`ExchangeName()` accessor added to `exchange.OptionsProduct` —
   approved and implemented.** A second, smaller addition to the same already-committed
   file: `OptionsProduct` gains `ExchangeName() string`, mirroring
   `Product`'s existing accessor. The adapter's own `ExchangeName()`
   becomes a straight passthrough (`p.wrapped.ExchangeName()`) rather than
   carrying the name separately — no divergence between what the adapter
   reports and what the underlying options product actually is.
3. **Far-cancel constants (`0.0001`, `×1000`) — approach approved, exact
   formula deferred.** Direction: derive the bound from the contract's own
   price scale at implementation time rather than shipping fixed
   constants, so they stay sane across a $0.50 weekly option and a $500
   LEAP. The specific formula isn't decided here — it's implementation
   work for this module's next stage.
4. **A roll leg's limiter uses `rollProduct.ProductID()` — decided after
   design review.** Replaces the first draft's invented `prior->new` key,
   which the exchange's roll product would have had no reason to match —
   and `Limiter.Run` refuses to run on a mismatch (`limiter/run.go:27`).
5. **Intent vocabulary is `open | close | roll`, side is a constructor
   argument — decided after design review.** The first draft persisted
   `sell-to-open`-style intents in `gobs` while `optlimiter` used
   `open|close|roll`, and `New` hardcoded SELL even for buy-to-close legs.
   Now one vocabulary is shared, and side is encoded in the wrapped
   limiter's point, the way `Limiter` already derives buy/sell.

## TODO: replace the wrapped `Limiter` with an option order state machine

Must be resolved before this module is implemented.

**Problem.** Unmodified `Limiter.Run` creates a BUY order only while
`Price <= ticker < Cancel`, and creates any order only on a price update
(scenario 4). That blocks profit-take buy-to-close legs and net-debit
rolls, and stalls on quiet contracts. Separately, option spreads are wide,
so **re-pricing an unfilled order will be routine** — and `Limiter` trades
one fixed price per instance. Both meet project.md's own escape-hatch
condition ("suppress rather than translate a behavior → fork").

**Likely direction (not yet decided).** `OptLimiter` becomes its own small
state machine: one order intent (e.g. sell-to-open 1 contract) that places
a broker order immediately, re-prices on a timer by cancelling and
re-placing until filled or stopped, and keeps every broker order it placed.
It would reuse `idgen` and `exchange.SimpleOrder` directly, and copy
`Limiter`'s crash-recovery and cancel-then-confirm patterns. Considered and
rejected:
- *An opt-in "resting" mode on `Limiter`*: every re-price would be a new
  leg — two KV records per attempt, legs that never filled cluttering the
  position's chain, and partial fills split across legs.
- *Feeding the limiter fake prices*: exactly the "suppress" hack the escape
  hatch forbids.

**Consequences if taken:**
- Removes the `OptionsProduct`→`Product` adapter (call the four verbs
  directly), the far-cancel workaround, the `limiter` keyspace override, and
  the keyspace-isolation TODO in scenario 7 — `optlimiter` would no longer
  touch the `limiter` package or `/limiters/`.
- `gobs.OptLimiterStateV1` drops `LimiterID` and stores its own orders and
  client-ID seed/offset, like `LimiterStateV2`; gobs-story decision #1
  (embed vs. reference) becomes moot.
- project.md decision-log #1 ("optlimiter reuses limiter.Limiter") is
  superseded; this story is largely rewritten.
- `OptionsRollProduct` stays as is — roll orders still go through its
  `LimitSell`/`LimitBuy`.

**Re-price rule, proposed:** start at the mid price; every N minutes, move
toward the far side of the spread by a set step; never past a limit the
caller sets (minimum premium for a sell-to-open, maximum cost for a
buy-to-close). Step and interval become `WheelKnobs`, so they persist. A
roll is priced on the net of both contracts' quotes.
