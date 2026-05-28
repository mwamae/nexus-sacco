package insurance

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func TestQuotePremium_BelowMin(t *testing.T) {
	// 100,000 × 0.5% = 500, min = 1000 → premium = 1000
	got := QuotePremium(dec("100000"), dec("0.5"), dec("1000"), nil)
	if !got.Equal(dec("1000")) {
		t.Errorf("expected 1000 (min floor), got %s", got)
	}
}

func TestQuotePremium_AboveMin_NoMax(t *testing.T) {
	// 500,000 × 0.5% = 2500, no cap → 2500
	got := QuotePremium(dec("500000"), dec("0.5"), dec("100"), nil)
	if !got.Equal(dec("2500")) {
		t.Errorf("expected 2500, got %s", got)
	}
}

func TestQuotePremium_AboveMax(t *testing.T) {
	// 10,000,000 × 1% = 100,000, max = 50,000 → capped at 50,000
	max := dec("50000")
	got := QuotePremium(dec("10000000"), dec("1.0"), dec("100"), &max)
	if !got.Equal(dec("50000")) {
		t.Errorf("expected 50000 (max cap), got %s", got)
	}
}

func TestQuotePremium_FractionalRate(t *testing.T) {
	// 25,000 × 0.0500% = 12.50, no floor needed → 12.50
	got := QuotePremium(dec("25000"), dec("0.0500"), dec("0"), nil)
	if !got.Equal(dec("12.50")) {
		t.Errorf("expected 12.50, got %s", got)
	}
}
