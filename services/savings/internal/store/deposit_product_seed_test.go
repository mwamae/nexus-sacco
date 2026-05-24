// PR 1 (BOSA / FOSA) — integration test for the per-tenant seed in
// migration 0028. Verifies that every tenant ends up with at least
// one active BOSA product after the migration runs, and that the
// seeded product carries the defaults the spec specified
// (KES 1,000 opening, KES 500 monthly contribution, day 5,
// partial_withdrawal_allowed=false, notice_period_days=0).
//
// Skipped when DATABASE_URL is unset — same pattern as the other
// store-level integration tests. Read-only, no writes, no cleanup.

package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

func TestBOSASeedPresentPerTenant(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer pool.Close()

	// Tenants without any BOSA product would mean the migration's
	// CROSS JOIN seed missed a row. Should always be empty.
	rows, err := pool.Query(ctx, `
		SELECT t.slug
		  FROM tenants t
		 WHERE NOT EXISTS (
		   SELECT 1 FROM deposit_products dp
		    WHERE dp.tenant_id = t.id
		      AND dp.segment = 'bosa'
		      AND dp.is_active = true
		 )
	`)
	if err != nil {
		t.Fatalf("query missing-BOSA tenants: %v", err)
	}
	var missing []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			t.Fatalf("scan: %v", err)
		}
		missing = append(missing, slug)
	}
	rows.Close()
	if len(missing) > 0 {
		t.Fatalf("tenants without an active BOSA product: %v", missing)
	}

	// tujenge is the dev fixture; assert the specific defaults match
	// the migration. Other tenants may have edited their seeded MD
	// product, so we don't assert on them.
	var (
		productType   string
		minOpening    decimal.Decimal
		monthly       decimal.Decimal
		dayOfMonth    *int
		partialAllow  bool
		noticePeriod  int
	)
	err = pool.QueryRow(ctx, `
		SELECT product_type::text, min_opening_balance, required_monthly_amount,
		       required_day_of_month, partial_withdrawal_allowed, notice_period_days
		  FROM deposit_products
		 WHERE tenant_id = (SELECT id FROM tenants WHERE slug='tujenge')
		   AND segment = 'bosa'
		 ORDER BY created_at LIMIT 1
	`).Scan(&productType, &minOpening, &monthly, &dayOfMonth, &partialAllow, &noticePeriod)
	if err != nil {
		t.Fatalf("read seeded BOSA on tujenge: %v", err)
	}

	if productType != string(domain.ProductMemberDeposit) {
		t.Errorf("seeded BOSA product_type = %q, want %q", productType, domain.ProductMemberDeposit)
	}
	if !minOpening.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("seeded min_opening_balance = %s, want 1000", minOpening.String())
	}
	if !monthly.Equal(decimal.NewFromInt(500)) {
		t.Errorf("seeded required_monthly_amount = %s, want 500", monthly.String())
	}
	if dayOfMonth == nil || *dayOfMonth != 5 {
		t.Errorf("seeded required_day_of_month = %v, want 5", dayOfMonth)
	}
	if partialAllow {
		t.Error("seeded BOSA has partial_withdrawal_allowed=true; should be false")
	}
	if noticePeriod != 0 {
		t.Errorf("seeded BOSA has notice_period_days=%d; should be 0", noticePeriod)
	}
}
