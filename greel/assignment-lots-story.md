# Story: tax lots for assigned shares

Follow-up to [tax-lots-story.md](tax-lots-story.md), which left two cases
selling by the account's default lot method:

- shares a put assignment brought in (its decision 3), and
- a level a call assignment left partly held (its decision 4).

This story plans how both can sell by lot. Some of it rests on facts about
how E*TRADE reports an assigned lot that nobody has seen yet, so it says
which parts wait for that check (scenario 1) and which don't.

---

## Scenario walkthrough

### 1. What we need to see first

A grid buy's lot is found by the buy's order number: the portfolio lists
each lot with an `orderNo`, and `ResolveTaxLots` matches it
(`etrade/client.go`, `resolveLotsFromCache`). An assignment has no order of
ours, so we don't know what E*TRADE puts there. Today's code also assumes
`orderNo` is set: the lot cache skips lots whose `orderNo` is 0
(`updatePortfolio`), and a tax lot ID can't name one (`makeTaxLotID`).

The check: when the account holds shares from an assignment (from greel or
from a manual put), run

```
tradebot etrade get-portfolio
```

and look at that lot's `positionLotId`, `orderNo`, `originalQty`,
`remainingQty`, `price` and `acquiredDate`. The questions are:

- Is `orderNo` 0, or some number? If a number, is it the option's order?
- Is `price` the strike, or the strike less the premium per share?
- Is `acquiredDate` the assignment day (the Monday after a Friday expiry,
  say) or the day we wrote the put?
- Is it one lot of 100 shares?

Scenario 3's matching rule waits for these answers.

**What the first assignment showed (2026-11-24).** An NVDA 167.5 put, sold
for 1.90 with a 0.52 fee (order 742), was assigned on 2026-11-23. The
portfolio showed one lot: `originalQty` 100, `acquiredDate` midnight New
York time on 2026-11-23, `price` 165.6052, and `orderNo` 743. That price is
the strike less the premium kept net of fees, per share: 167.5 − (190 −
0.52) / 100. Order 743 is E*TRADE's assignment order, which `get-order`
can't read (it skips it as a multi-instrument order), so it can't tie the
lot to the position. The transactions were "Option Assigned" (OPTN, which
`settlementKind` already reads as assigned) and "Bought" (EQ, 100 at
167.5, described as "PUT ASN"). Scenarios 2, 4 and 5
don't, and their code can go first.

### 2. Naming a lot without an order number, and part of a lot

Two changes to the E*TRADE tax lot ID, which is a URL with the lot detail
path and query values `AccountID`, `Symbol` and `OrderNo`:

- **`PositionLotID`:** an optional value naming the lot by E*TRADE's own
  lot ID. When it is there, `OrderNo` may be 0, and resolving matches the
  lot by `positionLotId` instead of `orderNo`. The lot cache keeps lots
  with no `orderNo` in its per-position lists (it still leaves them out of
  the by-order index, which they can't be in).
- **`Qty`:** an optional value capping how many shares the sell takes from
  that lot. A put assignment is one 100-share lot split over several
  levels, and today `PlaceLimitOrder` always sells a named lot's whole
  remaining quantity and requires the lots to add up to the order size
  exactly. With `Qty`, each lot gives `min(Qty, remaining)`; if the total
  is more than the order size, the last capped lots are trimmed down to
  it. That handles a level's sell that has partly filled: it names 25
  shares of the lot, has 15 left to sell, and the order draws 15.

Lot IDs without the new values work exactly as today, so looper and
existing records see no change: the equality check stays strict for them.
`TaxLotID.Equal` compares the URL path and the `Symbol` and `OrderNo`
values. It learns to compare `PositionLotID` when both IDs have it and to
ignore `Qty`, so two levels' parts of one lot count as the same lot. (It
also reads an `Account` value that `makeTaxLotID` never writes; the ID
says `AccountID`. The path already holds the account key, so this hasn't
mattered, but unit 1 fixes it.)

One unknown remains here: whether E*TRADE lets two open sells draw on the
same lot at once (two levels selling their parts at the same time). If it
doesn't, the second one's order fails and the limiter retries after the
first fills or is canceled, which is slow but safe. We'll learn it from
the first real run.

### 3. Finding the assigned lot

A new optional interface on the stock product, implemented by
`etrade.AccountProduct`:

```go
// AssignedLotFinder finds the lot a put assignment created.
type AssignedLotFinder interface {
	FindAssignedLot(ctx context.Context, fact *gobs.AssignmentFact, at time.Time) (gobs.TaxLotID, error)
}
```

It refreshes the portfolio and looks among the symbol's lots for exactly
one that matches the assignment: `originalQty` equal to the shares, some
still held, an `acquiredDate` within three days of the assignment's New
York date, and a `price` within a cent of the expected basis. The greeler
passes the basis, since it knows the position's premiums: the strike less
the premium kept net of fees per share. If no lot or more than one lot
matches, it returns an error and says why. The interface takes the basis
in place of the fact:

```go
FindAssignedLot(ctx context.Context, shares decimal.Decimal, at time.Time, basis decimal.Decimal) (gobs.TaxLotID, error)
```

It names the lot by `PositionLotID`, which is exact even though this lot
also has an `orderNo`.

### 4. Where the greeler keeps it

The wheel epoch gets a new field:

```go
type GreelEpoch struct {
	...
	AssignedLot gobs.TaxLotID // wheel epochs whose put was assigned; "" until found
}
```

It is kept on the greel record, not on the position, because only the
greeler has the stock product and knows the shares are now its levels'.

After a put assignment, the greeler looks the lot up and saves it. The
lookup runs before a level that holds those shares starts its sell. In
`sellSources`, a put assignment's share of a level adds that lot with
`Qty` set to the level's part, instead of marking the level unlotted.

While the lot isn't found, the levels holding assigned shares wait, like a
buy with no lots (tax-lots-story decision 2), and try again after
`retryDelay`. That needs a limit, because a lookup that never matches
would hold the shares forever. The plan is to wait up to one trading day
(E*TRADE may post the lot late, and a weekend assignment's shares arrive
on Monday anyway). After that, the greeler sends a message and sells
those shares by the default method, as today. That is decision 3 below.

### 5. Partial call assignment

A call assignment takes 100 shares out of the levels, lowest first, but
E*TRADE picks which lots go. A level left partly held may still have its
own lots (if E*TRADE took the lower levels' lots), or not.

Instead of always marking that level unlotted, the greeler checks before
its sell: it resolves the level's source lots (`ResolveTaxLotIDs`), and if
their remaining quantity covers the level's holding, the sell names them
with `Qty` (scenario 2). If they don't, the sell goes out unlotted and the
greeler logs it, as today. This needs nothing from scenario 1.

---

## Code

Four units, each its own PR into greel-v1:

1. **`etrade`: `PositionLotID` and `Qty` on tax lot IDs** (scenario 2):
   `makeTaxLotID`/`parseTaxLotID`, `TaxLotID.Equal`, keeping lots without
   an `orderNo` in the lot cache, and the capping and trimming in
   `PlaceLimitOrder`. Tests show old IDs behave as before.
2. **`greeler`: partial call levels** (scenario 5): `sellSources` keeps a
   partly called level's buys, and `sellLots` checks their remaining
   quantity before naming them with `Qty`.
3. **`exchange`/`etrade`: `AssignedLotFinder`** (scenario 3), after
   scenario 1's check.
4. **`gobs`/`greeler`: `AssignedLot`** (scenario 4): look it up and save it
   after a put assignment, the level wait with its one-day limit, and
   `sellSources` naming the lot with each level's `Qty`.

Units 1 and 2 can go now. Units 3 and 4 wait for scenario 1.

---

## Decisions to make at this checkpoint

1. **Extend the tax lot ID with optional `PositionLotID` and `Qty`**
   instead of adding a new sell call that takes lot quantities; old IDs
   behave as before (scenario 2).
2. **Keep the assigned lot on the greel's wheel epoch,** not on the
   position (scenario 4).
3. **If the assigned lot isn't found within one trading day, notify and
   sell those shares by the default method** (scenario 4). The other
   choice is to keep waiting until the operator steps in. Recommended:
   notify and fall back, so the shares don't sit unsold.
4. **Check a partly called level's lots before naming them,** instead of
   always selling it unlotted (scenario 5).
5. **Land units 1 and 2 now; units 3 and 4 after the portfolio check**
   (scenario 1).
