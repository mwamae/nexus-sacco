// Loans Phase 2 — additional reporting queries.
//
// Lives in its own file so the original loan_reports_store.go stays
// untouched (Phase 2 is purely additive — no business-logic change).
//
// Phase 3 wiring (S7): DPD now prefers the loan_dpd_snapshots row
// written by the dpd-classifier worker, falling back to the Phase 1
// inline proxy (CURRENT_DATE - next_installment_due_at) when no
// snapshot exists for the loan yet. The fallback keeps reports
// useful in dev environments where the classifier hasn't run, and
// during the seed phase right after migration 0039.
//
// `SnapshotMeta` (returned alongside the PAR + Aging payloads) lets
// the UI banner stale data when the most recent snapshot is older
// than 1 day — that's the "verification gate" the prompt calls for.

package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// dpdResolverCTE — reusable CTE prefix that exposes a per-loan
// dpd_days column resolved from the latest snapshot. Queries that
// already include their own WITH clause prepend this CTE.
const dpdResolverCTE = `
  latest_snap AS (
    SELECT DISTINCT ON (loan_id) loan_id, dpd_days
      FROM loan_dpd_snapshots
     ORDER BY loan_id, snapshot_date DESC
  )`

// dpdExpr — the SQL expression to use in SELECT lists for the
// resolved DPD. Requires the surrounding query to JOIN to
// latest_snap LEFT JOIN ls ON ls.loan_id = l.id.
const dpdExpr = `COALESCE(ls.dpd_days, GREATEST(0, (CURRENT_DATE - l.next_installment_due_at))::int)`

// SnapshotMeta surfaces the freshness of loan_dpd_snapshots so the
// UI can render a "data is N days stale" banner. Returned alongside
// every Phase 3-wired report.
type SnapshotMeta struct {
	Available           bool       `json:"available"`             // false = no snapshots at all
	LatestSnapshotDate  *time.Time `json:"latest_snapshot_date,omitempty"`
	StalenessDays       int        `json:"staleness_days"`         // 0 = today's snapshot
	LoansWithSnapshots  int        `json:"loans_with_snapshots"`
	LoansWithoutSnapshots int      `json:"loans_without_snapshots"`
}

// LoadSnapshotMetaTx populates SnapshotMeta for the current tenant.
// The caller already opened tx with set_config('app.tenant_id', …)
// so RLS scopes the counts correctly.
func (s *LoanReportsStore) LoadSnapshotMetaTx(ctx context.Context, tx pgx.Tx) (*SnapshotMeta, error) {
	meta := &SnapshotMeta{}
	var latest *time.Time
	if err := tx.QueryRow(ctx, `SELECT MAX(snapshot_date) FROM loan_dpd_snapshots`).Scan(&latest); err != nil {
		return nil, err
	}
	if latest == nil {
		// No snapshots at all — pure fallback mode.
		return meta, nil
	}
	meta.Available = true
	meta.LatestSnapshotDate = latest
	today := time.Now().UTC().Truncate(24 * time.Hour)
	meta.StalenessDays = int(today.Sub(*latest).Hours() / 24)
	if meta.StalenessDays < 0 {
		meta.StalenessDays = 0
	}
	// Counts based on loans that ought to have a snapshot
	// (active/in_arrears/restructured with positive principal).
	if err := tx.QueryRow(ctx, `
		WITH eligible AS (
		  SELECT id FROM loans
		   WHERE status IN ('active','in_arrears','restructured')
		     AND principal_balance > 0
		)
		SELECT
		  (SELECT count(*) FROM eligible e
		    WHERE EXISTS (SELECT 1 FROM loan_dpd_snapshots s WHERE s.loan_id = e.id)),
		  (SELECT count(*) FROM eligible e
		    WHERE NOT EXISTS (SELECT 1 FROM loan_dpd_snapshots s WHERE s.loan_id = e.id))
	`).Scan(&meta.LoansWithSnapshots, &meta.LoansWithoutSnapshots); err != nil {
		return nil, err
	}
	return meta, nil
}

// ─────────── PAR ───────────

type PARSummary struct {
	AsOf             time.Time       `json:"as_of"`
	TotalPrincipal   string          `json:"total_principal"`
	Par1Principal    string          `json:"par_1_principal"`
	Par30Principal   string          `json:"par_30_principal"`
	Par90Principal   string          `json:"par_90_principal"`
	Par1Pct          string          `json:"par_1"`
	Par30Pct         string          `json:"par_30"`
	Par90Pct         string          `json:"par_90"`
	TotalOutstanding string          `json:"total_outstanding"`
	ByProduct        []PARProductRow `json:"by_product"`
	Snapshot         *SnapshotMeta   `json:"snapshot,omitempty"`
}

type PARProductRow struct {
	ProductID      uuid.UUID `json:"product_id"`
	ProductName    string    `json:"product_name"`
	TotalPrincipal string    `json:"total_principal"`
	Par30Principal string    `json:"par_30_principal"`
	Par30Pct       string    `json:"par_30"`
}

func (s *LoanReportsStore) PARTx(ctx context.Context, tx pgx.Tx) (*PARSummary, error) {
	out := &PARSummary{AsOf: time.Now().UTC()}
	err := tx.QueryRow(ctx, `
		WITH `+dpdResolverCTE+`, active AS (
		  SELECT l.principal_balance,
		         (l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance) AS total_outstanding,
		         `+dpdExpr+` AS dpd
		    FROM loans l
		    LEFT JOIN latest_snap ls ON ls.loan_id = l.id
		   WHERE l.status IN ('active','in_arrears','restructured')
		     AND l.principal_balance > 0
		), totals AS (
		  SELECT COALESCE(SUM(principal_balance),0)::numeric AS total_p,
		         COALESCE(SUM(total_outstanding),0)::numeric AS total_o,
		         COALESCE(SUM(CASE WHEN dpd >= 1   THEN principal_balance ELSE 0 END),0)::numeric AS p1,
		         COALESCE(SUM(CASE WHEN dpd >= 30  THEN principal_balance ELSE 0 END),0)::numeric AS p30,
		         COALESCE(SUM(CASE WHEN dpd >= 90  THEN principal_balance ELSE 0 END),0)::numeric AS p90
		    FROM active
		)
		SELECT
		  total_p::text, total_o::text, p1::text, p30::text, p90::text,
		  CASE WHEN total_p > 0 THEN (p1  * 100.0 / total_p)::numeric(8,4)::text ELSE '0' END,
		  CASE WHEN total_p > 0 THEN (p30 * 100.0 / total_p)::numeric(8,4)::text ELSE '0' END,
		  CASE WHEN total_p > 0 THEN (p90 * 100.0 / total_p)::numeric(8,4)::text ELSE '0' END
		FROM totals
	`).Scan(
		&out.TotalPrincipal, &out.TotalOutstanding,
		&out.Par1Principal, &out.Par30Principal, &out.Par90Principal,
		&out.Par1Pct, &out.Par30Pct, &out.Par90Pct,
	)
	if err != nil {
		return nil, fmt.Errorf("par totals: %w", err)
	}

	rows, err := tx.Query(ctx, `
		WITH `+dpdResolverCTE+`, active AS (
		  SELECT l.product_id, l.principal_balance,
		         `+dpdExpr+` AS dpd
		    FROM loans l
		    LEFT JOIN latest_snap ls ON ls.loan_id = l.id
		   WHERE l.status IN ('active','in_arrears','restructured')
		     AND l.principal_balance > 0
		)
		SELECT a.product_id, p.name,
		       COALESCE(SUM(a.principal_balance),0)::text,
		       COALESCE(SUM(CASE WHEN a.dpd >= 30 THEN a.principal_balance ELSE 0 END),0)::text,
		       CASE WHEN SUM(a.principal_balance) > 0
		            THEN (SUM(CASE WHEN a.dpd >= 30 THEN a.principal_balance ELSE 0 END) * 100.0 / SUM(a.principal_balance))::numeric(8,4)::text
		            ELSE '0' END
		  FROM active a JOIN loan_products p ON p.id = a.product_id
		 GROUP BY a.product_id, p.name
		 ORDER BY SUM(a.principal_balance) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r PARProductRow
		if err := rows.Scan(&r.ProductID, &r.ProductName, &r.TotalPrincipal, &r.Par30Principal, &r.Par30Pct); err != nil {
			return nil, err
		}
		out.ByProduct = append(out.ByProduct, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Snapshot staleness metadata — drives the "data is N days stale" banner.
	meta, err := s.LoadSnapshotMetaTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("snapshot meta: %w", err)
	}
	out.Snapshot = meta
	return out, nil
}

// ─────────── Aging buckets ───────────

type AgingBucketRow struct {
	Label     string `json:"label"`
	DPDMin    int    `json:"dpd_min"`
	DPDMax    *int   `json:"dpd_max,omitempty"` // nil = open-ended
	Count     int    `json:"count"`
	Principal string `json:"principal"`
	Interest  string `json:"interest"`
	Penalty   string `json:"penalty"`
	Total     string `json:"total"`
}

type AgingBucketsReport struct {
	AsOf     time.Time        `json:"as_of"`
	Buckets  []AgingBucketRow `json:"buckets"`
	Snapshot *SnapshotMeta    `json:"snapshot,omitempty"`
}

func (s *LoanReportsStore) AgingBucketsTx(ctx context.Context, tx pgx.Tx) (*AgingBucketsReport, error) {
	out := &AgingBucketsReport{AsOf: time.Now().UTC()}
	defs := []struct {
		label  string
		lo, hi int // hi == -1 means open-ended
	}{
		{"Current (0)", 0, 0},
		{"1-30 days", 1, 30},
		{"31-60 days", 31, 60},
		{"61-90 days", 61, 90},
		{"91-180 days", 91, 180},
		{"180+ days", 181, -1},
	}
	for _, d := range defs {
		var n int
		var princ, intr, pen, tot string
		var query string
		var args []any
		if d.hi == -1 {
			query = `
				WITH ` + dpdResolverCTE + `
				SELECT count(*),
				       COALESCE(SUM(l.principal_balance),0)::text,
				       COALESCE(SUM(l.interest_balance),0)::text,
				       COALESCE(SUM(l.penalty_balance),0)::text,
				       COALESCE(SUM(l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance),0)::text
				  FROM loans l
				  LEFT JOIN latest_snap ls ON ls.loan_id = l.id
				 WHERE l.status IN ('active','in_arrears','restructured')
				   AND ` + dpdExpr + ` >= $1
			`
			args = []any{d.lo}
		} else {
			query = `
				WITH ` + dpdResolverCTE + `
				SELECT count(*),
				       COALESCE(SUM(l.principal_balance),0)::text,
				       COALESCE(SUM(l.interest_balance),0)::text,
				       COALESCE(SUM(l.penalty_balance),0)::text,
				       COALESCE(SUM(l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance),0)::text
				  FROM loans l
				  LEFT JOIN latest_snap ls ON ls.loan_id = l.id
				 WHERE l.status IN ('active','in_arrears','restructured')
				   AND ` + dpdExpr + ` BETWEEN $1 AND $2
			`
			args = []any{d.lo, d.hi}
		}
		if err := tx.QueryRow(ctx, query, args...).Scan(&n, &princ, &intr, &pen, &tot); err != nil {
			return nil, fmt.Errorf("aging bucket %s: %w", d.label, err)
		}
		row := AgingBucketRow{Label: d.label, DPDMin: d.lo, Count: n, Principal: princ, Interest: intr, Penalty: pen, Total: tot}
		if d.hi != -1 {
			hi := d.hi
			row.DPDMax = &hi
		}
		out.Buckets = append(out.Buckets, row)
	}
	meta, err := s.LoadSnapshotMetaTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("snapshot meta: %w", err)
	}
	out.Snapshot = meta
	return out, nil
}

// ─────────── Vintage cohorts ───────────

type VintagePoint struct {
	MonthsOnBook int    `json:"months_on_book"`
	Par30Pct     string `json:"par_30"`
	Par90Pct     string `json:"par_90"`
	WriteOffPct  string `json:"write_off"`
}

type VintageCohort struct {
	DisbursementMonth string         `json:"disbursement_month"` // YYYY-MM
	DisbursedCount    int            `json:"disbursed_count"`
	DisbursedAmount   string         `json:"disbursed_amount"`
	Performance       []VintagePoint `json:"performance"`
}

type VintageReport struct {
	AsOf    time.Time       `json:"as_of"`
	Cohorts []VintageCohort `json:"cohorts"`
}

// VintageTx computes performance at fixed months-on-book checkpoints
// (3, 6, 12, 18, 24) for each disbursement-month cohort within the
// from/to range (inclusive). Each cohort row shows the cohort's
// current par30/par90/write-off rates, computed against today.
//
// Phase 1 proxy: the per-checkpoint metric is computed against the
// CURRENT loan state (not the state at the historical checkpoint).
// Phase 3's snapshot history adds per-day historical reconstruction
// for a true "at month 6 on book" measurement.
func (s *LoanReportsStore) VintageTx(ctx context.Context, tx pgx.Tx, from, to time.Time) (*VintageReport, error) {
	out := &VintageReport{AsOf: time.Now().UTC()}
	// 1. Cohort totals.
	rows, err := tx.Query(ctx, `
		SELECT to_char(date_trunc('month', disbursed_at), 'YYYY-MM') AS month_label,
		       count(*),
		       COALESCE(SUM(principal),0)::text
		  FROM loans
		 WHERE disbursed_at IS NOT NULL
		   AND disbursed_at >= $1
		   AND disbursed_at <  $2
		 GROUP BY date_trunc('month', disbursed_at)
		 ORDER BY date_trunc('month', disbursed_at)
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("vintage cohorts: %w", err)
	}
	defer rows.Close()
	cohortIdx := map[string]int{}
	for rows.Next() {
		var c VintageCohort
		if err := rows.Scan(&c.DisbursementMonth, &c.DisbursedCount, &c.DisbursedAmount); err != nil {
			return nil, err
		}
		cohortIdx[c.DisbursementMonth] = len(out.Cohorts)
		out.Cohorts = append(out.Cohorts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 2. Per-checkpoint metrics. One query per (cohort, checkpoint) is
	// expensive but each cohort × 5 checkpoints is small (~20 cohorts
	// × 5 = 100 round-trips for a year's worth of data); cached for
	// 1h at the handler layer.
	checkpoints := []int{3, 6, 12, 18, 24}
	for ci := range out.Cohorts {
		cm := out.Cohorts[ci].DisbursementMonth
		for _, mo := range checkpoints {
			var par30Pct, par90Pct, woPct string
			err := tx.QueryRow(ctx, `
				WITH cohort_loans AS (
				  SELECT id, principal, status,
				         GREATEST(0, (CURRENT_DATE - next_installment_due_at))::int AS dpd,
				         ((CURRENT_DATE - disbursed_at::date) / 30)::int AS months_on_book
				    FROM loans
				   WHERE to_char(date_trunc('month', disbursed_at), 'YYYY-MM') = $1
				)
				SELECT
				  COALESCE(CASE WHEN count(*) > 0
				    THEN (count(*) FILTER (WHERE dpd >= 30 AND months_on_book >= $2) * 100.0 / count(*))::numeric(8,4)
				    ELSE 0 END,0)::text,
				  COALESCE(CASE WHEN count(*) > 0
				    THEN (count(*) FILTER (WHERE dpd >= 90 AND months_on_book >= $2) * 100.0 / count(*))::numeric(8,4)
				    ELSE 0 END,0)::text,
				  COALESCE(CASE WHEN count(*) > 0
				    THEN (count(*) FILTER (WHERE status = 'written_off' AND months_on_book >= $2) * 100.0 / count(*))::numeric(8,4)
				    ELSE 0 END,0)::text
				FROM cohort_loans
			`, cm, mo).Scan(&par30Pct, &par90Pct, &woPct)
			if err != nil {
				return nil, fmt.Errorf("vintage checkpoint %s mo=%d: %w", cm, mo, err)
			}
			out.Cohorts[ci].Performance = append(out.Cohorts[ci].Performance, VintagePoint{
				MonthsOnBook: mo,
				Par30Pct:     par30Pct,
				Par90Pct:     par90Pct,
				WriteOffPct:  woPct,
			})
		}
	}
	_ = cohortIdx
	return out, nil
}

// ─────────── Officer leaderboard ───────────

type OfficerRow struct {
	UserID          uuid.UUID `json:"user_id"`
	UserName        string    `json:"user_name"`
	DisbursedCount  int       `json:"disbursed_count"`
	DisbursedAmount string    `json:"disbursed_amount"`
	CollectedAmount string    `json:"collected_amount"`
	Par30Pct        string    `json:"par_30"`
	WriteOffAmount  string    `json:"write_off_amount"`
}

func (s *LoanReportsStore) OfficersTx(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]OfficerRow, error) {
	rows, err := tx.Query(ctx, `
		WITH disb AS (
		  SELECT l.disbursed_by AS user_id,
		         count(*)                AS disbursed_count,
		         COALESCE(SUM(l.principal),0)::numeric AS disbursed_amount,
		         COALESCE(SUM(CASE WHEN GREATEST(0,(CURRENT_DATE - l.next_installment_due_at))::int >= 30
		                            AND l.status IN ('active','in_arrears','restructured')
		                       THEN l.principal_balance ELSE 0 END),0)::numeric AS par30_p,
		         COALESCE(SUM(CASE WHEN l.status IN ('active','in_arrears','restructured')
		                       THEN l.principal_balance ELSE 0 END),0)::numeric AS active_p,
		         COALESCE(SUM(CASE WHEN l.status = 'written_off' THEN l.principal ELSE 0 END),0)::numeric AS wo_amount
		    FROM loans l
		   WHERE l.disbursed_by IS NOT NULL
		     AND l.disbursed_at BETWEEN $1 AND $2
		   GROUP BY l.disbursed_by
		), coll AS (
		  SELECT t.initiated_by AS user_id,
		         COALESCE(SUM(t.amount),0)::numeric AS collected_amount
		    FROM loan_transactions t
		   WHERE t.txn_type = 'repayment'
		     AND t.posted_at BETWEEN $1 AND $2
		   GROUP BY t.initiated_by
		), agg AS (
		  SELECT u_id,
		         max(disbursed_count) AS dc, max(disbursed_amount) AS da,
		         max(collected_amount) AS ca, max(par30_p) AS p30, max(active_p) AS ap,
		         max(wo_amount) AS wo
		    FROM (
		      SELECT user_id AS u_id, disbursed_count, disbursed_amount, 0::numeric AS collected_amount, par30_p, active_p, wo_amount FROM disb
		      UNION ALL
		      SELECT user_id AS u_id, 0, 0::numeric, collected_amount, 0::numeric, 0::numeric, 0::numeric FROM coll
		    ) z
		   WHERE u_id IS NOT NULL
		   GROUP BY u_id
		)
		SELECT a.u_id, COALESCE(u.full_name, '(unknown)'),
		       a.dc::int,
		       a.da::text, a.ca::text,
		       CASE WHEN a.ap > 0 THEN (a.p30 * 100.0 / a.ap)::numeric(8,4)::text ELSE '0' END,
		       a.wo::text
		  FROM agg a
		  LEFT JOIN users u ON u.id = a.u_id
		 ORDER BY a.da DESC NULLS LAST
		 LIMIT 100
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("officers: %w", err)
	}
	defer rows.Close()
	var out []OfficerRow
	for rows.Next() {
		var r OfficerRow
		if err := rows.Scan(&r.UserID, &r.UserName, &r.DisbursedCount, &r.DisbursedAmount, &r.CollectedAmount, &r.Par30Pct, &r.WriteOffAmount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── Disbursement register ───────────

type DisbursementRow struct {
	LoanID      uuid.UUID `json:"loan_id"`
	LoanNo      string    `json:"loan_no"`
	MemberName  string    `json:"member_name"`
	MemberNo    string    `json:"member_no"`
	Product     string    `json:"product"`
	Amount      string    `json:"amount"`
	Channel     *string   `json:"channel"`
	DisbursedAt time.Time `json:"disbursed_at"`
	OfficerName string    `json:"officer"`
}

func (s *LoanReportsStore) DisbursementsTx(ctx context.Context, tx pgx.Tx, from, to time.Time, productID *uuid.UUID, channel string, limit, offset int) ([]DisbursementRow, int, error) {
	if limit <= 0 {
		limit = 100
	}
	args := []any{from, to}
	cond := ""
	if productID != nil {
		args = append(args, *productID)
		cond += fmt.Sprintf(" AND l.product_id = $%d", len(args))
	}
	if channel != "" {
		args = append(args, channel)
		cond += fmt.Sprintf(" AND l.disbursement_channel = $%d", len(args))
	}

	var total int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM loans l
		 WHERE l.disbursed_at BETWEEN $1 AND $2 `+cond, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("disbursements count: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := tx.Query(ctx, `
		SELECT l.id, l.loan_no,
		       cd.full_name, cd.cp_number,
		       p.name,
		       l.principal::text, l.disbursement_channel,
		       l.disbursed_at,
		       COALESCE(u.full_name, '(unknown)')
		  FROM loans l
		  JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
		  JOIN loan_products p ON p.id = l.product_id
		  LEFT JOIN users u ON u.id = l.disbursed_by
		 WHERE l.disbursed_at BETWEEN $1 AND $2 `+cond+`
		 ORDER BY l.disbursed_at DESC
		 LIMIT $`+fmt.Sprintf("%d", len(args)-1)+` OFFSET $`+fmt.Sprintf("%d", len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("disbursements rows: %w", err)
	}
	defer rows.Close()
	var out []DisbursementRow
	for rows.Next() {
		var r DisbursementRow
		if err := rows.Scan(&r.LoanID, &r.LoanNo, &r.MemberName, &r.MemberNo, &r.Product, &r.Amount, &r.Channel, &r.DisbursedAt, &r.OfficerName); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ─────────── Repayment register ───────────

type RepaymentRow struct {
	TxnID       uuid.UUID `json:"txn_id"`
	LoanID      uuid.UUID `json:"loan_id"`
	LoanNo      string    `json:"loan_no"`
	MemberName  string    `json:"member_name"`
	Amount      string    `json:"amount"`
	Channel     *string   `json:"channel"`
	Principal   string    `json:"principal"`
	Interest    string    `json:"interest"`
	Fees        string    `json:"fees"`
	Penalty     string    `json:"penalty"`
	PostedAt    time.Time `json:"posted_at"`
	OfficerName string    `json:"officer"`
}

func (s *LoanReportsStore) RepaymentsTx(ctx context.Context, tx pgx.Tx, from, to time.Time, productID *uuid.UUID, channel string, limit, offset int) ([]RepaymentRow, int, error) {
	if limit <= 0 {
		limit = 100
	}
	args := []any{from, to}
	cond := ""
	if productID != nil {
		args = append(args, *productID)
		cond += fmt.Sprintf(" AND l.product_id = $%d", len(args))
	}
	if channel != "" {
		args = append(args, channel)
		cond += fmt.Sprintf(" AND t.channel = $%d", len(args))
	}

	var total int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM loan_transactions t
		  JOIN loans l ON l.id = t.loan_id
		 WHERE t.txn_type = 'repayment'
		   AND t.posted_at BETWEEN $1 AND $2 `+cond, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("repayments count: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := tx.Query(ctx, `
		SELECT t.id, t.loan_id, l.loan_no,
		       cd.full_name,
		       t.amount::text, t.channel,
		       t.principal_component::text, t.interest_component::text,
		       t.fee_component::text, t.penalty_component::text,
		       t.posted_at,
		       COALESCE(u.full_name, '(unknown)')
		  FROM loan_transactions t
		  JOIN loans l ON l.id = t.loan_id
		  JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
		  LEFT JOIN users u ON u.id = t.initiated_by
		 WHERE t.txn_type = 'repayment'
		   AND t.posted_at BETWEEN $1 AND $2 `+cond+`
		 ORDER BY t.posted_at DESC
		 LIMIT $`+fmt.Sprintf("%d", len(args)-1)+` OFFSET $`+fmt.Sprintf("%d", len(args)),
		args...)
	if err != nil {
		return nil, 0, fmt.Errorf("repayments rows: %w", err)
	}
	defer rows.Close()
	var out []RepaymentRow
	for rows.Next() {
		var r RepaymentRow
		if err := rows.Scan(&r.TxnID, &r.LoanID, &r.LoanNo, &r.MemberName, &r.Amount, &r.Channel,
			&r.Principal, &r.Interest, &r.Fees, &r.Penalty, &r.PostedAt, &r.OfficerName); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ─────────── Top-N borrowers ───────────

type TopNRow struct {
	MemberID   uuid.UUID `json:"member_id"`
	MemberNo   string    `json:"member_no"`
	MemberName string    `json:"member_name"`
	Value      string    `json:"value"`
}

// TopNTx supports metric in {"outstanding","disbursed","collected"}.
// "outstanding" sums current balances on active loans.
// "disbursed" sums principal across all loans for the member.
// "collected" sums loan_transactions.amount of type=repayment.
func (s *LoanReportsStore) TopNTx(ctx context.Context, tx pgx.Tx, metric string, limit int) ([]TopNRow, error) {
	if limit <= 0 {
		limit = 50
	}
	var query string
	switch metric {
	case "outstanding":
		query = `
			SELECT cd.counterparty_id, cd.cp_number, cd.full_name,
			       SUM(l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance)::text
			  FROM loans l
			  JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
			 WHERE l.status IN ('active','in_arrears','restructured')
			 GROUP BY cd.counterparty_id, cd.cp_number, cd.full_name
			 ORDER BY SUM(l.principal_balance + l.interest_balance + l.fees_balance + l.penalty_balance) DESC
			 LIMIT $1`
	case "disbursed":
		query = `
			SELECT cd.counterparty_id, cd.cp_number, cd.full_name,
			       SUM(l.principal)::text
			  FROM loans l
			  JOIN counterparty_directory cd ON cd.counterparty_id = l.counterparty_id
			 WHERE l.disbursed_at IS NOT NULL
			 GROUP BY cd.counterparty_id, cd.cp_number, cd.full_name
			 ORDER BY SUM(l.principal) DESC
			 LIMIT $1`
	case "collected":
		query = `
			SELECT cd.counterparty_id, cd.cp_number, cd.full_name,
			       SUM(t.amount)::text
			  FROM loan_transactions t
			  JOIN counterparty_directory cd ON cd.counterparty_id = t.counterparty_id
			 WHERE t.txn_type = 'repayment'
			 GROUP BY cd.counterparty_id, cd.cp_number, cd.full_name
			 ORDER BY SUM(t.amount) DESC
			 LIMIT $1`
	default:
		return nil, fmt.Errorf("unsupported top-n metric: %s", metric)
	}
	rows, err := tx.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("top-n: %w", err)
	}
	defer rows.Close()
	var out []TopNRow
	for rows.Next() {
		var r TopNRow
		if err := rows.Scan(&r.MemberID, &r.MemberNo, &r.MemberName, &r.Value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── Guarantor exposure ───────────

type GuarantorExposureRow struct {
	GuarantorMemberID uuid.UUID `json:"guarantor_member_id"`
	GuarantorName     string    `json:"guarantor_name"`
	GuarantorNo       string    `json:"guarantor_no"`
	TotalGuaranteed   string    `json:"total_guaranteed"`
	ActiveCount       int       `json:"active_count"`
}

func (s *LoanReportsStore) GuarantorExposureTx(ctx context.Context, tx pgx.Tx, memberID *uuid.UUID, limit int) ([]GuarantorExposureRow, error) {
	if limit <= 0 {
		limit = 50
	}
	args := []any{}
	cond := ""
	if memberID != nil {
		args = append(args, *memberID)
		cond = " WHERE g.guarantor_member_id = $1 "
	}
	args = append(args, limit)
	rows, err := tx.Query(ctx, `
		SELECT g.guarantor_member_id,
		       COALESCE(cd.full_name, '(unknown)'),
		       COALESCE(cd.cp_number, ''),
		       COALESCE(SUM(g.amount_guaranteed),0)::text,
		       count(*) FILTER (WHERE g.status = 'accepted')
		  FROM loan_guarantees g
		  LEFT JOIN counterparty_directory cd ON cd.counterparty_id = g.guarantor_member_id
		  `+cond+`
		 GROUP BY g.guarantor_member_id, cd.full_name, cd.cp_number
		 ORDER BY SUM(g.amount_guaranteed) DESC NULLS LAST
		 LIMIT $`+fmt.Sprintf("%d", len(args)),
		args...)
	if err != nil {
		return nil, fmt.Errorf("guarantor-exposure: %w", err)
	}
	defer rows.Close()
	var out []GuarantorExposureRow
	for rows.Next() {
		var r GuarantorExposureRow
		if err := rows.Scan(&r.GuarantorMemberID, &r.GuarantorName, &r.GuarantorNo, &r.TotalGuaranteed, &r.ActiveCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── Trend endpoints (read from loan_portfolio_snapshots) ───────────

type SnapshotPoint struct {
	SnapshotDate     string `json:"snapshot_date"` // YYYY-MM-DD
	TotalPrincipal   string `json:"total_principal"`
	TotalOutstanding string `json:"total_outstanding"`
	Par1Principal    string `json:"par_1_principal"`
	Par30Principal   string `json:"par_30_principal"`
	Par90Principal   string `json:"par_90_principal"`
	Par1Pct          string `json:"par_1"`
	Par30Pct         string `json:"par_30"`
	Par90Pct         string `json:"par_90"`
	ActiveCount      int    `json:"active_count"`
}

func (s *LoanReportsStore) PARHistoryTx(ctx context.Context, tx pgx.Tx, days int) ([]SnapshotPoint, error) {
	if days <= 0 || days > 365 {
		days = 90
	}
	rows, err := tx.Query(ctx, `
		SELECT to_char(snapshot_date, 'YYYY-MM-DD'),
		       total_principal::text,
		       (total_principal + total_interest + total_fees + total_penalty)::text,
		       par1_principal::text, par30_principal::text, par90_principal::text,
		       CASE WHEN total_principal > 0 THEN (par1_principal  * 100.0 / total_principal)::numeric(8,4)::text ELSE '0' END,
		       CASE WHEN total_principal > 0 THEN (par30_principal * 100.0 / total_principal)::numeric(8,4)::text ELSE '0' END,
		       CASE WHEN total_principal > 0 THEN (par90_principal * 100.0 / total_principal)::numeric(8,4)::text ELSE '0' END,
		       active_count
		  FROM loan_portfolio_snapshots
		 WHERE snapshot_date >= CURRENT_DATE - ($1 || ' days')::interval
		 ORDER BY snapshot_date
	`, days)
	if err != nil {
		return nil, fmt.Errorf("par-history: %w", err)
	}
	defer rows.Close()
	var out []SnapshotPoint
	for rows.Next() {
		var p SnapshotPoint
		if err := rows.Scan(&p.SnapshotDate, &p.TotalPrincipal, &p.TotalOutstanding,
			&p.Par1Principal, &p.Par30Principal, &p.Par90Principal,
			&p.Par1Pct, &p.Par30Pct, &p.Par90Pct, &p.ActiveCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
