// Copyright (c) 2026 Deepak Vankadaru

package server

import (
	"context"
	"fmt"
	"sort"

	"github.com/bvk/tradebot/api"
	"github.com/bvk/tradebot/point"
	"github.com/bvk/tradebot/waller"
	"github.com/shopspring/decimal"
)

// wallerSpec is a server-side replica of the CLI Spec used by "waller add"
// and "waller query", trimmed down to just the fields we need for validation.
type wallerSpec struct {
	feePct float64

	beginPrice float64
	endPrice   float64

	buyInterval    float64
	buyIntervalPct float64

	profitMargin    float64
	profitMarginPct float64

	buySize float64

	cancelOffsetPct float64
	cancelOffset    float64
}

func (s *wallerSpec) setDefaults() {
	// Calculate cancel-offset as N% away from the buy/sell prices, using the
	// middle of the selected price range as the reference (same as CLI).
	if s.cancelOffset == 0 {
		if s.beginPrice > 0 && s.endPrice > 0 && s.cancelOffsetPct != 0 {
			mid := decimal.NewFromFloat((s.beginPrice + s.endPrice) / 2)
			off, _ := mid.Mul(decimal.NewFromFloat(s.cancelOffsetPct)).Div(decimal.NewFromInt(100)).Float64()
			s.cancelOffset = off
		}
	}
}

// check validates the spec parameters using the same rules as the CLI.
func (s *wallerSpec) check() error {
	s.setDefaults()

	if s.beginPrice <= 0 || s.endPrice <= 0 {
		return fmt.Errorf("begin/end price ranges cannot be zero or negative")
	}
	if s.buySize <= 0 {
		return fmt.Errorf("buy size cannot be zero or negative")
	}
	if s.buyInterval <= 0 && s.buyIntervalPct <= 0 {
		return fmt.Errorf("buy interval cannot be zero or negative")
	}
	if s.buyInterval > 0 && s.buyIntervalPct > 0 {
		return fmt.Errorf("only one of buy interval and buy interval percent can be positive")
	}

	if s.cancelOffset <= 0 {
		return fmt.Errorf("buy/sell cancel offsets cannot be zero or negative")
	}

	if s.profitMargin <= 0 && s.profitMarginPct <= 0 {
		return fmt.Errorf("one of profit margin or profit margin percent must be given")
	}
	if s.profitMargin > 0 && s.profitMarginPct > 0 {
		return fmt.Errorf("only one of profit margin and profit margin percent can be positive")
	}

	if s.endPrice <= s.beginPrice {
		return fmt.Errorf("end price range cannot be lower or equal to the begin price")
	}
	if s.feePct < 0 || s.feePct >= 100 {
		return fmt.Errorf("fee percentage should be in between 0-100")
	}

	return nil
}

func (s *wallerSpec) feeDecimal() decimal.Decimal {
	return decimal.NewFromFloat(s.feePct)
}

func (s *wallerSpec) buyIntervalSize(p decimal.Decimal) decimal.Decimal {
	if s.buyIntervalPct == 0 {
		return decimal.NewFromFloat(s.buyInterval)
	}
	return p.Mul(decimal.NewFromFloat(s.buyIntervalPct).Div(decimal.NewFromInt(100)))
}

func (s *wallerSpec) fixedProfitPairs() ([]*point.Pair, error) {
	var pairs []*point.Pair
	beginPrice := decimal.NewFromFloat(s.beginPrice)
	endPrice := decimal.NewFromFloat(s.endPrice)
	cancelOffset := decimal.NewFromFloat(s.cancelOffset)

	for price := beginPrice; price.LessThan(endPrice); price = price.Add(s.buyIntervalSize(price)) {
		buy := &point.Point{
			Price:  price,
			Size:   decimal.NewFromFloat(s.buySize),
			Cancel: price.Add(cancelOffset),
		}
		if err := buy.Check(); err != nil {
			return nil, fmt.Errorf("invalid buy point: %w", err)
		}
		margin := decimal.NewFromFloat(s.profitMargin)
		sell, err := point.SellPoint(buy, margin)
		if err != nil {
			return nil, fmt.Errorf("could not compute sell point: %w", err)
		}

		p := &point.Pair{Buy: *buy, Sell: *sell}
		if s.feePct != 0 {
			p = point.AdjustForMargin(p, s.feeDecimal())
		}
		pairs = append(pairs, p)
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("could not create fixed profit margin based buy/sell pairs")
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Buy.Price.LessThan(pairs[j].Buy.Price)
	})
	return pairs, nil
}

func (s *wallerSpec) percentProfitPairs() ([]*point.Pair, error) {
	var pairs []*point.Pair
	beginPrice := decimal.NewFromFloat(s.beginPrice)
	endPrice := decimal.NewFromFloat(s.endPrice)
	cancelOffset := decimal.NewFromFloat(s.cancelOffset)

	for price := beginPrice; price.LessThan(endPrice); price = price.Add(s.buyIntervalSize(price)) {
		buy := &point.Point{
			Price:  price,
			Size:   decimal.NewFromFloat(s.buySize),
			Cancel: price.Add(cancelOffset),
		}
		if err := buy.Check(); err != nil {
			return nil, fmt.Errorf("invalid buy point: %w", err)
		}

		// margin := buyValue * marginPct / 100
		margin := buy.Value().Mul(decimal.NewFromFloat(s.profitMarginPct).Div(decimal.NewFromInt(100)))
		sell, err := point.SellPoint(buy, margin)
		if err != nil {
			return nil, fmt.Errorf("could not compute sell point: %w", err)
		}

		p := &point.Pair{Buy: *buy, Sell: *sell}
		if s.feePct != 0 {
			p = point.AdjustForMargin(p, s.feeDecimal())
		}
		pairs = append(pairs, p)
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("could not create percentage profit buy/sell pairs")
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Buy.Price.LessThan(pairs[j].Buy.Price)
	})
	return pairs, nil
}

// doWallerValidate validates the input spec and, when valid, returns the
// generated pairs and summary analysis (similar to "waller query").
func (s *Server) doWallerValidate(ctx context.Context, req *api.WallerValidateRequest) (*api.WallerValidateResponse, error) {
	resp := &api.WallerValidateResponse{}

	spec := &wallerSpec{
		feePct:          req.FeePct,
		beginPrice:      req.BeginPrice,
		endPrice:        req.EndPrice,
		buyInterval:     req.BuyInterval,
		buyIntervalPct:  req.BuyIntervalPct,
		profitMargin:    req.ProfitMargin,
		profitMarginPct: req.ProfitMarginPct,
		buySize:         req.BuySize,
		cancelOffsetPct: req.CancelOffsetPct,
	}

	if err := spec.check(); err != nil {
		resp.Valid = false
		resp.Error = err.Error()
		return resp, nil
	}

	var (
		pairs []*point.Pair
		err   error
	)
	if spec.profitMargin > 0 {
		pairs, err = spec.fixedProfitPairs()
	} else {
		pairs, err = spec.percentProfitPairs()
	}
	if err != nil {
		resp.Valid = false
		resp.Error = err.Error()
		return resp, nil
	}

	// Run analysis using the shared waller.Analysis type.
	a := waller.Analyze(pairs, spec.feeDecimal())

	summary := &api.WallerValidateSummary{
		Budget:    a.Budget().StringFixed(5),
		FeePct:    spec.feeDecimal().StringFixed(5),
		NumPairs:  a.NumPairs(),
		MinLoopFee: a.MinLoopFee().StringFixed(5),
		AvgLoopFee: a.AvgLoopFee().StringFixed(5),
		MaxLoopFee: a.MaxLoopFee().StringFixed(5),
		MinPriceMargin:  a.MinPriceMargin().StringFixed(5),
		AvgPriceMargin:  a.AvgPriceMargin().StringFixed(5),
		MaxPriceMargin:  a.MaxPriceMargin().StringFixed(5),
		MinProfitMargin: a.MinProfitMargin().StringFixed(5),
		AvgProfitMargin: a.AvgProfitMargin().StringFixed(5),
		MaxProfitMargin: a.MaxProfitMargin().StringFixed(5),
	}

	// APR summary for a few standard targets, mirroring PrintAnalysis.
	aprs := []float64{5, 10, 20, 30}
	for _, rate := range aprs {
		nsells := a.NumSellsForReturnRate(rate)
		row := &api.WallerValidateAPRRow{
			RatePct:          rate,
			NumSellsPerYear:  decimal.NewFromInt(int64(nsells)).StringFixed(2),
			NumSellsPerMonth: decimal.NewFromFloat(float64(nsells) / 12.0).StringFixed(2),
			NumSellsPerDay:   decimal.NewFromFloat(float64(nsells) / 365.0).StringFixed(2),
		}
		summary.APRs = append(summary.APRs, row)
	}

	// Build preview rows for the UI (similar to "waller query -print-pairs").
	var previews []*api.WallerValidatePairPreview
	d100 := decimal.NewFromInt(100)
	feePctDec := spec.feeDecimal()
	for _, p := range pairs {
		bfee := p.Buy.Price.Mul(p.Buy.Size).Mul(feePctDec).Div(d100)
		sfee := p.Sell.Price.Mul(p.Sell.Size).Mul(feePctDec).Div(d100)
		profit := p.ValueMargin().Sub(bfee).Sub(sfee)

		previews = append(previews, &api.WallerValidatePairPreview{
			BuySize:     p.Buy.Size.StringFixed(5),
			BuyPrice:    p.Buy.Price.StringFixed(5),
			BuyCancel:   p.Buy.Cancel.StringFixed(5),
			SellSize:    p.Sell.Size.StringFixed(5),
			SellPrice:   p.Sell.Price.StringFixed(5),
			SellCancel:  p.Sell.Cancel.StringFixed(5),
			PriceMargin: p.Sell.Price.Sub(p.Buy.Price).StringFixed(5),
			Profit:      profit.StringFixed(2),
		})
	}

	resp.Valid = true
	resp.Error = ""
	resp.Summary = summary
	resp.PreviewPairs = previews
	return resp, nil
}

