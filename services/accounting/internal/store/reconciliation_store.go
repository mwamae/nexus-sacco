// Subledger reconciliation report.
//
// For every account that has both a GL line and a subledger source of
// truth, compute the gap. If the two diverge, something silently
// stopped posting — typically an accounting-service outage or a code
// path that bypasses the GL entirely (see R2 outbox, R3 interest run).
//
// The status thresholds mirror the spec:
//   • ok    — |delta| < 1.00 KES (round-trip rounding noise)
//   • warn  — |delta| < 0.1% of GL balance (and >= 1 KES)
//   • error — anything else
//
// Subledger queries duplicate the segment + product_type → CoA mapping
// that lives in savings's depositLiabilityCode helper. Cross-service
// Go imports aren't done in this codebase; replicating the mapping as
// a CASE expression matches how SASRA already buckets the same data.

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// ReconciliationRow — one CoA code's GL vs subledger snapshot.
type ReconciliationRow struct {
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	GLBalance       decimal.Decimal `json:"gl_balance"`
	SubledgerBalance decimal.Decimal `json:"subledger_balance"`
	Delta           decimal.Decimal `json:"delta"`
	DeltaPctOfGL    decimal.Decimal `json:"delta_pct_of_gl"`
	Status          string          `json:"status"` // ok | warn | error
	LastJEID        *uuid.UUID      `json:"last_je_id,omitempty"`
	LastJENo        string          `json:"last_je_no,omitempty"`
}

// SubledgerReconciliationReport — the full DTO + an overall_status =
// worst-individual-row so the UI can render one traffic light at the
// top and the CLI flag can exit non-zero on any error.
//
// Named `Subledger…` (not just `Reconciliation…`) to avoid colliding
// with the bank-reconciliation DTO already in this package.
type SubledgerReconciliationReport struct {
	AsOf          time.Time           `json:"as_of"`
	OverallStatus string              `json:"overall_status"` // ok | warn | error
	Rows          []ReconciliationRow `json:"rows"`
}

// ReconciliationStore is intentionally thin — this report is read-only
// and lives at a single entry point. Same pattern as ReportStore but
// kept separate so the file stays focused.
type ReconciliationStore struct{ pool *pgxpool.Pool }

func NewReconciliationStore(pool *pgxpool.Pool) *ReconciliationStore {
	return &ReconciliationStore{pool: pool}
}

// accountSpec — one row's wiring: the CoA code, the human label, the
// debit/credit polarity for the GL side, and the SQL that produces
// the subledger total scoped to the tenant + as_of date. The SQL
// MUST be parameter-safe (no string interpolation of user input).
type accountSpec struct {
	Code         string
	Name         string
	DebitNormal  bool   // true for asset accounts (1100 etc); false for liability/equity
	SubledgerSQL string // returns one numeric column
}

// Static list — the spec's 10 reconciled accounts. 2050/2052/2053/…
// BOSA codes: we iterate the actual CoA on the tenant rather than
// hard-coding (a tenant may have just 2050 or may have sub-classed
// 2052/2053; the report dynamically picks them up).
var staticAccountSpecs = []accountSpec{
	{
		Code: "2000", Name: "Ordinary Savings Deposits", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.segment = 'fosa'
			   AND dp.product_type = 'ordinary'
		`,
	},
	{
		Code: "2010", Name: "Holiday Savings", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.segment = 'fosa' AND dp.product_type = 'holiday'
		`,
	},
	{
		Code: "2020", Name: "Emergency Savings", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.segment = 'fosa' AND dp.product_type = 'emergency'
		`,
	},
	{
		Code: "2030", Name: "Goal Savings", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.segment = 'fosa' AND dp.product_type = 'goal'
		`,
	},
	{
		Code: "2040", Name: "Junior Savings", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.segment = 'fosa' AND dp.product_type = 'junior'
		`,
	},
	{
		Code: "2050", Name: "Member Deposits (BOSA)", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.segment = 'bosa'
		`,
	},
	{
		Code: "2100", Name: "Fixed Deposits", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(da.current_balance), 0)
			  FROM deposit_accounts da
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE da.status != 'closed'
			   AND dp.product_type = 'fixed'
		`,
	},
	{
		Code: "1100", Name: "Member Loans Receivable", DebitNormal: true,
		SubledgerSQL: `
			SELECT COALESCE(SUM(principal_balance), 0)
			  FROM loans
			 WHERE status IN ('active', 'in_arrears', 'restructured')
		`,
	},
	{
		Code: "1110", Name: "Loan Interest Receivable", DebitNormal: true,
		SubledgerSQL: `
			SELECT COALESCE(SUM(interest_balance), 0)
			  FROM loans
			 WHERE status IN ('active', 'in_arrears', 'restructured')
		`,
	},
	{
		Code: "1120", Name: "Loan Loss Provision", DebitNormal: false,
		// Provision is the most-recent POSTED run's total. NULL when
		// no provisioning run has ever been posted on this tenant —
		// treat as 0 so the report doesn't crash for new SACCOs.
		SubledgerSQL: `
			SELECT COALESCE(
			  (SELECT total_provision FROM provision_runs
			    WHERE status = 'posted'
			    ORDER BY as_of_date DESC LIMIT 1),
			  0)
		`,
	},
	{
		Code: "3000", Name: "Member Share Capital", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(shares_held * par_value_at_open), 0)
			  FROM share_accounts
			 WHERE status = 'active'
		`,
	},
	{
		Code: "2200", Name: "Withholding Tax Payable", DebitNormal: false,
		SubledgerSQL: `
			SELECT COALESCE(SUM(wht_amount), 0)
			  FROM tax_payable_ledger
			 WHERE remitted_at IS NULL
		`,
	},
}

// ReconciliationTx returns the per-account snapshot as of asOf.
//
// For each accountSpec:
//   1. Look up the CoA row (id + name); if missing on this tenant,
//      skip the row (a SACCO that hasn't enabled e.g. junior savings
//      legitimately has no 2040 account).
//   2. Sum journal_lines net for the account through asOf.
//   3. Run the spec's subledger query.
//   4. Compute delta + status; skip rows where both are zero (keeps
//      the report tight for new tenants).
//   5. Pull the most-recent JE id on the account for the investigate
//      drill-in.
func (s *ReconciliationStore) ReconciliationTx(ctx context.Context, tx pgx.Tx, asOf time.Time) (*SubledgerReconciliationReport, error) {
	out := &SubledgerReconciliationReport{AsOf: asOf, OverallStatus: "ok"}
	for _, spec := range staticAccountSpecs {
		// CoA lookup. Missing → skip.
		var acctID uuid.UUID
		var name string
		err := tx.QueryRow(ctx, `
			SELECT id, name FROM chart_of_accounts
			 WHERE tenant_id = current_tenant_id()
			   AND code = $1 AND is_active = true
		`, spec.Code).Scan(&acctID, &name)
		if err != nil {
			if isNoRows(err) {
				continue
			}
			return nil, fmt.Errorf("CoA lookup %s: %w", spec.Code, err)
		}

		// GL side — journal_lines net through asOf. Normalised to the
		// account's natural polarity so the delta math works
		// regardless of asset/liability/equity.
		var gl decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(
			  CASE WHEN $2 THEN l.debit - l.credit
			       ELSE l.credit - l.debit
			  END
			), 0)
			  FROM journal_lines l
			  JOIN journal_entries je ON je.id = l.entry_id
			 WHERE l.account_id = $1
			   AND je.status = 'posted'
			   AND je.entry_date <= $3
		`, acctID, spec.DebitNormal, asOf).Scan(&gl); err != nil {
			return nil, fmt.Errorf("GL sum %s: %w", spec.Code, err)
		}

		// Subledger side.
		var sub decimal.Decimal
		if err := tx.QueryRow(ctx, spec.SubledgerSQL).Scan(&sub); err != nil {
			return nil, fmt.Errorf("subledger %s: %w", spec.Code, err)
		}

		if gl.IsZero() && sub.IsZero() {
			continue
		}

		delta := gl.Sub(sub).Abs()
		var pct decimal.Decimal
		if !gl.IsZero() {
			pct = delta.Mul(decimal.NewFromInt(100)).Div(gl.Abs()).Round(4)
		}

		// Most-recent JE on this account — investigation starting
		// point, harmless if NULL.
		var lastJEID *uuid.UUID
		var lastJENo string
		_ = tx.QueryRow(ctx, `
			SELECT je.id, je.entry_no
			  FROM journal_lines l
			  JOIN journal_entries je ON je.id = l.entry_id
			 WHERE l.account_id = $1
			   AND je.status = 'posted'
			 ORDER BY je.entry_date DESC, je.created_at DESC
			 LIMIT 1
		`, acctID).Scan(&lastJEID, &lastJENo)

		status := classify(delta, gl)
		if worseStatus(out.OverallStatus, status) == status {
			out.OverallStatus = status
		}

		out.Rows = append(out.Rows, ReconciliationRow{
			Code: spec.Code, Name: name,
			GLBalance: gl, SubledgerBalance: sub,
			Delta: delta, DeltaPctOfGL: pct,
			Status:   status,
			LastJEID: lastJEID, LastJENo: lastJENo,
		})
	}
	return out, nil
}

// classify applies the spec's thresholds.
func classify(delta, gl decimal.Decimal) string {
	if delta.LessThan(decimal.NewFromFloat(1.0)) {
		return "ok"
	}
	if gl.IsZero() {
		// Any non-trivial delta on a zero-GL account is error — there's
		// nothing to compute a percentage against.
		return "error"
	}
	pct := delta.Mul(decimal.NewFromInt(100)).Div(gl.Abs())
	if pct.LessThan(decimal.NewFromFloat(0.1)) {
		return "warn"
	}
	return "error"
}

// worseStatus picks the more severe of two statuses, used to roll up
// the overall_status to the worst individual row.
func worseStatus(a, b string) string {
	rank := func(s string) int {
		switch s {
		case "error":
			return 2
		case "warn":
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// isNoRows wraps pgx.ErrNoRows — keeps the call sites tidy when only
// the no-row branch is interesting.
func isNoRows(err error) bool {
	return err == pgx.ErrNoRows
}
