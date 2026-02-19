// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/gobs"
	"github.com/bvk/tradebot/namer"
	"github.com/bvk/tradebot/timerange"
	"github.com/bvk/tradebot/waller"
	"github.com/bvkgo/kv"
)

func (s *Server) doWallerGet(ctx context.Context, uid string) (*api.WallerGetResponse, error) {
	if uid == "" {
		return nil, fmt.Errorf("uid is required: %w", os.ErrInvalid)
	}

	resp := new(api.WallerGetResponse)
	found := false

	collect := func(ctx context.Context, r kv.Reader, jd *gobs.JobData) error {
		if jd.ID != uid {
			return nil
		}
		found = true

		if jd.Typename != "Waller" {
			return fmt.Errorf("job %q is of type %q, not Waller: %w", uid, jd.Typename, os.ErrInvalid)
		}

		name, _, _, err := namer.Resolve(ctx, r, jd.ID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("could not resolve job name: %w", err)
		}

		resp.UID = jd.ID
		resp.Type = jd.Typename
		resp.Status = string(jd.State)
		resp.Name = name
		resp.ManualFlag = (jd.Flags & ManualFlag) != 0

		t, err := Load(ctx, r, jd.ID, jd.Typename)
		if err != nil {
			return fmt.Errorf("could not load trader %q: %w", uid, err)
		}
		resp.ProductID = t.ProductID()
		resp.ExchangeName = t.ExchangeName()

		wall := t.(*waller.Waller)

		if st := wall.Status(&timerange.Range{}); st != nil {
			resp.HasStatus = true
			resp.Budget = st.Budget.StringFixed(5)
			resp.Return = st.ReturnRate().StringFixed(5)
			resp.AnnualReturn = st.AnnualReturnRate().StringFixed(5)
			resp.ProfitPerDay = st.ProfitPerDay().StringFixed(5)
			resp.Days = st.NumDays().StringFixed(2)
			resp.Buys = st.NumBuys
			resp.Sells = st.NumSells
			resp.Profit = st.Profit().StringFixed(5)
			resp.Fees = st.Fees().StringFixed(5)

			resp.BoughtFees = st.BoughtFees.StringFixed(5)
			resp.BoughtSize = st.BoughtSize.StringFixed(5)
			resp.BoughtValue = st.BoughtValue.StringFixed(5)

			resp.SoldFees = st.SoldFees.StringFixed(5)
			resp.SoldSize = st.SoldSize.StringFixed(5)
			resp.SoldValue = st.SoldValue.StringFixed(5)

			resp.UnsoldFees = st.UnsoldFees.StringFixed(5)
			resp.UnsoldSize = st.UnsoldSize.StringFixed(5)
			resp.UnsoldValue = st.UnsoldValue.StringFixed(5)

			resp.OversoldFees = st.OversoldFees.StringFixed(5)
			resp.OversoldSize = st.OversoldSize.StringFixed(5)
			resp.OversoldValue = st.OversoldValue.StringFixed(5)
		}

		for i, p := range wall.Pairs() {
			ps := wall.PairStatus(p, &timerange.Range{})
			if ps == nil {
				continue
			}
			pair := &api.WallerGetPairItem{
				Index:        i,
				Pair:         fmt.Sprintf("%s-%s", p.Buy.Price.StringFixed(5), p.Sell.Price.StringFixed(5)),
				Buys:         ps.NumBuys,
				Sells:        ps.NumSells,
				Budget:       ps.Budget.StringFixed(5),
				Return:       ps.ReturnRate().StringFixed(5),
				AnnualReturn: ps.AnnualReturnRate().StringFixed(5),
				Days:         ps.NumDays().StringFixed(5),
				Profit:       ps.Profit().StringFixed(5),
				Fees:         ps.Fees().StringFixed(5),
				BoughtValue:  ps.Bought().StringFixed(5),
				SoldValue:    ps.Sold().StringFixed(5),
				UnsoldValue:  ps.UnsoldValue.StringFixed(5),
				SoldSize:     ps.SoldSize.Sub(ps.OversoldSize).StringFixed(5),
				UnsoldSize:   ps.UnsoldSize.StringFixed(5),
			}
			resp.Pairs = append(resp.Pairs, pair)
		}

		return nil
	}

	if err := s.runner.Scan(ctx, nil, collect); err != nil {
		return nil, fmt.Errorf("could not scan jobs: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("job %q not found: %w", uid, os.ErrNotExist)
	}
	return resp, nil
}
