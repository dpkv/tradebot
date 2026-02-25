// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"maps"
	"strings"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/kvutil"
	"github.com/shopspring/decimal"
)

func (s *Server) doAlertsGet(ctx context.Context) (*api.AlertsGetResponse, error) {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()

	resp := &api.AlertsGetResponse{
		LowBalanceLimits: make(map[string]string),
		PerExchange:      make(map[string]api.AlertsExchangeConfig),
	}
	if state == nil || state.AlertsConfig == nil {
		return resp, nil
	}
	cfg := state.AlertsConfig
	for ccy, lim := range cfg.LowBalanceLimits {
		resp.LowBalanceLimits[ccy] = lim.String()
	}
	for ex, exCfg := range cfg.PerExchangeConfig {
		if exCfg == nil || exCfg.LowBalanceLimits == nil {
			continue
		}
		entry := api.AlertsExchangeConfig{LowBalanceLimits: make(map[string]string)}
		for ccy, lim := range exCfg.LowBalanceLimits {
			entry.LowBalanceLimits[ccy] = lim.String()
		}
		resp.PerExchange[ex] = entry
	}
	return resp, nil
}

func (s *Server) doAlertsPost(ctx context.Context, req *api.AlertsPostRequest) (*api.AlertsPostResponse, error) {
	if req.LowBalanceLimits == nil {
		req.LowBalanceLimits = make(map[string]string)
	}

	limitsMap := make(map[string]decimal.Decimal)
	for ccy, str := range req.LowBalanceLimits {
		ccy = strings.ToUpper(strings.TrimSpace(ccy))
		if ccy == "" {
			continue
		}
		lim, err := decimal.NewFromString(strings.TrimSpace(str))
		if err != nil {
			return nil, err
		}
		limitsMap[ccy] = lim
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := gobs.Clone(s.state)
	if err != nil {
		return nil, err
	}
	if state.AlertsConfig == nil {
		state.AlertsConfig = new(gobs.AlertsConfig)
	}
	if state.AlertsConfig.LowBalanceLimits == nil {
		state.AlertsConfig.LowBalanceLimits = make(map[string]decimal.Decimal)
	}
	if state.AlertsConfig.PerExchangeConfig == nil {
		state.AlertsConfig.PerExchangeConfig = make(map[string]*gobs.AlertsConfig)
	}

	exchange := strings.ToLower(strings.TrimSpace(req.Exchange))
	if exchange != "" {
		if _, ok := state.AlertsConfig.PerExchangeConfig[exchange]; !ok {
			state.AlertsConfig.PerExchangeConfig[exchange] = new(gobs.AlertsConfig)
		}
		exCfg := state.AlertsConfig.PerExchangeConfig[exchange]
		if exCfg.LowBalanceLimits == nil {
			exCfg.LowBalanceLimits = make(map[string]decimal.Decimal)
		}
		maps.Copy(exCfg.LowBalanceLimits, limitsMap)
	} else {
		maps.Copy(state.AlertsConfig.LowBalanceLimits, limitsMap)
	}

	if err := kvutil.SetDB[gobs.ServerState](ctx, s.db, ServerStateKey, state); err != nil {
		return nil, err
	}
	s.state = state
	return &api.AlertsPostResponse{OK: true}, nil
}
