# Story: `etrade place-option-order` — placing one option order by hand

Companion to [etrade-options-story.md](etrade-options-story.md). The
E*TRADE options code can place option orders, but only from inside a
running greeler: `OptionProduct.place` (`etrade/option_product.go`) calls
`Client.PlaceOptionLimitOrder` (`etrade/client_option.go`). The command line
has `etrade option-chain` and `etrade option-quote` to look options up, and
`get-order`, `list-orders` and `cancel-order` work on any order, but
`etrade place-order` only places equity orders. There is no way to send one
option order by hand, for example to check the option order flow against
the sandbox or a real account before letting a greeler trade.

This story adds that command.

---

## Scenario walkthrough

### 1. A new command, not more flags on `place-order`

`tradebot etrade place-option-order` is a separate command in its own file
(`subcmds/etrade/place_option_order.go`), next to `place-order`.

Folding options into `place-order` would mean flags that only apply to one
kind of order (`--contract` and `--action` for options; `--symbol`,
`--side`, `--order-term` and `--market-session` for equities), and checks
that differ by kind. Two small commands are easier to read and to use than
one with a mode switch, and `place-order` stays exactly as it is.

### 2. Flags

```
tradebot etrade place-option-order \
    --contract AAPL261218P00200000 \
    --action SELL_OPEN \
    --qty 1 \
    --limit-price 1.25 \
    [--account-id ID] [--client-order-id N] [--preview] [--secrets-file PATH]
```

- `--contract` (required) is the compact OSI contract ID, the same form
  `option-quote` takes and greel uses everywhere
  (etrade-options-story.md, scenario 1). It is parsed before any request,
  so a typo fails at once.
- `--action` (required) is one of `BUY_OPEN`, `SELL_OPEN`, `BUY_CLOSE`,
  `SELL_CLOSE`, case-insensitive. These are E*TRADE's option order actions
  and the constants `etrade.OptionBuyOpen` and friends already name them.
- `--qty` (required) is a positive whole number of contracts.
- `--limit-price` (required) is the per-share limit price, as E*TRADE
  quotes it (1.25 means $125 per contract).
- `--account-id` is picked automatically when the secrets file has one
  account, as in `place-order`.
- `--client-order-id` defaults to the current Unix milliseconds, as in
  `place-order`.
- `--secrets-file` defaults to the data directory's `secrets.json`, and the
  secrets file's `sandbox` setting picks the sandbox or the live API, as
  for every `etrade` command.

There are no `--order-term` or `--market-session` flags. Every option order
is a regular-session, good-for-the-day limit order, because that is what
`PlaceOptionLimitOrder` sends and what optlimiter places. The command tests
that path; it does not add new order shapes.

### 3. `--preview`: check without placing

E*TRADE places every order in two steps: a preview, then a place that
quotes the preview ID (`Client.previewAndPlace`). The preview alone places
nothing, and E*TRADE answers it with the order's estimated value and
commission and any warnings (for example, not enough buying power, or the
account isn't approved for that option level).

With `--preview` the command runs only the first step and prints what
E*TRADE returned. This is the safe first thing to run on a live account:
it shows that the contract, action and quantity are accepted, and what the
order would cost, without risking a fill.

The exact names of the value, commission and message fields are taken from
E*TRADE's preview response documentation and checked against the sandbox in
the code PR.

### 4. Output

Without `--preview`, after the order is placed the command fetches it with
`GetOrder` and prints it as indented JSON, as `place-order` does. If that
fetch fails it prints `{"orderId": N}` so the ID is never lost. The order
can then be followed with `get-order` and cancelled with `cancel-order`.

With `--preview`, it prints the preview as indented JSON: the preview ID,
the estimated total value and commission, and the messages.

### 5. What it does not do

The command talks to the E*TRADE client directly, like `place-order`. It
does not go through `OptionProduct`, so it writes nothing to the tradebot
database and no greeler will ever adopt or track its order. That is what a
hand test wants, but it means: do not run it on a contract that a running
greeler is trading, because the greeler's position would not include the
manual order.

It also asks for no confirmation, again like `place-order`. `--preview`
is the dry run.

---

## Code

- `etrade/client_option.go`: two exported methods that take the contract
  ID string, so the command does not need the unexported `contract` type:
  - `PlaceOptionOrder(ctx, accountID, contractID, action string, qty,
    limitPrice decimal.Decimal, clientOrderID string) (int64, error)`
    parses the ID and calls the existing `PlaceOptionLimitOrder`.
  - `PreviewOptionOrder(...)` with the same arguments returns an
    `OptionOrderPreview` (preview ID, estimated value and commission,
    messages). It shares the order detail with `PlaceOptionLimitOrder`
    through one small helper, so the two cannot drift.
- `etrade/client.go`: `previewAndPlace` is split into `previewOrder` and
  the place step, with no change in behaviour; `previewOrderResponse`
  gains the value, commission and message fields.
- `etrade/client_option_test.go`: tests for both new methods against the
  existing fake server, including a bad contract ID failing before any
  request.
- `subcmds/etrade/place_option_order.go`: the command.
- `main.go`: registered in the `etrade` group after `place-order`.

Planned as two PRs: the client methods with their tests, then the command
and its registration.

---

## Decisions made at this checkpoint

1. **A separate `place-option-order` command,** leaving `place-order`
   unchanged (scenario 1).
2. **The contract is named by its compact OSI ID,** the same form as
   `option-quote` and greel (scenario 2).
3. **No order term or session flags:** the command sends the same
   regular-session, good-for-the-day limit order that optlimiter does
   (scenario 2).
4. **`--preview` runs only E*TRADE's preview step** and prints its
   estimate, as the dry run (scenario 3).
5. **The command bypasses `OptionProduct`** and keeps nothing in the
   database (scenario 5).
