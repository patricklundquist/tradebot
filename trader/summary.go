// Copyright (c) 2023 BVK Chaitanya

package trader

import (
	"fmt"

	"github.com/bvk/tradebot/timerange"
	"github.com/shopspring/decimal"
)

type Summary struct {
	TimePeriod timerange.Range

	NumSells int
	NumBuys  int

	Budget decimal.Decimal

	SoldFees  decimal.Decimal
	SoldSize  decimal.Decimal
	SoldValue decimal.Decimal

	BoughtFees  decimal.Decimal
	BoughtSize  decimal.Decimal
	BoughtValue decimal.Decimal

	UnsoldFees  decimal.Decimal
	UnsoldSize  decimal.Decimal
	UnsoldValue decimal.Decimal

	OversoldFees  decimal.Decimal
	OversoldSize  decimal.Decimal
	OversoldValue decimal.Decimal
}

func (s *Summary) String() string {
	return fmt.Sprintf("nsells=%d nbuys=%d sfees=%s ssize=%s svalue=%s bfees=%s bsize=%s bvalue=%s",
		s.NumSells, s.NumBuys, s.SoldFees.StringFixed(3), s.SoldSize.StringFixed(3), s.SoldValue.StringFixed(3),
		s.BoughtFees.StringFixed(3), s.BoughtSize.StringFixed(3), s.BoughtValue.StringFixed(3))
}

func (s *Summary) FeePct() decimal.Decimal {
	divisor := s.SoldValue.Add(s.BoughtValue)
	if divisor.IsZero() {
		return decimal.Zero
	}
	totalFees := s.SoldFees.Add(s.BoughtFees)
	d100 := decimal.NewFromInt(100)
	return totalFees.Mul(d100).Div(divisor)
}

func (s *Summary) Sold() decimal.Decimal {
	return s.SoldValue.Sub(s.OversoldValue)
}

func (s *Summary) Bought() decimal.Decimal {
	return s.BoughtValue.Sub(s.UnsoldValue)
}

func (s *Summary) Fees() decimal.Decimal {
	sfees := s.SoldFees.Sub(s.OversoldFees)
	bfees := s.BoughtFees.Sub(s.UnsoldFees)
	return sfees.Add(bfees)
}

func (s *Summary) Profit() decimal.Decimal {
	svalue := s.SoldValue.Sub(s.OversoldValue)
	bvalue := s.BoughtValue.Sub(s.UnsoldValue)
	sfees := s.SoldFees.Sub(s.OversoldFees)
	bfees := s.BoughtFees.Sub(s.UnsoldFees)
	profit := svalue.Sub(bvalue).Sub(bfees).Sub(sfees)
	return profit
}

func (s *Summary) NumDays() decimal.Decimal {
	if s.TimePeriod.IsZero() {
		return decimal.Zero
	}
	return decimal.NewFromFloat(s.TimePeriod.Duration().Hours() / 24)
}

func (s *Summary) ProfitPerDay() decimal.Decimal {
	ndays := s.NumDays()
	if ndays.IsZero() {
		return s.Profit()
	}
	return s.Profit().Div(ndays)
}

func (s *Summary) ReturnRate() decimal.Decimal {
	if s.Budget.IsZero() {
		return decimal.Zero
	}
	return s.Profit().Mul(decimal.NewFromInt(100)).Div(s.Budget)
}

func (s *Summary) AnnualReturnRate() decimal.Decimal {
	if s.Budget.IsZero() {
		return decimal.Zero
	}
	perYear := s.ProfitPerDay().Mul(decimal.NewFromInt(365))
	return perYear.Mul(decimal.NewFromInt(100)).Div(s.Budget)
}

func Summarize(statuses []*Status) *Summary {
	sum := new(Summary)

	var tr *timerange.Range
	for i, s := range statuses {
		if i == 0 {
			tr = &s.TimePeriod
		} else {
			tr = timerange.Union(tr, &s.TimePeriod)
		}

		sum.NumBuys += s.NumBuys
		sum.NumSells += s.NumSells
		sum.Budget = sum.Budget.Add(s.Budget)

		sum.SoldFees = sum.SoldFees.Add(s.SoldFees)
		sum.SoldSize = sum.SoldSize.Add(s.SoldSize)
		sum.SoldValue = sum.SoldValue.Add(s.SoldValue)

		sum.BoughtFees = sum.BoughtFees.Add(s.BoughtFees)
		sum.BoughtSize = sum.BoughtSize.Add(s.BoughtSize)
		sum.BoughtValue = sum.BoughtValue.Add(s.BoughtValue)

		sum.UnsoldFees = sum.UnsoldFees.Add(s.UnsoldFees)
		sum.UnsoldSize = sum.UnsoldSize.Add(s.UnsoldSize)
		sum.UnsoldValue = sum.UnsoldValue.Add(s.UnsoldValue)

		sum.OversoldFees = sum.OversoldFees.Add(s.OversoldFees)
		sum.OversoldSize = sum.OversoldSize.Add(s.OversoldSize)
		sum.OversoldValue = sum.OversoldValue.Add(s.OversoldValue)
	}

	if tr != nil {
		sum.TimePeriod = *tr
	}
	return sum
}
