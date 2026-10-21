# Story: etrade — OptionsExchange and StockHoldings

Companion to [project.md](project.md) (the last unbuilt piece: no exchange
implements `exchange.OptionsExchange` yet),
[optlimiter-story.md](optlimiter-story.md) (section 7, what option orders
need from the exchange) and [optpos-story.md](optpos-story.md) (scenario 6,
settlement). Until this lands a greeler on etrade refuses to run
(`greeler/run.go:90`) and a ladder's stock reconciliation is off
(`greelladder/run.go:60`).

The work extends the existing `etrade` package. Nothing in `exchange`,
`gobs`, `optlimiter`, `optpos`, `greeler` or `greelladder` changes:
`*etrade.Exchange` gains the five `OptionsExchange` methods and
`GetStockHolding`, and the type assertions those packages already make
start succeeding.

---

## Scenario walkthrough

### 1. Contract identity

`gobs.OptionContract.ContractID` is ours, not E*TRADE's. It keeps the
format the gobs comment already shows, an OSI key with readable separators:

```
AAPL_20261218_P00200000     underlying _ YYYYMMDD _ C|P strike×1000 (8 digits)
```

E*TRADE never sees it. Every E*TRADE request names an option by its
product fields (`symbol`, `callPut`, `expiryYear/Month/Day`,
`strikePrice`), and quotes take the colon form
`AAPL:2026:12:18:PUT:200`. One small file (`etrade/contract.go`) parses
and formats all three, and everything else goes through it. A contract ID
that doesn't parse is `os.ErrInvalid` before any request is made.

`Expiry` is 16:00 America/New_York on the expiry date, the close of the
last session. optpos starts settling at `Expiry` (`optpos/optpos.go:251`)
and the selector counts DTE from it, so midnight would make a contract
look expired for its whole last trading day.

`ContractSize` is 100. Only standard contracts are listed (scenario 2), so
it never varies.

### 2. The chain: `GetOptionsChain`, `GetOptionsProduct`

E*TRADE serves one expiry per chain request, so the chain takes:

1. `GET /v1/market/optionexpiredate?symbol=X&expiryType=ALL` for the
   expiry dates;
2. one `GET /v1/market/optionchains` per date, with `chainType=CALLPUT`,
   `optionCategory=STANDARD`, `skipAdjusted=true`, `includeWeekly=true`
   and no strike window, so every strike comes back.

Adjusted contracts (after a split or special dividend, deliverable not 100
shares) are dropped twice: by `skipAdjusted` and again by each pair's
`adjustedFlag`. greel's sizing assumes 100 shares per contract, and an
adjusted deliverable would break the all-or-nothing qualification.

Each entry fills `Bid`, `Ask`, `Price` (the mid, or last trade when a side
is zero, as equity quotes do), `Volume`, `OpenInterest` and
`ImpliedVolatility` from the pair's quote and greeks.

A liquid ETF has 40 or more expiries, so one chain is 40+ requests. Every
greeler on an underlying re-selects at the same session open, so the
exchange keeps each chain for 60 seconds and lets concurrent callers share
one fetch (one in-flight fetch per underlying). That turns M greelers'
session start into one fetch. Rate limiting already retries on 429
(`etrade/client.go`, `handleHTTPError`).

`GetOptionsProduct` is the live quote optlimiter re-prices from, so it is
never cached: `GET /v1/market/quote/AAPL:2026:12:18:PUT:200?detailFlag=OPTIONS`,
which also carries the greeks `GetGreeks` returns.

### 3. Trading a contract: `OpenOptionsProduct`

`OptionProduct` (`etrade/option_product.go`) implements
`exchange.OptionsProduct` beside the equity `Product`, sharing the client.
The exchange caches open option products by contract ID, as it caches
equity products by symbol.

- **Orders** are `orderType=OPTN`, `priceType=LIMIT`,
  `orderTerm=GOOD_FOR_DAY`, `marketSession=REGULAR`, quantity in
  contracts, the limit price per share, through the same preview-then-place
  flow equity orders use. The four verbs map to `orderAction`
  `BUY_OPEN`, `SELL_OPEN`, `BUY_CLOSE`, `SELL_CLOSE`. v1 only calls
  `LimitSellToOpen`; the other three are implemented because the interface
  has them, and are one line each.
- **No extended-hours retry loop.** The equity path sleeps until 7am and
  re-sends when extended hours are closed. Option orders are regular
  session only, and optlimiter already waits for the session
  (`optlimiter/price.go`), so a rejection is returned as an error.
- **No price rounding.** optlimiter rounds to the option tick itself; the
  product sends what it is given.
- **No background cancel of failed placements.** The equity product
  cancels an order whose placement errored but turns up later
  (`goCancelFailedCreates`). For options that is optlimiter's job: it
  finds the order by client ID and cancels it (optlimiter-story section
  4). Two actors cancelling would race.

### 4. Routing order and price updates

Today the client publishes every polled order to a topic keyed by
`order.Symbol` (`goRefreshOrders`). E*TRADE reports an option order's
symbol as its underlying, so an option order on AAPL would land on the
AAPL equity topic, and an option quote's symbol would overwrite the AAPL
stock price. Both are fixed by keying on the instrument instead of the
symbol: equity orders and quotes keep the symbol key; option orders and
quotes use the contract ID, built from their product fields. The option
price poll is a second `GetQuotes` call with `detailFlag=OPTIONS`, made
only while some option product has a price subscriber.

The equity recovery matcher (`matchOpenOrder`) also gets an explicit
`securityType == EQ` check. Today an option order can't match only
because its side reads `SELL_OPEN` rather than `SELL`.

### 5. Client IDs: `GetOptionsOrderByClientID`

The broker can't help here: E*TRADE's `clientOrderId` takes digits only,
and the notes record that list and single-order responses omit it
(`etrade/NOTES`). So the mapping lives in our KV store, under
`/etrade/options/clientorder/<uuid>`:

```go
type optionOrderEntry struct {
	ContractID    string
	ServerOrderID int64 // zero until the place call returns
	Placement     orderPlacementInfo
	Done          bool // last seen terminal; skips re-tracking on open
}
```

`LimitSellToOpen` writes the entry **before** the preview call and adds the
server ID after the place call returns, the same two-phase write the
equity product uses. Unlike equity entries, these are **not deleted** when
the order finishes, because optlimiter looks up filled and cancelled orders
too. A position writes a handful of orders per session, so the records
stay small; they are kept for 90 days after the contract's expiry and
pruned when the exchange starts.

The lookup:

1. **No entry** → `os.ErrNotExist`. The entry is written before anything
   reaches the broker, so no entry means the order was never sent.
2. **Server ID known** → `GetOrder` by ID, with the client ID restored.
3. **Server ID zero** (a crash or an error between place and the second
   write) → list the account's option orders for the underlying in every
   status from the placement day (`GET .../orders?symbol=AAPL&securityType=OPTN&fromDate=`),
   paging with `marker`, and match contract, action, quantity, limit price
   and placed time within 60 seconds, skipping order IDs another entry
   already owns.
   - One match → save its server ID on the entry and return it.
   - No match → `os.ErrNotExist`. optlimiter keeps asking until
     `absentSettle` has passed, which covers a broker slow to list an
     order (optlimiter-story section 4).
   - Two or more → an error, never a guess. optlimiter never has two live
     orders and each re-price is at least a tick lower, so this should not
     happen; if it does, a person should look.

The same UUID handed to `LimitSellToOpen` twice returns the existing order
if its server ID is known, and an error if it isn't, so a caller bug can't
place twice.

### 6. Settlement: `GetOptionsSettlement`

Only broker records may say "assigned" or "expired". On E*TRADE those are
transactions: `GET /v1/accounts/{key}/transactions`. When a short option is
assigned or expires, the account gets a transaction whose brokerage product
is that option.

`GetOptionsSettlement(contractID)` pages through transactions from
`now − SettlementLookback` (default 45 days) to now and keeps those whose
product fields equal the contract's:

- a type containing "assign" (case-insensitive) → `assigned`;
- a type containing "expir" → `expired`;
- anything else (the opening sale, a buy-to-close) → ignored.

`Key` is the `transactionId`, `At` the transaction date, `Contracts` the
absolute quantity, `Fee` the transaction's fee. If both kinds appear (a
multi-contract position partly assigned and the rest expired), the answer
is `assigned` with the assigned count, since that is what moves shares.
Nothing found → `open`.

Three things to know:

- **The exact type strings aren't documented.** Matching on substrings
  survives the likely spellings ("Option Assignment", "Assigned", "Option
  Expiration", "Expired"). A transaction on the contract with a type the
  matcher doesn't know is logged at warning with the raw type, so the
  first real expiry shows it if the guess is wrong. Until then the
  position stays settling, which is the safe failure: optpos records
  nothing, and the greeler stays in wheel mode until someone looks.
- **The lookback bounds the query.** optpos asks hourly while the greeler
  runs, so an assignment is seen within an hour; the 45 days only need to
  cover the bot being down. A longer outage leaves the position settling
  (safe, as above) until the option is raised.
- **It is account-wide.** Transactions don't say which order opened the
  position. A manual trade in the same contract on the same account is
  indistinguishable, the same limitation the risk gates already document
  (risk-gates-story scenario 8).

### 7. Stock holdings: `GetStockHolding`

`GET /v1/accounts/{key}/portfolio`, all pages, summing `quantity` over
positions whose product is `EQ` and whose symbol is the product ID. The
portfolio is on a trade-date basis, so unsettled buys and sells count,
which is what the ladder's reconciliation wants (`greelladder/run.go:19`).
Short option positions are not stock and are skipped. No position → zero,
not an error.

`etrade` doesn't import `greelladder`; a test asserts that
`*etrade.Exchange` satisfies `greelladder.StockHoldings`.

### 8. Testing

There is no CI, and E*TRADE's sandbox returns canned responses that don't
depend on the request, so it can't exercise this logic.

- **Unit tests against an `httptest` server** with JSON fixtures shaped
  like E*TRADE's documented responses: chain assembly and the adjusted
  filter, contract ID round trips, order and quote routing, each branch of
  the client ID lookup, settlement classification, holdings paging. The
  client gets an unexported base URL override so tests can point it at
  the fake server.
- **Read-only debug subcommands** beside the existing `tradebot etrade ...`
  ones: `option-chain`, `option-quote` and `list-transactions`, so the real
  responses (and the transaction type strings in scenario 6) can be
  checked against a live account before the first greel runs on it.

---

## Code

- `etrade/contract.go`: contract ID parse/format, E*TRADE product fields
  and quote symbol.
- `etrade/internal/option.go`: chain, expiry date, option quote,
  portfolio and transaction response types.
- `etrade/client.go`: `GetOptionExpiryDates`, `GetOptionChain`,
  option quotes, a `PlaceLimitOrder` that takes a product (equity callers
  unchanged), `ListOrders` with filters and paging, `GetPortfolio`,
  `ListTransactions`; routing by instrument in `goRefreshOrders` and
  `goPollPrices`.
- `etrade/option_exchange.go`: the five `OptionsExchange` methods,
  `GetStockHolding`, the chain cache, entry pruning.
- `etrade/option_product.go`: `OptionProduct`.
- `etrade/options.go`: `SettlementLookback` (default 45 days) and
  `ChainCacheTTL` (default 60 seconds).
- `subcmds/etrade/`: the three read-only subcommands.

---

## Decisions to make at this checkpoint

1. **ContractID is our OSI-style key**, `AAPL_20261218_P00200000`; E*TRADE
   only ever sees product fields (scenario 1).
2. **Expiry is 16:00 New York time on the expiry date** (scenario 1).
3. **Only standard, unadjusted contracts are listed**, so `ContractSize`
   is always 100 (scenario 2).
4. **The chain covers every expiry and strike, cached 60 seconds per
   underlying** with one shared fetch (scenario 2). `GetOptionsProduct` is
   never cached.
5. **Option orders are regular-session day limit orders**, unrounded,
   with no extended-hours retry and no background cancel of failed
   placements (scenario 3).
6. **Order and price updates are routed by instrument**: symbol for
   equity, contract ID for options (scenario 4).
7. **Client ID mapping is ours, written before placement and kept 90 days
   past expiry**; a zero server ID is resolved by matching the order list
   in all statuses, and an ambiguous match is an error (scenario 5).
8. **Settlement comes from account transactions** matched on the
   contract's product fields and on "assign"/"expir" in the type, over a
   45-day lookback; anything unrecognized leaves the position settling
   (scenario 6).
9. **Stock holdings are the portfolio's trade-date equity quantity**
   (scenario 7).
10. **Tests use an httptest fake; real responses are checked with three
    read-only debug subcommands** (scenario 8).

### Open question

Decision 8 rests on transaction type strings E*TRADE doesn't document. If
you have an assigned or expired short option in your account history, the
`list-transactions` subcommand in the code PR will show the real strings;
the matcher is tightened then if they differ.
