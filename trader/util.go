// Copyright (c) 2023 BVK Chaitanya

package trader

import (
	"github.com/bvk/tradebot/gobs"
	"github.com/shopspring/decimal"
)

func filledSize(vs []*gobs.Order) decimal.Decimal {
	var sum decimal.Decimal
	for _, v := range vs {
		sum = sum.Add(v.FilledSize)
	}
	return sum
}

func filledValue(vs []*gobs.Order) decimal.Decimal {
	var sum decimal.Decimal
	for _, v := range vs {
		sum = sum.Add(v.FilledSize.Mul(v.FilledPrice))
	}
	return sum
}

func filledFee(vs []*gobs.Order) decimal.Decimal {
	var sum decimal.Decimal
	for _, v := range vs {
		sum = sum.Add(v.FilledFee)
	}
	return sum
}

func avgPrice(vs []*gobs.Order) decimal.Decimal {
	var sum decimal.Decimal
	for _, v := range vs {
		sum = sum.Add(v.FilledPrice)
	}
	return sum.Div(decimal.NewFromInt(int64(len(vs))))
}

func maxPrice(max decimal.Decimal, vs []*gobs.Order) decimal.Decimal {
	for _, v := range vs {
		if v.FilledPrice.GreaterThan(max) {
			max = v.FilledPrice
		}
	}
	return max
}

func minPrice(min decimal.Decimal, vs []*gobs.Order) decimal.Decimal {
	for _, v := range vs {
		if v.FilledPrice.LessThan(min) {
			min = v.FilledPrice
		}
	}
	return min
}
