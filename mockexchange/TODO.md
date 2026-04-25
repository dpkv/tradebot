# Mock Exchange — Build Plan

## Tasks

- [x] `datafeed/datafeed.go` — DataFeed interface, Tick struct, CandleExpander func type + built-ins (OpenLowHighClose, OpenHighLowClose)
- [ ] `datafeed/coinbasedb.go` — Candle feed sourced from Coinbase BadgerDB (wraps `coinbase.Datastore.ScanCandles`)
- [ ] `datafeed/csv.go` — Candle feed sourced from a CSV file (columns: time, low, high, close)
- [ ] `mockexchange/exchange.go` — MockExchange: balance tracking, product registry, implements `exchange.Exchange`
- [ ] `mockexchange/product.go` — MockProduct: open order list, fill simulation via `ProcessTick`, publishes price/order topics, implements `exchange.Product`
- [ ] `mockexchange/engine.go` — Engine: pulls ticks from a DataFeed, calls `product.ProcessTick(tick)` in a loop; pluggable via DataFeed interface

## Responsibility Boundaries

| Component | Owns |
|---|---|
| `DataFeed` | Raw tick emission from any source |
| `CandleExpander` | Intra-candle price path (e.g. low→high→close) |
| `MockProduct` | Open orders, fill logic, price/order topics |
| `MockExchange` | Balances, product creation |
| `Engine` | Time loop: feed → ProcessTick |
