# Story: tax lots on greel stock sells

Companion to [greeler-story.md](greeler-story.md) (scenario 1, the fold,
and scenario 3, the allocation rule) and
[accounting-story.md](accounting-story.md) (per-level cost lots, which are
bookkeeping only and are not the broker's tax lots this story is about).

Today a greeler's stock sells go out with no tax lots. Every grid buy
already resolves and saves the broker lots it created (`limiter/run.go`,
on every buy limiter of a product that is an `exchange.TaxLotResolver`),
and `Limiter.limitSell` already passes a sell limiter's lots to
`LimitSellTaxLots`. But the greeler never calls `SetTaxLotIDs` on its
sell limiters, so they carry none and E*TRADE sells by the account's
default lot method (FIFO, say). In an account that also runs loopers or
holds the same stock by hand, that default can sell somebody else's lot:
the realized gain is wrong, and the looper's own sell later fails because
its lot is gone.

`Looper` already solves this for one buy/sell pair
(`looper/run.go`: `addNewSell`, `fixBuyTaxLots`,
`unsoldBuysMissingTaxLots`, `unusedTaxLots`). This story carries the same
mechanics into the greeler, per level.

Options are out of scope: an option order has no tax lots.

---

## Scenario walkthrough

### 1. Which lots a level's sell should name

A level cycles flat → buy → sell → flat. A sell is always sized to the
level's whole holding (`armLevels`), so a completed sell leaves the level
flat, and the shares a new sell closes are exactly the shares the level
took in since it was last flat. In the common case that is one buy
limiter's fills; it can be more than one only in records from older code.

So a level's **sources** are the entries of its replay since its holding
was last zero, walked in the same order as `fold` (grid epochs' limiters
in `LevelLimiterIDs[i]` order, each assigned wheel epoch's share at the
point the epoch ended):

- a buy limiter with fills adds its tax lots;
- a sell limiter subtracts its filled size;
- a put assignment's share adds shares with **no** lots (scenario 3);
- a call assignment's share subtracts shares (scenario 4);
- whenever the running holding returns to zero, the sources are cleared.

The sell names the lots of the buy sources, the same set looper's
`unusedTaxLots` would give for that level. Since a buy limiter belongs to
one level, a lot never spans two levels, and E*TRADE's rule that the
named lots' remaining quantity add up to the order size holds.

### 2. Making sure the buys' lots are there first

A buy limiter resolves its lots when it completes. If that fails (E*TRADE
hadn't posted the lot yet, say) the limiter returns an error, but nothing
retries it: run again, a limiter with nothing pending returns at once
(`limiter/run.go`, before the resolution), and `armLevels` doesn't resume a
filled buy anyway; after `retryDelay` the level just moves on to its sell.
Buys that completed under an older build may lack lots too.

Like looper's `FIX-TAX-LOTS` step, before a level starts a new sell on a
product that is a `TaxLotResolver`, the greeler resolves lots for any
source buy that lacks them (`ResolveTaxLots` on the buy's server order IDs,
checking the resolved size equals the buy's filled size, then saving the
buy). If any is still missing, **that level** skips its sell this step and
tries again on the next one. Unlike looper, which has one pair, other
levels carry on: one level's stuck lot doesn't hold the rest of the greel.

### 3. Put assignment: shares with no buy behind them

A put assignment brings 100 shares into the levels, lowest first, with no
buy limiter, so there are no resolved lots to name. Worse, the assignment
is one broker lot split over several levels, and `LimitSellTaxLots` sells
a named lot's whole remaining quantity, so even with the lot in hand a
level couldn't sell its 25-share part of it.

Two ways to do it properly, both needing facts we don't have yet:

- find the assignment's lot among the account's lots for the symbol (by
  acquired date, the strike as cost, 100 shares; its `orderNo` is
  unknown), and
- teach the E*TRADE order code to sell part of a lot (its order request
  already carries a quantity per lot; today it always sends the lot's
  remaining quantity).

So for now a level whose sources include a put assignment **sells without
lots**, as today, and the greeler logs that it did. This is the follow-up
already on file: after the first real assignment, look at what E*TRADE
reports for the assigned lot and design the lookup then.

### 4. Call assignment: the broker picks the lots

A call assignment takes 100 shares out of the levels, lowest first. Which
physical lots leave is E*TRADE's choice (its default method), not ours.
Every level qualifying for a call is full, and with the ladder's cut
(`greelladder.Cut`) the levels add up to at least 100, so most levels are
emptied and their sources cleared. A level left partly held (the top level
of a greel whose levels add up to more than 100) may have lost some of its
lots and kept others, so its recorded lots can't be trusted: that level's
sources are marked unlotted, and its next sell goes out without lots, as
in scenario 3.

### 5. Sells that already exist

Running greelers have sell limiters created without lots. When a level
resumes a sell limiter that has no lots:

- if it has **not filled anything**, it gets the level's lots now (after
  scenario 2's check), saved before it runs, as looper does for a resumed
  sell;
- if it has partly filled, it keeps going without lots. Its earlier fills
  went by the default method, so the buy's lots may no longer add up to
  what's left to sell, and naming them would fail the order every time.
  (Looper assigns lots in this case too; we don't copy that.)

A sell that already has lots is resumed unchanged.

### 6. When the named lots don't match

If a lot was sold from outside the greeler (by hand, or by another job's
untargeted sell), E*TRADE's lots no longer add up to the order size and
`PlaceLimitOrder` refuses the order.

The common way this happens is a call assignment. E*TRADE picks the
assigned shares by the account's lot method (lowest cost first, say), and
those are often the cheaper lots of a sibling greel or a looper in the same
account, not the called greel's own. Retrying forever would leave that
sibling's shares unsold.

So (revised 2026-11-20, replacing the first version's "no fallback") the
greeler checks before it names lots: on a product that is an
`exchange.TaxLotChecker`, if the lots no longer hold the sell's size, the
sell goes out without lots, logged and sent as a message. If the lots are
taken while a sell is working, its next order fails with
`exchange.ErrTaxLotsMismatch`; the greeler drops the sell's lots, sends the
same message, and retries it without them.

### 7. What doesn't change

- Buys, wheel mode, option orders, the ladder: no change. The ladder runs
  its greelers' own `Run`, so they get this for free.
- Accounting stays the per-level FIFO replay of accounting-story.md. Tax
  lots change what the broker reports as each sale's basis, not what the
  greeler's Summary computes.
- Products that aren't `TaxLotResolver`/`TaxLotSeller` (Coinbase, tests)
  behave as today: buys have no lots, sells name none.
- No new gobs fields. Lots already live in each limiter's record
  (`gobs.LimiterStateV2.TaxLots`), and sources are a replay, like the fold.

---

## Code

Two units, each its own PR into greel-v1:

1. **`greeler/taxlots.go`:** the per-level source walk of scenario 1, with
   scenarios 3 and 4's unlotted marking. It returns, for level *i*, the
   source buys, their lots, and whether the level is unlotted. Pure; unit
   tests cover a plain buy/sell cycle, repeated cycles, put-assigned
   shares, a partial call assignment, and the trailing unfinished sell
   (left out of its own sources).
2. **`greeler/run.go`:** `armLevels` uses it. A new sell gets the level's
   lots via `SetTaxLotIDs` before `addLimiter` saves it; scenario 2's
   resolve-and-skip runs first on a `TaxLotResolver`; a resumed lot-less
   sell with no fills gets lots per scenario 5. Tests with a fake product
   that implements `TaxLotResolver` and `TaxLotSeller` check the lots on
   new and resumed sells, the skip when a lot is missing, and the
   unlotted cases.

---

## Decisions made at this checkpoint

1. **A level's sell names the lots of the buys since the level was last
   flat,** found by replaying the level, as the fold does (scenario 1).
2. **Missing buy lots are resolved before the sell, per level;** a level
   that still lacks them skips its sell for the step, and the other levels
   carry on (scenario 2).
3. **Put-assigned shares sell without lots for now,** until we know how
   E*TRADE reports the assigned lot (scenario 3).
4. **A level left partly held by a call assignment sells without lots**
   (scenario 4).
5. **Existing lot-less sells get lots only if they haven't filled yet**
   (scenario 5).
6. **A sell whose named lots no longer cover it sells by the default lot
   method and sends a message** (scenario 6; revised from "no fallback"
   after call assignments were seen to take siblings' lots).
