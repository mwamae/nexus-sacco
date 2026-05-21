// Amortization schedule generator (Phase 6c) — pure function.
//
// Supports two repayment methods initially:
//   reducing_balance — standard amortising PMT (constant installment,
//                      interest reduces as principal does)
//   flat_rate        — interest computed on original principal,
//                      total spread evenly across installments
//
// Bullet and interest_only are recognised but stubbed out (return a
// single-row schedule); Phase 6e or later fills them in.
//
// Due dates: first_due_date = disbursement_date + (grace + 1) months.
// Subsequent installments at +1 month intervals (Go's AddDate clamps
// day-of-month for short months — fine for amortisation).

package domain

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
)

type ScheduleRow struct {
	InstallmentNo    int             `json:"installment_no"`
	DueDate          time.Time       `json:"due_date"`
	PrincipalDue     decimal.Decimal `json:"principal_due"`
	InterestDue      decimal.Decimal `json:"interest_due"`
	FeeDue           decimal.Decimal `json:"fee_due"`
	TotalDue         decimal.Decimal `json:"total_due"`
	OutstandingAfter decimal.Decimal `json:"outstanding_after"` // principal balance projected after this row
}

// GenerateSchedule produces the full amortization table. Pure: no DB
// calls, no time dependencies other than the supplied disbursement date.
func GenerateSchedule(
	principal, annualRatePct decimal.Decimal,
	termMonths int,
	gracePeriodMonths int,
	disbursementDate time.Time,
	interestMethod LoanInterestMethod,
	repayMethod LoanRepaymentMethod,
) []ScheduleRow {
	if termMonths <= 0 || principal.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	firstDue := firstDueDate(disbursementDate, gracePeriodMonths)
	switch repayMethod {
	case RepayReducingBalance:
		return scheduleReducingBalance(principal, annualRatePct, termMonths, firstDue)
	case RepayFlatRate:
		return scheduleFlatRate(principal, annualRatePct, termMonths, firstDue)
	case RepayBullet:
		return scheduleBullet(principal, annualRatePct, termMonths, firstDue, interestMethod)
	case RepayInterestOnly:
		return scheduleInterestOnly(principal, annualRatePct, termMonths, firstDue)
	}
	// Fallback — treat as reducing balance.
	return scheduleReducingBalance(principal, annualRatePct, termMonths, firstDue)
}

func firstDueDate(disbursed time.Time, grace int) time.Time {
	return disbursed.AddDate(0, grace+1, 0)
}

// scheduleReducingBalance — PMT-based amortisation. Computes a constant
// installment, applies it to a declining principal each period.
func scheduleReducingBalance(principal, annualRatePct decimal.Decimal, n int, firstDue time.Time) []ScheduleRow {
	out := make([]ScheduleRow, 0, n)
	monthlyR := annualRatePct.Div(decimal.NewFromInt(1200)) // /100 / 12
	var installment decimal.Decimal
	if monthlyR.IsZero() {
		installment = principal.Div(decimal.NewFromInt(int64(n))).Round(2)
	} else {
		// float for PMT, then snap to 2dp
		r, _ := monthlyR.Float64()
		p, _ := principal.Float64()
		pmt := p * r / (1.0 - math.Pow(1.0+r, -float64(n)))
		installment = decimal.NewFromFloat(pmt).Round(2)
	}
	remaining := principal
	for i := 1; i <= n; i++ {
		var interestDue, principalDue decimal.Decimal
		if monthlyR.IsZero() {
			interestDue = decimal.Zero
			principalDue = installment
		} else {
			interestDue = remaining.Mul(monthlyR).Round(2)
			principalDue = installment.Sub(interestDue)
		}
		// Final installment — flush rounding residual.
		if i == n {
			principalDue = remaining
			installment = principalDue.Add(interestDue).Round(2)
		}
		remaining = remaining.Sub(principalDue)
		if remaining.LessThan(decimal.Zero) {
			remaining = decimal.Zero
		}
		out = append(out, ScheduleRow{
			InstallmentNo:    i,
			DueDate:          firstDue.AddDate(0, i-1, 0),
			PrincipalDue:     principalDue,
			InterestDue:      interestDue,
			FeeDue:           decimal.Zero,
			TotalDue:         principalDue.Add(interestDue),
			OutstandingAfter: remaining,
		})
	}
	return out
}

// scheduleFlatRate — total interest computed on original principal,
// then total + principal spread evenly across n installments.
func scheduleFlatRate(principal, annualRatePct decimal.Decimal, n int, firstDue time.Time) []ScheduleRow {
	years := decimal.NewFromInt(int64(n)).Div(decimal.NewFromInt(12))
	totalInterest := principal.Mul(annualRatePct).Div(decimal.NewFromInt(100)).Mul(years).Round(2)
	principalPer := principal.Div(decimal.NewFromInt(int64(n))).Round(2)
	interestPer := totalInterest.Div(decimal.NewFromInt(int64(n))).Round(2)
	out := make([]ScheduleRow, 0, n)
	remaining := principal
	for i := 1; i <= n; i++ {
		p := principalPer
		intr := interestPer
		if i == n {
			// Flush principal residual to the last row.
			p = remaining
			// Recompute interest residual to make totals exact.
			intr = totalInterest.Sub(interestPer.Mul(decimal.NewFromInt(int64(n - 1)))).Round(2)
		}
		remaining = remaining.Sub(p)
		if remaining.LessThan(decimal.Zero) {
			remaining = decimal.Zero
		}
		out = append(out, ScheduleRow{
			InstallmentNo:    i,
			DueDate:          firstDue.AddDate(0, i-1, 0),
			PrincipalDue:     p,
			InterestDue:      intr,
			FeeDue:           decimal.Zero,
			TotalDue:         p.Add(intr),
			OutstandingAfter: remaining,
		})
	}
	return out
}

// scheduleBullet — single payment at maturity (no intermediate
// installments). Interest accrued via the chosen interest method.
func scheduleBullet(principal, annualRatePct decimal.Decimal, n int, firstDue time.Time, im LoanInterestMethod) []ScheduleRow {
	years := decimal.NewFromInt(int64(n)).Div(decimal.NewFromInt(12))
	var interest decimal.Decimal
	if im == InterestFlat {
		interest = principal.Mul(annualRatePct).Div(decimal.NewFromInt(100)).Mul(years).Round(2)
	} else {
		// Compounding monthly on a non-reducing balance is unusual for SACCO
		// bullets; treat as simple interest unless the product says otherwise.
		interest = principal.Mul(annualRatePct).Div(decimal.NewFromInt(100)).Mul(years).Round(2)
	}
	dueDate := firstDue.AddDate(0, n-1, 0)
	return []ScheduleRow{{
		InstallmentNo:    1,
		DueDate:          dueDate,
		PrincipalDue:     principal,
		InterestDue:      interest,
		FeeDue:           decimal.Zero,
		TotalDue:         principal.Add(interest),
		OutstandingAfter: decimal.Zero,
	}}
}

// scheduleInterestOnly — interest per period, principal at maturity.
func scheduleInterestOnly(principal, annualRatePct decimal.Decimal, n int, firstDue time.Time) []ScheduleRow {
	monthlyInt := principal.Mul(annualRatePct).Div(decimal.NewFromInt(1200)).Round(2)
	out := make([]ScheduleRow, 0, n)
	remaining := principal
	for i := 1; i <= n; i++ {
		var p decimal.Decimal
		if i == n {
			p = remaining
		}
		remaining = remaining.Sub(p)
		out = append(out, ScheduleRow{
			InstallmentNo:    i,
			DueDate:          firstDue.AddDate(0, i-1, 0),
			PrincipalDue:     p,
			InterestDue:      monthlyInt,
			FeeDue:           decimal.Zero,
			TotalDue:         p.Add(monthlyInt),
			OutstandingAfter: remaining,
		})
	}
	return out
}
