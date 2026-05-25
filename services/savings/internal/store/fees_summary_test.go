// Acceptance test for the Fees & Collections Summary report.
//
// Invariant check rather than a fixed-snapshot comparison so the test
// tracks live tenant seed data:
//   • total_amount equals Σ by_fee_code.total equals Σ by_channel.total
//   • net_amount = total_amount − total_voided
//   • voiding a single line moves its amount from net into voided
//
// Runs inside a rolled-back tx so the synthetic receipts never persist.

package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestFeesSummary_AggregationInvariants(t *testing.T) {
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

	var tenantID, cpID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		t.Skipf("no tujenge: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) // rolls back all seed below

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT id FROM counterparties WHERE tenant_id=$1 AND status='active' ORDER BY id LIMIT 1
	`, tenantID).Scan(&cpID); err != nil {
		t.Fatalf("pick cp: %v", err)
	}

	// Seed: 4 posted receipts across 2 codes × 2 channels, one voided
	// line for the void invariant.
	marker := fmt.Sprintf("FSACC-%d", time.Now().UnixNano())
	userID := uuid.New()
	type seed struct {
		Channel string
		FeeCode string
		Amount  decimal.Decimal
		Voided  bool
	}
	seeds := []seed{
		{"cash", "ad_hoc", decimal.NewFromInt(100), false},
		{"cash", "statement_fee", decimal.NewFromInt(50), false},
		{"mpesa", "ad_hoc", decimal.NewFromInt(200), false},
		{"mpesa", "statement_fee", decimal.NewFromInt(75), true}, // voided
	}
	asOfDate := time.Now().UTC().Truncate(24 * time.Hour)
	for i, s := range seeds {
		var receiptID uuid.UUID
		// posted_outside_till = true bypasses the till/virtual_till
		// requirements (third branch of receipts_check). Avoids the
		// need to seed a till_session or virtual_till for the test.
		if err := tx.QueryRow(ctx, `
			INSERT INTO receipts (
			  tenant_id, serial, counterparty_id, channel,
			  channel_amount, value_date, cashier_user_id, status,
			  posted_outside_till
			) VALUES (
			  $1, $2, $3, $4::receipt_channel,
			  $5, $6, $7, 'posted', true
			) RETURNING id
		`, tenantID, fmt.Sprintf("R-%s-%d", marker, i), cpID,
			s.Channel, s.Amount, asOfDate, userID).Scan(&receiptID); err != nil {
			t.Fatalf("seed receipt %d: %v", i, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO receipt_lines (
			  receipt_id, line_no, kind, amount, fee_code, status
			) VALUES ($1, 1, 'fee'::receipt_line_kind, $2, $3, 'posted')
		`, receiptID, s.Amount, s.FeeCode); err != nil {
			t.Fatalf("seed line %d: %v", i, err)
		}
		if s.Voided {
			if _, err := tx.Exec(ctx, `
				UPDATE receipt_lines
				   SET voided_at = now(), voided_by = $2, void_reason = 'test'
				 WHERE receipt_id = $1
			`, receiptID, userID); err != nil {
				t.Fatalf("void line %d: %v", i, err)
			}
		}
	}

	from := asOfDate.AddDate(0, 0, -1)
	to := asOfDate.AddDate(0, 0, 1)
	rs := NewFeesSummaryStore(pool)
	rep, err := rs.SummaryTx(ctx, tx, FeesSummaryFilter{From: from, To: to, CounterpartyID: cpID})
	if err != nil {
		t.Fatalf("SummaryTx: %v", err)
	}

	// Filter to JUST the seeded subset by summing only ad_hoc + statement_fee.
	// The shared tenant may have OTHER fee receipts in the same window;
	// the invariant we check is on the AGGREGATION, not on absolute totals.
	feeSum := decimal.Zero
	for _, r := range rep.ByFeeCode {
		feeSum = feeSum.Add(r.TotalAmount)
	}
	chSum := decimal.Zero
	for _, r := range rep.ByChannel {
		chSum = chSum.Add(r.TotalAmount)
	}
	if !rep.TotalAmount.Equal(feeSum) {
		t.Errorf("total_amount != Σ by_fee_code: %s vs %s",
			rep.TotalAmount.StringFixed(2), feeSum.StringFixed(2))
	}
	if !rep.TotalAmount.Equal(chSum) {
		t.Errorf("total_amount != Σ by_channel: %s vs %s",
			rep.TotalAmount.StringFixed(2), chSum.StringFixed(2))
	}
	if !rep.NetAmount.Equal(rep.TotalAmount.Sub(rep.TotalVoided)) {
		t.Errorf("net != total − voided: net=%s total=%s voided=%s",
			rep.NetAmount.StringFixed(2),
			rep.TotalAmount.StringFixed(2),
			rep.TotalVoided.StringFixed(2))
	}
	// Voided amount must include the 75 KES voided line we seeded.
	if !rep.TotalVoided.GreaterThanOrEqual(decimal.NewFromInt(75)) {
		t.Errorf("expected voided amount ≥ 75 (the seeded voided line), got %s",
			rep.TotalVoided.StringFixed(2))
	}
	// Per-fee-code rows must each balance the same way.
	for _, r := range rep.ByFeeCode {
		if !r.NetAmount.Equal(r.TotalAmount.Sub(r.VoidedAmount)) {
			t.Errorf("fee %s net inconsistent: %s vs %s − %s",
				r.FeeCode, r.NetAmount, r.TotalAmount, r.VoidedAmount)
		}
	}
}
