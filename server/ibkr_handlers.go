// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/bvk/tradebot/ibkr"
)

func (s *Server) ibkrExchange() *ibkr.Exchange {
	if ex, ok := s.exchangeMap["ibkr"]; ok {
		if ibkrEx, ok := ex.(*ibkr.Exchange); ok {
			return ibkrEx
		}
	}
	return nil
}

func ibkrJSONHandler(fn func(context.Context, *http.Request) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := fn(r.Context(), r)
		if err != nil {
			slog.Warn("ibkr dashboard: api error", "path", r.URL.Path, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}

func (s *Server) handleIBKRBalance(ctx context.Context, r *http.Request) (any, error) {
	ex := s.ibkrExchange()
	if ex == nil {
		return nil, http.ErrNoCookie // placeholder; real error below
	}
	summary, err := ex.GetAccountSummary(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"currency":        summary.Currency,
		"net_liquidation": summary.NetLiquidation.String(),
		"available_funds": summary.AvailableFunds.String(),
		"settled_cash":    summary.SettledCash.String(),
		"cash_balance":    summary.CashBalance.String(),
		"buying_power":    summary.BuyingPower.String(),
	}, nil
}

func (s *Server) handleIBKROrders(ctx context.Context, r *http.Request) (any, error) {
	ex := s.ibkrExchange()
	if ex == nil {
		return nil, http.ErrNoCookie
	}
	orders, err := ex.GetOrders(ctx)
	if err != nil {
		return nil, err
	}

	type optInfo struct {
		occSymbol  string
		underlying string
		optType    string
		strike     string
		expiry     string
	}
	cache := map[int]*optInfo{}
	for _, o := range orders {
		if o.SecType != "OPT" {
			continue
		}
		if _, ok := cache[o.ConID]; ok {
			continue
		}
		contract, err := ex.GetOptionContractInfo(ctx, o.ConID)
		if err != nil {
			slog.Warn("ibkr dashboard: could not resolve option contract info", "conid", o.ConID, "err", err)
			cache[o.ConID] = &optInfo{occSymbol: o.Symbol}
			continue
		}
		cache[o.ConID] = &optInfo{
			occSymbol:  contract.Symbol,
			underlying: contract.Underlying,
			optType:    contract.OptionType,
			strike:     contract.Strike.String(),
			expiry:     contract.Expiry.Format("2006-01-02"),
		}
	}

	out := make([]map[string]any, 0, len(orders))
	for _, o := range orders {
		row := map[string]any{
			"order_id":        o.OrderID,
			"client_order_id": o.ClientOrderID,
			"sec_type":        o.SecType,
			"symbol":          o.Symbol,
			"side":            o.Side,
			"status":          o.Status,
			"limit_price":     o.LimitPrice.String(),
			"ordered_qty":     o.OrderedQty.String(),
			"filled_qty":      o.FilledQty.String(),
			"avg_fill_price":  o.AvgFillPrice.String(),
			"last_exec_time":  "",
		}
		if o.LastExecutionTimeMilli != 0 {
			row["last_exec_time"] = time.UnixMilli(o.LastExecutionTimeMilli).Format(time.DateTime)
		}
		if o.SecType == "OPT" {
			if info, ok := cache[o.ConID]; ok {
				row["symbol"]      = info.occSymbol
				row["underlying"]  = info.underlying
				row["option_type"] = info.optType
				row["strike"]      = info.strike
				row["expiry"]      = info.expiry
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Server) handleIBKRPositions(ctx context.Context, r *http.Request) (any, error) {
	ex := s.ibkrExchange()
	if ex == nil {
		return nil, http.ErrNoCookie
	}
	positions, err := ex.GetPositions(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]map[string]any, 0, len(positions))
	for _, p := range positions {
		row := map[string]any{
			"conid":          p.ConID,
			"ticker":         p.Ticker,
			"sec_type":       p.SecType,
			"qty":            p.Qty.String(),
			"avg_cost":       p.AvgCost.String(),
			"mkt_price":      p.MktPrice.String(),
			"mkt_value":      p.MktValue.String(),
			"unrealized_pnl": p.UnrealizedPnl.String(),
			"realized_pnl":   p.RealizedPnl.String(),
		}
		if p.SecType == "OPT" {
			contract, err := ex.GetOptionContractInfo(ctx, p.ConID)
			if err != nil {
				slog.Warn("ibkr dashboard: could not resolve option position info", "conid", p.ConID, "err", err)
				row["occ_symbol"] = p.Ticker
			} else {
				row["occ_symbol"]   = contract.Symbol
				row["underlying"]   = contract.Underlying
				row["option_type"]  = contract.OptionType
				row["strike"]       = contract.Strike.String()
				row["expiry"]       = contract.Expiry.Format("2006-01-02")
			}
		}
		out = append(out, row)
	}
	return out, nil
}
