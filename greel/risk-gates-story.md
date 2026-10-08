# Story: risk gates — ladder-level limits on written options

Companion to [project.md](project.md) (open item 6),
[greelladder-story.md](greelladder-story.md) (scenario 4, decision 5) and
[greeler-story.md](greeler-story.md) (scenario 4, decision 3). Both stories
left the gates as a TODO after the first design, which had the ladder call
`SetOption("freeze", "wheel")` on running greelers, was found to break the
`trader.Trader` contract: options change only while a job isn't running,
and the ladder couldn't tell its own freezes apart from an operator's.

This story replaces that design with an admission hook, the same shape as
the sibling `Exclude` hook the ladder already hands its greelers.

---

## Scenario walkthrough

### 1. What a gate has to bound

A greeler writes at most one contract at a time: it has one position per
wheel epoch, and the constraint it passes leaves `NumContracts` at zero,
which means one (`greeler/run.go:325`, `optpos/optpos.go:390`). So across a
ladder:

- **Open contracts** is the number of greelers in wheel mode. A greeler
  counts from the moment it flips until its wheel epoch ends (expired,
  assigned, or abandoned unfilled). An opening order that hasn't filled
  still counts, since it can fill at any moment.
- **Assignment exposure** is the cash the ladder would pay if every open
  put were assigned: strike × contract size, summed over the greelers
  writing puts. Calls are covered by shares the levels already hold, so an
  assignment moves no cash in; they count toward open contracts but not
  toward exposure.

Every greeler's put collateral already fits in its own budget (accounting-
story scenario 7: the strike is at or below the lowest buy price), so
exposure never exceeds the ladder's `BudgetAt`. The gate is for the case
where the operator doesn't want all of that committed to puts at once,
such as a ladder sharing cash with other jobs, or a sharp drop that would
assign every band in the same week.

### 2. Admission, not freezing

The ladder hands each greeler an `Admit` hook, like `Exclude`:

```go
// Admit reports whether the greeler may flip to wheel mode to write one
// contract of optionType whose strike is at most maxStrike (puts) and
// reserves the room if so. Nil when the greeler runs standalone.
func(uid, optionType string, maxStrike decimal.Decimal) bool
```

`stepGrid` asks it once the dwell clock has run out and the levels
qualify, right before `flipToWheel`. If the ladder says no:

- the greeler stays in grid mode and the dwell clock stays set, so it asks
  again on the next step and flips as soon as there is room;
- nothing is written and no option is touched, so the operator's own
  `freeze`/`retire` stay exactly as they set them;
- an open position is never affected: gates only stop new entries, and in
  v1 a written option ends only by broker settlement.

A standalone greeler has no hook and no limit, as with exclusion.

### 3. How exposure is counted: the strike bound, not the strike

A put's exposure is counted at its `MaxStrike` bound (the greeler's lowest
buy price, `greeler/run.go:325-337`) times 100, not at the strike optpos
later selects. The bound is known before selection, doesn't move when
optpos re-selects a contract the next session, and is never lower than the
real strike, so the gate can't be passed by a strike chosen after
admission. The cost is that the gate is conservative by the gap between
the lowest buy price and the selected strike.

### 4. Races between siblings

Two siblings can reach their flip in the same instant. The ladder admits
under its own lock and keeps a reservation for each admitted greeler until
that greeler reports itself in wheel mode, with the same one-minute TTL as
a contract claim (`greelladder/greelladder.go:30`), in case the flip fails
before its epoch is saved. A count is: siblings that report wheel mode,
plus fresh reservations of siblings that don't yet.

Each greeler reports its commitment the way it reports `HeldContract`: the
option type and strike bound of its current wheel epoch, or none in grid
mode, with a `known` flag that is false until `Run` has loaded its
position. While any sibling isn't known, the ladder admits nothing, which
is the same rule exclusion uses.

### 5. Setting the limits: ladder options

Two new ladder options, both zero (no limit) by default:

- `max-contracts=N`: at most N greelers in wheel mode at once.
- `max-put-exposure=V`: at most V in open put exposure (scenario 3), in the
  quote currency.

They are the ladder's own options, kept in `GreelLadderStateV1.Options`,
which already exists and is saved today as nil. Unlike `freeze` and
`retire` they aren't forwarded to the greelers. Like every option they
change only while the ladder isn't running: pause, set, resume. Lowering a
limit below what is already open blocks new entries until enough positions
settle; it closes nothing.

There is no creation-time config for them, because there is no ladder
creation subcommand yet; options cover it, and the earlier
`maxOpenContracts`/`maxAssignmentValue` constructor config (greelladder-
story decision 5) is dropped.

### 6. Kill switch: freeze already is one

Project.md names a kill switch next to the limits. Pausing the ladder and
setting `freeze=wheel` (no new positions) or `freeze=all` (no new limiters
either) on it is that switch: the ladder forwards it to every greeler, and
it survives restarts. A gate that answers "no" to everything would duplicate
it, so there is no separate kill switch, and `max-contracts=0` keeps
meaning "no limit".

### 7. Telling the operator

The ladder sends one messenger alert when a greeler is first blocked by a
gate ("greeler X wants to write a PUT but the ladder is at its contract
limit 2/2"), and not again until that greeler has been admitted. Blocks
are also logged at debug on every step, as a failed qualification is.

### 8. What the gates don't see

Like exclusion, gates count only the ladder's own greelers. Standalone
greelers, other ladders, and manual option trades on the same account are
invisible to them. An account-wide limit needs the exchange's view of open
option positions and is left for when an exchange implements
`OptionsExchange`.

Freed room goes to whichever blocked greeler steps first; there is no
queue or priority between bands in v1.

---

## Code

- `greeler`: `SetAdmit`; `stepGrid` asks the hook before `flipToWheel`;
  `Commitment() (optionType string, maxStrike decimal.Decimal, known bool)`
  kept current where `updateHeld` is.
- `greelladder`: `max-contracts` and `max-put-exposure` in `SetOption`,
  saved and loaded through `GreelLadderStateV1.Options`; `admitFor`/`admit`
  with reservations under `mu`; the one-alert-per-block notifier.
- `gobs`: no new fields. `GreelLadderProgress` stays empty, since nothing
  the gates track needs to survive a restart.

---

## Proposed decisions

1. **Gates are an admission hook the ladder hands its greelers**, asked
   before a flip to wheel mode; they never set options on a running
   greeler (scenario 2).
2. **A blocked greeler stays in grid mode with its dwell clock set** and
   retries every step (scenario 2).
3. **Gates stop new entries only**; open positions run to settlement
   (scenario 2).
4. **Two limits: open contracts (puts and calls) and put exposure**; calls
   carry no cash exposure (scenario 1).
5. **Put exposure is counted at the strike bound** × 100, not the selected
   strike (scenario 3).
6. **Admission reserves room under the ladder's lock**, and nothing is
   admitted while a sibling's commitment isn't known (scenario 4).
7. **Limits are ladder options, zero meaning no limit**, changed only while
   the ladder is paused; the constructor config from greelladder-story
   decision 5 is dropped (scenario 5).
8. **No separate kill switch**: ladder `freeze` is it (scenario 6).
9. **One alert per block** (scenario 7).
10. **Gates count only the ladder's own greelers** in v1 (scenario 8).
