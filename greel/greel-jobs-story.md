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
   handler adds no rules of its own. Those checks are factored out so the
   client runs the very same code (below).
5. Apply `Options` with `SetOption`, then save and `runner.Add` in one
   transaction, then `runner.Resume`.

The request carries the gobs configs directly, and the ladder request
keeps its list of bands: the client decides the bands (scenario 3) and the
server validates them.

**Shared validation.** Today the config rules live inside the
constructors' private `check` methods, mixed with the UID check, so the
client could only reach them by building a job. They move into exported
functions that need no UID and no exchange:

- `greeler.CheckConfig(cfg *gobs.GreelConfig) error`: product and account
  set, the grid rules, the zone rules, the wheel knobs (scenario 5) and a
  registered selector. `greeler.New` and `Load` call it, so nothing
  changes for them.
- `greelladder.CheckBands(accountName, productID string, bands
  []*gobs.GreelConfig) error`: the ladder's own rules (account and product
  set, at least one band, each band's product and account empty or
  matching), then `greeler.CheckConfig` on each band as `New` would fill
  it. `greelladder.New` calls it.

The server reaches them through the constructors; `add` calls them before
it posts, and `--dry-run` and `query` call them instead of posting, so a
config error reads the same everywhere and the rules have one copy.

### 2. Options at creation: the risk gates need them

The risk gates are ladder options (`max-contracts`, `max-put-exposure`),
and options change only while a job isn't running
(`server/jobs.go` `doJobSetOption`). An unset limit means no limit
(`greelladder/gates.go`), and the create resumes the job at once. Without
options at creation, a new ladder runs ungated from the create until
someone pauses it to set the limits; if the spot is already far from a
band and the dwell time is short, a band can flip and write a contract in
that gap. Nothing runs while the ladder is paused.
risk-gates-story scenario 5 left this open because no creation subcommand
existed.

So both requests take `Options`, applied before the first save. The ladder
subcommand has `--max-contracts` and `--max-put-exposure`; both subcommands
take repeated `--option key=value` for anything else (`freeze`, for a job
that should start frozen). An unknown key or bad value fails the request,
since `SetOption` rejects it.

### 3. Configs come from flags

Both `add` commands take the whole config as flags, like `waller add`, and
each command's help ends with a sample command line:

```sh
tradebot greeler add --name aapl-greel \
  --account etrade/main --product AAPL \
  --begin-price 150 --end-price 200 --buy-interval-pct 2 \
  --profit-margin-pct 3 --buy-size 50 \
  --grid-pct 5 --far-pct 12 --hysteresis-pct 2 --dwell-time 48h \
  --target-delta 0.3 --min-dte 21 --max-dte 45 \
  --min-premium-yield 0.5 --min-open-interest 100 --max-spread-pct 10 \
  --reprice-step 0.25 --reprice-interval 5m

tradebot greelladder add --name aapl-ladder \
  --max-contracts 4 --max-put-exposure 80000 \
  ...the same grid, zone and wheel flags...
```

The flags fall into three sets, defined once in `subcmds/greel` and shared
by every greel command that takes a config (`add` and `query`):

- **Grid:** waller's range flags (scenario 4).
- **Zones:** `--grid-pct`, `--far-pct`, `--hysteresis-pct`, `--dwell-time`
  (a Go duration), `--contract-selector`.
- **Wheel:** `--target-delta`, `--min-dte`, `--max-dte`,
  `--min-premium-yield`, `--min-open-interest`, `--max-spread-pct`,
  `--reprice-step`, `--reprice-interval`.

`--account` and `--product` are required; there is no default account,
since only an options account works.

**A ladder's greels.** `greelladder add` builds the grid over the whole
range exactly as `greeler add` would, then `greelladder.Cut` cuts its
levels, from the bottom, into consecutive greels of one contract each: every greel gets
`ceil(100 / buy-size)` levels, the fewest that hold 100 shares. Levels
left over at the top, too few for a contract, join the top greel. A
range too small for even one greel is an error. So the range decides how
many greels there are, there is no count flag, and a ladder over a range
trades the same levels as one greeler over it. Every greel gets the same
zone and wheel settings; greels with different settings aren't
expressible with flags and are left out. `--dry-run` prints each greel's
levels.

### 4. The grid is a range, as in waller

The grid flags are waller's: `--begin-price`, `--end-price`, one of
`--buy-interval` / `--buy-interval-pct`, one of `--profit-margin` /
`--profit-margin-pct`, `--buy-size`, `--sell-size` (must equal buy size)
`--cancel-offset-pct` (default 5, as in waller) and `--fee-pct` (default
0.25, as in waller). They go through waller's generator
(`subcmds/waller/spec.go`, `fixedProfitPairs` and `percentProfitPairs`),
fee adjustment included, so a range means exactly what it means to
`waller add`: each sell price is raised so the margin survives the fee on
both legs. `--fee-pct 0` turns the adjustment off. There is no form for
explicit levels.

### 5. No hidden defaults

Every zone percentage and every wheel knob must be given as a flag, except
where zero is already meaningful: `hysteresis-pct` and `dwell-time` may be
zero, and an empty `contract-selector` means the default selector. A
greeler that sells options with a default someone didn't choose is the
wrong failure.

The constructors don't check the wheel knobs today, so
`greeler.CheckConfig` gains that check, where every path (server,
subcommand, dry run, a test) gets it:
`wheel` present, `min-dte` ≥ 0 and ≤ `max-dte`, `reprice-step` in (0, 1],
`reprice-interval` > 0, and the rest not negative.

### 6. `--dry-run`

`--dry-run` builds the configs, runs `greeler.CheckConfig` or
`greelladder.CheckBands` locally (no server, no exchange), and prints the resolved config, one line
per grid level, so the generated grid can be checked before anything
trades. It catches every config error the server would, except the account
and product checks of scenario 1.

### 7. Names

`--name` is required, as for `waller add`, and set with `SetJobName` after
the create. A ladder's greelers are already named by the ladder
(`<ladder-uid>/greeler-000000`, ...) and aren't separate jobs, so they get
no job names.

### 8. Parity with waller

`waller` has `add`, `list`, `get`, `print`, `query`, `upgrade`, `simulate`
and `summary`. The `greeler` and `greelladder` groups get all of them but
`simulate`, each working like waller's (`get`, `print`, `list`, `upgrade`
and `summary` read the database directly through `cmdutil.DBFlags`;
`query` needs no server):

- `list`: job keys under `/greelers/` or `/greelladders/`. `greeler list`
  leaves out a ladder's greelers (`<ladder-uid>/greeler-NNNNNN`), as
  `waller list` leaves out its loops; `--all` includes them.
- `get <name-or-uid>`: a human-readable state, like `waller get`: the
  `Status` summary (grid round trips, premiums, assignments), the current
  mode and epoch, the pending flip and its dwell clock, the held contract,
  and one row per grid level. `greelladder get` prints the ladder's gate
  limits and usage, then one block per greel.
- `print <name-or-uid>`: the gob as JSON.
- `query`: takes the config flags and prints what a job would need without
  creating it: the grid levels, the stock budget (waller's `Analyze`
  at `--fee-pct`), the price bounds of the grid and far
  zones, the contracts the grid can cover, and the worst-case put exposure
  (contracts × 100 × the lowest buy price). The ladder version prints this
  per greel and in total.
- `upgrade <uid>...`: load and save, to rewrite old state in the current
  form.
- `summary`: waller summary's flags and formats (`table`, `json`,
  `template`), over greelers or ladders, using `greeler.Summary` and
  `greelladder.Summary`, with the premium columns added.

No `simulate`: a prices file has no option prices, so it could replay
only the grid half of a greel job.

---

## Code

- `api/greel.go`: `GreelRequest`, `GreelLadderRequest`, responses, paths,
  `Check`.
- `server/server.go`: `doGreel`, `doGreelLadder`, registered in the handler
  map; an `OptionsAccount` check helper.
- `greeler/greeler.go`: `CheckConfig`, factored out of `check`, with the
  wheel knob rules added.
- `greelladder/greelladder.go`: `CheckBands`, factored out of `New`.
- `greelladder/cut.go`: `Cut(cfg) ([]*gobs.GreelConfig, error)`, the
  one-contract cutting rule, used by the client's `add`, `--dry-run` and
  `query`.
- `subcmds/waller/spec.go`: export the pair generator.
- `subcmds/greel/flags.go`: the grid, zone and wheel flag sets, their
  checks, and conversion to `gobs.GreelConfig`.
- `subcmds/greel/greeler_*.go`, `subcmds/greel/ladder_*.go`: the seven
  commands for each group.
- `main.go`: `greeler` and `greelladder` command groups.

Planned as seven PRs: `CheckConfig`, `CheckBands` and `Cut`; API and
server; the shared flags; `add` for both; `list`, `get`, `print`, `upgrade`; `query`;
`summary`.

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
4. **Configs come from flags,** with a sample command line in each help
   text; `--account` and `--product` are required. A ladder is one range
   cut into greels of one contract each, with the same settings
   (scenario 3).
5. **The grid is a range, as in waller,** through waller's generator
   with its fee adjustment and `--fee-pct` (scenario 4).
6. **No hidden defaults;** the config checks move into exported
   `greeler.CheckConfig` and `greelladder.CheckBands`, used by the
   constructors, `add`, `--dry-run` and `query`, and gain the wheel knob
   rules (scenarios 1 and 5).
7. **`--dry-run` validates locally with those checks and prints the
   grid** (scenario 6).
8. **`--name` is required;** ladder greelers get no job names (scenario 7).
9. **Parity with waller, except `simulate`:** `add`, `list`, `get`,
   `print`, `query`, `upgrade` and `summary` for both groups (scenario 8).
