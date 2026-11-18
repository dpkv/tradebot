# Story: adding greel jobs — `greeler add` and `greelladder add`

Companion to [project.md](project.md), [greeler-story.md](greeler-story.md),
[greelladder-story.md](greelladder-story.md) and
[risk-gates-story.md](risk-gates-story.md). Every greel package is built,
and E*TRADE implements the accounts they need
([etrade-options-story.md](etrade-options-story.md)), but nothing creates a
greel job. The server loads greelers and ladders that are already in the
database (`server/load.go`); there is no endpoint and no subcommand that
puts one there. The other jobs each have both: `/trader/wall` and
`tradebot waller add`, and so on (`api/wall.go`, `server/server.go`
`doWall`, `subcmds/waller/add.go`).

This story adds the same pair for a single greeler and for a ladder.

---

## Scenario walkthrough

### 1. Two endpoints, shaped like the wall

- `POST /trader/greel` takes `api.GreelRequest{Config *gobs.GreelConfig,
  Options map[string]string}` and answers `{UID}`.
- `POST /trader/greelladder` takes `api.GreelLadderRequest{AccountName,
  ProductID string, Bands []*gobs.GreelConfig, Options map[string]string}`
  and answers `{UID}`.

The handlers follow `doWall`:

1. `req.Check()`.
2. Resolve the account. It must exist and implement
   `exchange.OptionsAccount`; anything else is rejected here rather than
   failing later inside `Run`, where the only trace is a job in a failed
   state.
3. `getProduct(account, product)`, which also checks the product is enabled
   on the exchange.
4. `greeler.New(uuid, cfg)` or `greelladder.New(uuid, account, product,
   bands)`. These constructors already validate everything about the config
   (grid order, equal sizes, one contract's worth of shares, `0 < grid < far`,
   hysteresis below far, dwell not negative, a registered selector), so the
   handler adds no rules of its own.
5. Apply `Options` with `SetOption`, then save and `runner.Add` in one
   transaction, then `runner.Resume`.

The request carries the gobs configs directly: the server is the single
place configs are validated, and the subcommand just builds them.

### 2. Options at creation: the risk gates need them

The risk gates are ladder options (`max-contracts`, `max-put-exposure`),
and options change only while a job isn't running
(`server/jobs.go` `doJobSetOption`). Without them at creation, a new ladder
starts running with no limits, and the only way to set them is pause, set,
resume, with a window in between where it can write contracts unlimited.
risk-gates-story scenario 5 left this open because no creation subcommand
existed.

So both requests take `Options`, applied before the first save. The ladder
subcommand has `--max-contracts` and `--max-put-exposure`; both subcommands
take repeated `--option key=value` for anything else (`freeze`, for a job
that should start frozen). An unknown key or bad value fails the request,
since `SetOption` rejects it.

### 3. The spec file

A greeler config has a grid, three zone percentages, a dwell time, a
selector name and eight wheel knobs; a ladder is a list of those. That is
too much for flags, and a ladder's bands differ from each other. So both
subcommands read a JSON spec file:

```sh
tradebot greeler add --spec aapl.json --name aapl-greel
tradebot greelladder add --spec aapl-ladder.json --name aapl-ladder --max-contracts 4
```

The spec is the subcommand's own schema, not gobs JSON, so it can be written
by hand: percentages and prices as numbers or strings, durations as Go
duration strings (`"48h"`), lower-case dashed keys. Unknown keys are an
error, so a typo doesn't silently become a zero.

```json
{
  "account": "etrade/main",
  "product": "AAPL",
  "grid": {
    "begin-price": 150, "end-price": 200,
    "buy-interval-pct": 2, "profit-margin-pct": 3,
    "size": 50, "cancel-offset-pct": 5
  },
  "grid-pct": 5, "far-pct": 12, "hysteresis-pct": 2, "dwell-time": "48h",
  "contract-selector": "",
  "wheel": {
    "target-delta": 0.3, "min-dte": 21, "max-dte": 45,
    "min-premium-yield": 0.5, "min-open-interest": 100, "max-spread-pct": 10,
    "reprice-step": 0.25, "reprice-interval": "5m"
  }
}
```

A ladder spec has `account`, `product` and `bands`, a list of the same
objects without `account` and `product` (a band that names them must match
the ladder's, as `greelladder.New` already requires).

`--account` and `--product` flags override the spec's, so one band file can
be reused across symbols.

### 4. The grid: explicit levels or a range

`grid` is either:

- `"levels": [{"buy": 150, "sell": 155, "size": 50, "buy-cancel": 160,
  "sell-cancel": 145}, ...]`, used as written; or
- a range, as above: `begin-price`, `end-price`, one of `buy-interval` /
  `buy-interval-pct`, one of `profit-margin` / `profit-margin-pct`, `size`,
  and `cancel-offset-pct` (default 5).

The range form reuses waller's generator (`subcmds/waller/spec.go`,
`fixedProfitPairs` and `percentProfitPairs`), so a range means exactly what
it means to `waller add`. Waller's fee adjustment is left out: greel's
grid sells at the price it is given. Every point needs a cancel price
(`point.Point.Check`), which is why the explicit form asks for one and the
range form derives it.

### 5. No hidden defaults

Every zone percentage and every wheel knob must be in the spec, except
where zero is already meaningful: `hysteresis-pct` and `dwell-time` may be
zero, and an empty `contract-selector` means the default selector. A
greeler that sells options with a default someone didn't choose is the
wrong failure.

The constructors don't check the wheel knobs today, so `greeler.New` gains
that check, where every path (subcommand, a future one, a test) gets it:
`wheel` present, `min-dte` ≥ 0 and ≤ `max-dte`, `reprice-step` in (0, 1],
`reprice-interval` > 0, and the rest not negative.

### 6. `--dry-run`

`--dry-run` builds the configs, runs `greeler.New` or `greelladder.New`
locally (no server, no exchange), and prints the resolved config, one line
per grid level, so the generated grid can be checked before anything
trades. It catches every config error the server would, except the account
and product checks of scenario 1.

### 7. Names

`--name` is required, as for `waller add`, and set with `SetJobName` after
the create. A ladder's greelers are already named by the ladder
(`<ladder-uid>/greeler-000000`, ...) and aren't separate jobs, so they get
no job names.

### 8. What this doesn't add

No `list`, `get` or `print` subcommands: `tradebot job list` and `tradebot
status` already show greel jobs, and greel summaries are in `tradebot
summary` (accounting-story). They can follow once the jobs are in use.

---

## Code

- `api/greel.go`: `GreelRequest`, `GreelLadderRequest`, responses, paths,
  `Check`.
- `server/server.go`: `doGreel`, `doGreelLadder`, registered in the handler
  map; an `OptionsAccount` check helper.
- `greeler/greeler.go`: wheel knob validation in `check`.
- `subcmds/greel/spec.go`: the spec schema, parsing (strict), conversion to
  `gobs.GreelConfig`, grid generation through waller's generator (exported
  from `subcmds/waller` or moved to a shared package).
- `subcmds/greel/greeler_add.go`, `subcmds/greel/ladder_add.go`: the two
  `add` commands, `--dry-run`, `--name`, `--option`, the ladder's gate flags.
- `main.go`: `greeler` and `greelladder` command groups.

Planned as three PRs: API and server; spec and grid; the subcommands.

---

## Decisions made at this checkpoint

1. **Two endpoints, `/trader/greel` and `/trader/greelladder`,** taking
   gobs configs; the server validates through the existing constructors
   and adds only the account and product checks (scenario 1).
2. **The account must implement `exchange.OptionsAccount`** or the create
   is rejected (scenario 1). A ladder on an account without
   `StockHoldings` is accepted: stock reconciliation is alert-only and
   already skips when holdings are missing.
3. **Options are applied at creation,** and the ladder subcommand has
   `--max-contracts` and `--max-put-exposure`, so a ladder never runs
   before its limits are set (scenario 2).
4. **Configs come from a strict JSON spec file** with a hand-writable
   schema; `--account`/`--product` override (scenario 3).
5. **The grid is explicit levels or a range,** the range through waller's
   generator without the fee adjustment (scenario 4).
6. **No hidden defaults;** wheel knob validation moves into
   `greeler.New` (scenario 5).
7. **`--dry-run` validates locally and prints the grid** (scenario 6).
8. **`--name` is required;** ladder greelers get no job names (scenario 7).
9. **Only `add` for now** (scenario 8).
