# Mock Exchange — Build Plan

## Tasks

- [x] `datafeed/datafeed.go` — DataFeed interface, Tick struct, CandleExpander func type + built-ins (OpenLowHighClose, OpenHighLowClose)
- [x] `datafeed/coinbasedb.go` — Candle feed sourced from Coinbase BadgerDB (wraps `coinbase.Datastore.ScanCandles`)
- [x] `datafeed/csv.go` — Candle feed sourced from a CSV file (columns: time, open, low, high, close)
- [x] `mockexchange/exchange.go` — MockExchange: single source of truth for balance/available across all products, reserve/release/settle, implements `exchange.Exchange`
- [x] `mockexchange/product.go` — MockProduct: open order list, fill simulation via `ProcessTick`, delegates all fund operations to Exchange, implements `exchange.Product`
- [x] `mockexchange/engine.go` — Engine: pulls ticks from a DataFeed, calls `product.ProcessTick(tick)` in a loop; pluggable via DataFeed interface
- [x] `subcmds/backtest/backtest.go` — BacktestFlags (shared flags: product, exchange, begin/end, feed, balances, fee-rate) + `runBacktest(ctx, *BacktestFlags, trader.Trader)` wiring mock exchange, in-memory DB, feed, engine, and P&L summary
- [x] `subcmds/backtest/waller.go` — Waller backtest command: embeds BacktestFlags + waller.Spec (reused directly, no duplication), constructs waller, calls runBacktest
- [x] `main.go` — Registered `backtest` command group

## Responsibility Boundaries

| Component | Owns |
|---|---|
| `DataFeed` | Raw tick emission from any source |
| `CandleExpander` | Intra-candle price path (e.g. open→low→high→close) |
| `MockProduct` | Open orders, fill logic, price/order topics |
| `MockExchange` | Balance and available balance across all products |
| `Engine` | Time loop: feed → ProcessTick |
| `BacktestFlags` | Shared CLI flags + mock exchange/feed/engine setup |
| `backtest.Waller` | Strategy-specific flags and constructor only |

## Usage

```
tradebot backtest waller \
  --product=BTC-USD --begin=2024-01-01 --end=2024-06-01 \
  --feed=coinbase --data-dir=~/.tradebot \
  --quote-balance=10000 --fee-rate=0.006 \
  --begin-price=60000 --end-price=70000 \
  --buy-interval=1000 --profit-margin=500 --buy-size=0.001
```
