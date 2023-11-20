// Copyright (c) 2023 BVK Chaitanya

package waller

import (
	"time"

	"github.com/bvk/tradebot/exchange"
	"github.com/bvk/tradebot/looper"
	"github.com/bvk/tradebot/point"
	"github.com/shopspring/decimal"
)

type buyData struct {
	orders []*exchange.Order
	fees   decimal.Decimal
	size   decimal.Decimal
	value  decimal.Decimal
	feePct decimal.Decimal

	unsoldSize  decimal.Decimal
	unsoldFees  decimal.Decimal
	unsoldValue decimal.Decimal
}

type sellData struct {
	orders []*exchange.Order
	fees   decimal.Decimal
	size   decimal.Decimal
	value  decimal.Decimal
	feePct decimal.Decimal
}

type pairData struct {
	nsells int
	nbuys  int

	profit decimal.Decimal

	fees   decimal.Decimal
	feePct decimal.Decimal
	value  decimal.Decimal

	unsoldFees  decimal.Decimal
	unsoldSize  decimal.Decimal
	unsoldValue decimal.Decimal
}

type summary struct {
	nbuys  int
	nsells int

	profit decimal.Decimal

	fees   decimal.Decimal
	feePct float64
	value  decimal.Decimal

	unsoldFees  decimal.Decimal
	unsoldSize  decimal.Decimal
	unsoldValue decimal.Decimal

	numDays        int
	firstOrderTime time.Time
	lastOrderTime  time.Time

	arr decimal.Decimal
}

type Status struct {
	uid string

	productID string

	pairs []*point.Pair

	analysis *Analysis

	summary     *summary
	buyDataMap  map[int]*buyData
	sellDataMap map[int]*sellData
	pairDataMap map[int]*pairData
}

func (s *Status) UID() string {
	return s.uid
}

func (s *Status) ProductID() string {
	return s.productID
}

func (s *Status) Pairs() []*point.Pair {
	return s.pairs
}

func (s *Status) Analysis() *Analysis {
	return s.analysis
}

func (s *Status) EffectiveFeePct() float64 {
	return s.summary.feePct
}

func (s *Status) NumBuys() int {
	return s.summary.nbuys
}

func (s *Status) NumSells() int {
	return s.summary.nsells
}

func (s *Status) Uptime() time.Duration {
	return time.Now().Sub(s.summary.firstOrderTime)
}

func (s *Status) Budget() decimal.Decimal {
	return s.analysis.Budget()
}

func (s *Status) Profit() decimal.Decimal {
	return s.summary.profit
}

func (s *Status) ReturnRate() decimal.Decimal {
	return s.summary.arr
}

func (s *Status) TotalFees() decimal.Decimal {
	return s.summary.fees
}

func (s *Status) TotalValue() decimal.Decimal {
	return s.summary.value
}

func (s *Status) UnsoldFees() decimal.Decimal {
	return s.summary.unsoldFees
}

func (s *Status) UnsoldSize() decimal.Decimal {
	return s.summary.unsoldSize
}

func (s *Status) UnsoldValue() decimal.Decimal {
	return s.summary.unsoldValue
}

func (s *Status) NumBuysForPair(i int) int {
	return s.pairDataMap[i].nbuys
}

func (s *Status) NumSellsForPair(i int) int {
	return s.pairDataMap[i].nsells
}

func (s *Status) UnsoldSizeForPair(i int) decimal.Decimal {
	return s.pairDataMap[i].unsoldSize
}

func (s *Status) UnsoldValueForPair(i int) decimal.Decimal {
	return s.pairDataMap[i].unsoldValue
}

func (s *Status) FeesForPair(i int) decimal.Decimal {
	return s.pairDataMap[i].fees
}

func (s *Status) FeePctForPair(i int) decimal.Decimal {
	return s.pairDataMap[i].feePct
}

func (s *Status) ProfitForPair(i int) decimal.Decimal {
	return s.pairDataMap[i].profit
}

func (s *Status) NumOrdersAtBuyPoint(i int) int {
	return len(s.buyDataMap[i].orders)
}

func (s *Status) EffectiveFeePctAtBuyPoint(i int) decimal.Decimal {
	return s.buyDataMap[i].feePct
}

func (s *Status) TotalSizeAtBuyPoint(i int) decimal.Decimal {
	return s.buyDataMap[i].size
}

func (s *Status) TotalFeesAtBuyPoint(i int) decimal.Decimal {
	return s.buyDataMap[i].fees
}

func (s *Status) TotalValueAtBuyPoint(i int) decimal.Decimal {
	return s.buyDataMap[i].value
}

func (s *Status) NumOrdersAtSellPoint(i int) int {
	return len(s.sellDataMap[i].orders)
}

func (s *Status) EffectiveFeePctAtSellPoint(i int) decimal.Decimal {
	return s.sellDataMap[i].feePct
}

func (s *Status) TotalSizeAtSellPoint(i int) decimal.Decimal {
	return s.sellDataMap[i].size
}

func (s *Status) TotalFeesAtSellPoint(i int) decimal.Decimal {
	return s.sellDataMap[i].fees
}

func (s *Status) TotalValueAtSellPoint(i int) decimal.Decimal {
	return s.sellDataMap[i].value
}

func (w *Waller) Status() *Status {
	var pairs []*point.Pair
	for i := range w.buyPoints {
		p := &point.Pair{
			Buy:  *w.buyPoints[i],
			Sell: *w.sellPoints[i],
		}
		pairs = append(pairs, p)
	}

	s := &Status{
		uid:       w.uid,
		productID: w.productID,
		pairs:     pairs,
	}
	w.summarize(s)
	return s
}

func (w *Waller) findLooper(p *point.Pair) *looper.Looper {
	for _, l := range w.loopers {
		if p.Equal(l.Pair()) {
			return l
		}
	}
	return nil
}

func (w *Waller) getBuyOrders(p *point.Pair) []*exchange.Order {
	loop := w.findLooper(p)
	if loop == nil {
		return nil
	}
	return loop.GetBuyOrders()
}

func (w *Waller) getSellOrders(p *point.Pair) []*exchange.Order {
	loop := w.findLooper(p)
	if loop == nil {
		return nil
	}
	return loop.GetSellOrders()
}

func (w *Waller) summarize(s *Status) {
	minTime := func(a, b time.Time) time.Time {
		if a.Before(b) {
			return a
		}
		return b
	}

	hundred := decimal.NewFromInt(100)
	daysPerYear := decimal.NewFromInt(365)

	buyDataMap := make(map[int]*buyData)
	sellDataMap := make(map[int]*sellData)
	pairDataMap := make(map[int]*pairData)

	summary := &summary{
		firstOrderTime: time.Now(),
	}

	for i, pair := range s.pairs {
		buys := w.getBuyOrders(pair)
		sells := w.getSellOrders(pair)
		if len(buys) == 0 && len(sells) == 0 {
			continue
		}

		sdata := &sellData{
			orders: sells,
		}
		var lastSellTime time.Time
		for _, sell := range sells {
			if sell.Done {
				sdata.fees = sdata.fees.Add(sell.Fee)
				sdata.size = sdata.size.Add(sell.FilledSize)
				sdata.value = sdata.value.Add(sell.FilledSize.Mul(sell.FilledPrice))
				lastSellTime = sell.CreateTime.Time

				summary.firstOrderTime = minTime(summary.firstOrderTime, sell.CreateTime.Time)
			}
		}
		if len(sells) > 0 {
			sdata.feePct = sdata.fees.Mul(hundred).Div(sdata.value)
		}

		bdata := &buyData{
			orders: buys,
		}
		for _, buy := range buys {
			if buy.Done {
				bdata.fees = bdata.fees.Add(buy.Fee)
				bdata.size = bdata.size.Add(buy.FilledSize)
				bdata.value = bdata.value.Add(buy.FilledSize.Mul(buy.FilledPrice))

				if buy.CreateTime.Time.After(lastSellTime) {
					bdata.unsoldFees = bdata.unsoldFees.Add(buy.Fee)
					bdata.unsoldSize = bdata.unsoldSize.Add(buy.FilledSize)
					bdata.unsoldValue = bdata.unsoldValue.Add(buy.FilledSize.Mul(buy.FilledPrice))
				}

				summary.firstOrderTime = minTime(summary.firstOrderTime, buy.CreateTime.Time)
			}
		}
		if len(buys) > 0 {
			bdata.feePct = bdata.fees.Mul(hundred).Div(bdata.value)
		}

		pdata := &pairData{
			nbuys:  int(bdata.size.Div(s.pairs[i].Buy.Size).IntPart()),
			nsells: int(sdata.size.Div(s.pairs[i].Sell.Size).IntPart()),
			fees:   bdata.fees.Add(sdata.fees),
			value:  bdata.value.Add(sdata.value),

			unsoldFees:  bdata.unsoldFees,
			unsoldSize:  bdata.unsoldSize,
			unsoldValue: bdata.unsoldValue,
		}
		pdata.feePct = pdata.fees.Mul(hundred).Div(pdata.value)

		if pdata.nsells > 0 {
			pdata.profit = sdata.value.Sub(sdata.fees).Sub(bdata.fees).Sub(bdata.value).Add(bdata.unsoldFees).Add(bdata.unsoldValue)
		}

		summary.nbuys += pdata.nbuys
		summary.nsells += pdata.nsells
		summary.fees = summary.fees.Add(pdata.fees)
		summary.value = summary.value.Add(pdata.value)
		summary.feePct = summary.fees.Mul(hundred).Div(summary.value).InexactFloat64()
		summary.profit = summary.profit.Add(pdata.profit)
		summary.unsoldFees = summary.unsoldFees.Add(pdata.unsoldFees)
		summary.unsoldSize = summary.unsoldSize.Add(pdata.unsoldSize)
		summary.unsoldValue = summary.unsoldValue.Add(pdata.unsoldValue)

		pairDataMap[i] = pdata
		if len(buys) > 0 {
			buyDataMap[i] = bdata
		}
		if len(sells) > 0 {
			sellDataMap[i] = sdata
		}
	}
	analysis := Analyze(s.pairs, summary.feePct)
	duration := time.Now().Sub(summary.firstOrderTime)
	numDays := int64(duration/time.Hour/24) + 1
	profitPerYear := summary.profit.Div(decimal.NewFromInt(numDays)).Mul(daysPerYear)
	summary.arr = profitPerYear.Mul(hundred).Div(analysis.Budget())

	s.analysis = analysis
	s.buyDataMap = buyDataMap
	s.sellDataMap = sellDataMap
	s.pairDataMap = pairDataMap
	s.summary = summary
}