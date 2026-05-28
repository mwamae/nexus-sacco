// Loans Phase 4 — dividend offset waterfall allocation tests.
//
// Pure-function tests over allocateInWaterfall / decimalMin. The
// preview + post endpoints are integration-level (require a populated
// dividend run); covered separately via the savings smoke tests.

package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestAllocateInWaterfall_PenaltyFirst(t *testing.T) {
	l := offsetLoanRow{
		LoanID:           uuid.New(),
		LoanNo:           "L-1",
		PrincipalOverdue: dec("1000"),
		InterestBalance:  dec("200"),
		PenaltyBalance:   dec("50"),
		TotalArrears:     dec("1250"),
	}
	a := allocateInWaterfall(dec("100"), l)
	if !a.Penalty.Equal(dec("50")) {
		t.Errorf("penalty: got %s want 50", a.Penalty)
	}
	if !a.Interest.Equal(dec("50")) {
		t.Errorf("interest: got %s want 50 (remaining after penalty)", a.Interest)
	}
	if !a.PrincipalOverdue.Equal(dec("0")) {
		t.Errorf("principal: got %s want 0", a.PrincipalOverdue)
	}
}

func TestAllocateInWaterfall_FullCoverage(t *testing.T) {
	l := offsetLoanRow{
		PrincipalOverdue: dec("1000"),
		InterestBalance:  dec("200"),
		PenaltyBalance:   dec("50"),
		TotalArrears:     dec("1250"),
	}
	a := allocateInWaterfall(dec("1250"), l)
	if !a.Penalty.Equal(dec("50")) || !a.Interest.Equal(dec("200")) || !a.PrincipalOverdue.Equal(dec("1000")) {
		t.Errorf("full coverage allocation wrong: %+v", a)
	}
	total := a.Penalty.Add(a.Interest).Add(a.PrincipalOverdue)
	if !total.Equal(dec("1250")) {
		t.Errorf("sum of legs %s != input 1250", total)
	}
}

func TestAllocateInWaterfall_ZeroPenalty(t *testing.T) {
	l := offsetLoanRow{
		PrincipalOverdue: dec("500"),
		InterestBalance:  dec("100"),
		PenaltyBalance:   decimal.Zero,
		TotalArrears:     dec("600"),
	}
	a := allocateInWaterfall(dec("300"), l)
	if !a.Penalty.IsZero() {
		t.Errorf("expected zero penalty, got %s", a.Penalty)
	}
	if !a.Interest.Equal(dec("100")) {
		t.Errorf("interest: got %s want 100", a.Interest)
	}
	if !a.PrincipalOverdue.Equal(dec("200")) {
		t.Errorf("principal: got %s want 200", a.PrincipalOverdue)
	}
}

func TestAllocateInWaterfall_OffsetSmallerThanPenalty(t *testing.T) {
	l := offsetLoanRow{
		PrincipalOverdue: dec("1000"),
		InterestBalance:  dec("500"),
		PenaltyBalance:   dec("100"),
		TotalArrears:     dec("1600"),
	}
	a := allocateInWaterfall(dec("30"), l)
	if !a.Penalty.Equal(dec("30")) {
		t.Errorf("penalty: got %s want 30 (offset smaller than penalty cap)", a.Penalty)
	}
	if !a.Interest.IsZero() || !a.PrincipalOverdue.IsZero() {
		t.Errorf("expected interest+principal both zero, got %+v", a)
	}
}

func TestDecimalMin(t *testing.T) {
	if !decimalMin(dec("5"), dec("3")).Equal(dec("3")) {
		t.Errorf("decimalMin(5,3) wrong")
	}
	if !decimalMin(dec("-1"), dec("0")).Equal(dec("-1")) {
		t.Errorf("decimalMin(-1,0) wrong")
	}
	if !decimalMin(dec("5"), dec("5")).Equal(dec("5")) {
		t.Errorf("decimalMin equal wrong")
	}
}
