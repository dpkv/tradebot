# Story: option orders from `etrade place-order`

Companion to [etrade-options-story.md](etrade-options-story.md). The
E*TRADE options code can place option orders, but only from inside a
running greeler: `OptionProduct.place` (`etrade/option_product.go`) calls
`Client.PlaceOptionLimitOrder` (`etrade/client_option.go`). The command line
has `etrade option-chain` and `etrade option-quote` to look options up, and
`get-order`, `list-orders` and `cancel-order` work on any order, but
`etrade place-order` only places equity orders. There is no way to send one
option order by hand, for example to check the option order flow against
the sandbox or a real account before letting a greeler trade.

This story teaches `place-order` to place option orders too.

---

## Scenario walkthrough

### 1. One command, two kinds of order

`tradebot etrade place-order` keeps every flag it has today and gains one,
`--contract`. With `--symbol` it places an equity order exactly as now.
With `--contract` it places an option order. Giving both, or neither, is an
error.

```
# equity, unchanged
tradebot etrade place-order --symbol AAPL --side BUY --qty 10 --limit-price 180

# option
tradebot etrade place-order --contract AAPL261218P00200000 \
    --side SELL_OPEN --qty 1 --limit-price 1.25
```

Equity orders behave exactly as before; nothing an existing invocation
does changes.

### 2. Flags for an option order

- `--contract` is the compact OSI contract ID, the same form
  `option-quote` takes and greel uses everywhere
  (etrade-options-story.md, scenario 1). It is parsed before any request,
  so a typo fails at once.
- `--side` takes the option order actions instead of BUY and SELL:
  `BUY_OPEN`, `SELL_OPEN`, `BUY_CLOSE` or `SELL_CLOSE`, case-insensitive.
  These are E*TRADE's own action names (the constants `etrade.OptionBuyOpen`
  and friends). A plain `BUY` or `SELL` with `--contract` is an error, since
  open versus close can't be guessed. An option action with `--symbol` is
  an error too.
- `--qty` must be a positive whole number of contracts.
- `--limit-price` is the per-share price, as E*TRADE quotes it (1.25 means
  $125 per contract).
- `--account-id`, `--client-order-id` and `--secrets-file` work as they do
  for equities.
- `--order-term` and `--market-session` can't be used with `--contract`.
  Every option order is a regular-session, good-for-the-day limit order,
  because that is what `PlaceOptionLimitOrder` sends and what optlimiter
  places. Setting either flag on an option order is an error rather than
  being silently ignored; the check uses `flag.FlagSet.Visit`, so the
  equity defaults (`GOOD_UNTIL_CANCEL`, `REGULAR`) don't trip it.

### 3. `--preview`: check without placing

E*TRADE places every order in two steps: a preview, then a place that
quotes the preview ID (`Client.previewAndPlace`). The preview alone places
nothing, and E*TRADE answers it with the order's estimated value and
commission and any warnings (for example, not enough buying power, or the
account isn't approved for that option level).

A new `--preview` flag runs only the first step and prints what E*TRADE
returned. It works for both kinds of order. It is the safe first thing to
run on a live account: it shows that the contract, side and quantity are
accepted, and what the order would cost, without risking a fill.

The exact names of the value, commission and message fields are taken from
E*TRADE's preview response documentation and checked against the sandbox in
the code PR.

### 4. Output

Unchanged for a placed order of either kind: the command fetches it with
`GetOrder` and prints it as indented JSON, or prints `{"orderId": N}` if
that fetch fails, so the ID is never lost. The order can then be followed
with `get-order` and cancelled with `cancel-order`.

With `--preview` it prints the preview as indented JSON: the preview ID,
the estimated total value and commission, and the messages.

### 5. What it does not do

The command talks to the E*TRADE client directly, for options as for
equities. It does not go through `OptionProduct`, so it writes nothing to
the tradebot database and no greeler will ever adopt or track its order.
That is what a hand test wants, but it means: do not place a manual order on
a contract that a running greeler is trading, because the greeler's
position would not include it.

It asks for no confirmation, as today. `--preview` is the dry run.

---

## Code

- `etrade/client.go`: `previewAndPlace` is split into `previewOrder` and
  the place step, with no change in behaviour, and `previewOrderResponse`
  gains the value, commission and message fields. The equity order detail
  moves into a small helper shared by `PlaceLimitOrder` and a new
  `PreviewLimitOrder`.
- `etrade/client_option.go`: two exported methods that take the contract
  ID string, so the command does not need the unexported `contract` type:
  - `PlaceOptionOrder(ctx, accountID, contractID, action string, qty,
    limitPrice decimal.Decimal, clientOrderID string) (int64, error)`
    parses the ID and calls the existing `PlaceOptionLimitOrder`.
  - `PreviewOptionOrder(...)` with the same arguments.

  Both previews return one `OrderPreview` (preview ID, estimated value and
  commission, messages). The option order detail moves into a helper
  shared with `PlaceOptionLimitOrder`, so placing and previewing can't
  drift.
- `etrade/client_test.go`, `etrade/client_option_test.go`: tests for the
  new methods against the existing fake server, including a bad contract ID
  failing before any request.
- `subcmds/etrade/place_order.go`: the `--contract` and `--preview` flags,
  the checks in scenario 2, and the switch between the two kinds of order.

Planned as two PRs: the client methods with their tests, then the command.

---

## Decisions made at this checkpoint

1. **Option orders go through `place-order`,** selected by `--contract`
   in place of `--symbol`; equity orders are unchanged (scenario 1).
2. **The contract is named by its compact OSI ID,** the same form as
   `option-quote` and greel (scenario 2).
3. **`--side` carries the option action** (`BUY_OPEN` and so on) for an
   option order; plain `BUY` and `SELL` are rejected there (scenario 2).
4. **`--order-term` and `--market-session` are rejected on option
   orders,** which are always regular-session, good-for-the-day limit
   orders, as optlimiter places them (scenario 2).
5. **`--preview` runs only E*TRADE's preview step,** for either kind of
   order, and prints its estimate (scenario 3).
6. **The command bypasses `OptionProduct`** and keeps nothing in the
   database (scenario 5).
