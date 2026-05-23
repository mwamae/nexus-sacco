// Interest run + line + tax-payable persistence.
//
// Compute pass:
//   1. ComputeLinesTx — aggregates deposit_daily_balances over the FY
//      range for every in-scope account, computes weighted-average +
//      gross/WHT/net, returns the (un-persisted) lines.
//   2. ReplaceLinesTx — drops any previous draft lines for the run and
//      inserts the new set + updates the run header aggregates.
//
// Posting pass:
//   For each line, depending on payout_method, post the deposit credit
//   (deposit_transactions) OR share purchase (share_transactions) OR
//   the external entry, then write the tax_payable_ledger row.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type InterestStore struct {
	pool *pgxpool.Pool
}

func NewInterestStore(pool *pgxpool.Pool) *InterestStore {
	return &InterestStore{pool: pool}
}

// ─────────── Run CRUD ───────────

const runCols = `
	id, tenant_id, run_no, financial_year_label, fy_start, fy_end, status,
	agm_rate_pct, agm_resolution_ref, agm_resolution_date, wht_rate_pct,
	product_ids,
	member_count, total_weighted_balance, total_gross_interest, total_wht, total_net_interest,
	notes,
	created_at, created_by,
	computed_at, computed_by,
	submitted_at, submitted_by, workflow_instance_id,
	approved_at, approved_by,
	posted_at, posted_by,
	locked_at,
	cancelled_at, cancelled_by, cancellation_reason
`

func scanRun(row pgx.Row) (*domain.InterestRun, error) {
	var r domain.InterestRun
	err := row.Scan(
		&r.ID, &r.TenantID, &r.RunNo, &r.FinancialYearLabel, &r.FYStart, &r.FYEnd, &r.Status,
		&r.AGMRatePct, &r.AGMResolutionRef, &r.AGMResolutionDate, &r.WHTRatePct,
		&r.ProductIDs,
		&r.MemberCount, &r.TotalWeightedBalance, &r.TotalGrossInterest, &r.TotalWHT, &r.TotalNetInterest,
		&r.Notes,
		&r.CreatedAt, &r.CreatedBy,
		&r.ComputedAt, &r.ComputedBy,
		&r.SubmittedAt, &r.SubmittedBy, &r.WorkflowInstanceID,
		&r.ApprovedAt, &r.ApprovedBy,
		&r.PostedAt, &r.PostedBy,
		&r.LockedAt,
		&r.CancelledAt, &r.CancelledBy, &r.CancellationReason,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRunTx inserts a new draft run. Caller validates inputs upstream.
func (s *InterestStore) CreateRunTx(ctx context.Context, tx pgx.Tx, in domain.InterestRun) (*domain.InterestRun, error) {
	runNo, err := nextSeq(ctx, tx, "interest_run", "IR")
	if err != nil {
		return nil, err
	}
	in.RunNo = runNo
	row := tx.QueryRow(ctx, `
		INSERT INTO interest_runs (
			tenant_id, run_no, financial_year_label, fy_start, fy_end, status,
			agm_rate_pct, agm_resolution_ref, agm_resolution_date, wht_rate_pct,
			product_ids, notes, created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, 'draft',
			$5, $6, $7, $8,
			$9, $10, $11
		)
		RETURNING `+runCols,
		in.RunNo, in.FinancialYearLabel, in.FYStart, in.FYEnd,
		in.AGMRatePct, in.AGMResolutionRef, in.AGMResolutionDate, in.WHTRatePct,
		in.ProductIDs, in.Notes, in.CreatedBy,
	)
	return scanRun(row)
}

func (s *InterestStore) GetRunTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.InterestRun, error) {
	row := tx.QueryRow(ctx, `SELECT `+runCols+` FROM interest_runs WHERE id = $1`, id)
	r, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

type RunListFilter struct {
	Status string
	FYLike string
	Limit  int
	Offset int
}

func (s *InterestStore) ListRunsTx(ctx context.Context, tx pgx.Tx, f RunListFilter) ([]domain.InterestRun, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, f.Status)
		idx++
	}
	if f.FYLike != "" {
		where += fmt.Sprintf(" AND financial_year_label ILIKE $%d", idx)
		args = append(args, "%"+f.FYLike+"%")
		idx++
	}
	var total int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM interest_runs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM interest_runs %s
		ORDER BY fy_end DESC, created_at DESC
		LIMIT $%d OFFSET $%d
	`, runCols, where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.InterestRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

// UpdateStatusTx records a status transition and the matching audit
// columns (computed_at, submitted_at, approved_at, posted_at, etc.).
type StatusTransition struct {
	To              domain.InterestRunStatus
	By              uuid.UUID
	WorkflowID      *uuid.UUID
	CancelReason    *string
	Aggregates      *RunAggregates // optional — set when moving to preview
	ApprovedAt      *time.Time     // optional override; defaults to now()
}

type RunAggregates struct {
	MemberCount          int
	TotalWeightedBalance decimal.Decimal
	TotalGrossInterest   decimal.Decimal
	TotalWHT             decimal.Decimal
	TotalNetInterest     decimal.Decimal
}

func (s *InterestStore) UpdateStatusTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, t StatusTransition) (*domain.InterestRun, error) {
	// Build SET clause per target.
	fields := []string{"status = $2"}
	args := []any{runID, string(t.To)}
	idx := 3
	switch t.To {
	case domain.RunComputing:
		// transient — no extra columns
	case domain.RunPreview:
		fields = append(fields, fmt.Sprintf("computed_at = now(), computed_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.Aggregates != nil {
			fields = append(fields,
				fmt.Sprintf("member_count = $%d", idx),
				fmt.Sprintf("total_weighted_balance = $%d", idx+1),
				fmt.Sprintf("total_gross_interest = $%d", idx+2),
				fmt.Sprintf("total_wht = $%d", idx+3),
				fmt.Sprintf("total_net_interest = $%d", idx+4),
			)
			args = append(args,
				t.Aggregates.MemberCount,
				t.Aggregates.TotalWeightedBalance,
				t.Aggregates.TotalGrossInterest,
				t.Aggregates.TotalWHT,
				t.Aggregates.TotalNetInterest,
			)
			idx += 5
		}
	case domain.RunApproved:
		fields = append(fields, fmt.Sprintf("approved_at = now(), approved_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.WorkflowID != nil {
			fields = append(fields, fmt.Sprintf("workflow_instance_id = $%d, submitted_at = COALESCE(submitted_at, now()), submitted_by = COALESCE(submitted_by, $%d)", idx, idx+1))
			args = append(args, *t.WorkflowID, t.By); idx += 2
		}
	case domain.RunPosting:
		// transient — no extra columns
	case domain.RunPosted:
		fields = append(fields, fmt.Sprintf("posted_at = now(), posted_by = $%d", idx))
		args = append(args, t.By); idx++
	case domain.RunLocked:
		fields = append(fields, "locked_at = now()")
	case domain.RunCancelled:
		fields = append(fields, fmt.Sprintf("cancelled_at = now(), cancelled_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.CancelReason != nil {
			fields = append(fields, fmt.Sprintf("cancellation_reason = $%d", idx))
			args = append(args, *t.CancelReason); idx++
		}
	}
	q := fmt.Sprintf(`UPDATE interest_runs SET %s WHERE id = $1 RETURNING `+runCols, joinFields(fields))
	row := tx.QueryRow(ctx, q, args...)
	return scanRun(row)
}

// UpdateWorkflowIDTx is used after we create the workflow instance on
// preview-submit, before approval comes back.
func (s *InterestStore) UpdateWorkflowIDTx(ctx context.Context, tx pgx.Tx, runID, wfID, by uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE interest_runs
		   SET workflow_instance_id = $2,
		       submitted_at = COALESCE(submitted_at, now()),
		       submitted_by = COALESCE(submitted_by, $3)
		 WHERE id = $1
	`, runID, wfID, by)
	return err
}

func joinFields(fs []string) string {
	out := ""
	for i, f := range fs {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out
}

// ─────────── Compute pass ───────────

// ComputeLinesTx aggregates deposit_daily_balances over the FY range
// for every account whose product is in scope, computes the
// weighted-average + gross/WHT/net per line, and returns the slice.
// Pure read — no writes, idempotent. Caller persists via ReplaceLinesTx.
func (s *InterestStore) ComputeLinesTx(
	ctx context.Context, tx pgx.Tx,
	run *domain.InterestRun,
) ([]domain.InterestRunLine, error) {
	if len(run.ProductIDs) == 0 {
		return nil, domain.ErrNoProductsInScope
	}
	daysInFY := domain.DaysInFY(run.FYStart, run.FYEnd)
	if daysInFY <= 0 {
		return nil, domain.ErrFYInvalid
	}

	rows, err := tx.Query(ctx, `
		SELECT account_id, counterparty_id, product_id,
		       COUNT(*)              AS snapshot_days,
		       COALESCE(SUM(balance), 0) AS sum_balance
		FROM deposit_daily_balances
		WHERE snapshot_date BETWEEN $1 AND $2
		  AND product_id = ANY($3)
		GROUP BY account_id, counterparty_id, product_id
		HAVING SUM(balance) > 0
		ORDER BY account_id
	`, run.FYStart, run.FYEnd, run.ProductIDs)
	if err != nil {
		return nil, fmt.Errorf("aggregate snapshots: %w", err)
	}
	defer rows.Close()

	var lines []domain.InterestRunLine
	for rows.Next() {
		var (
			acctID, memberID, productID uuid.UUID
			snapshotDays                int
			sumBalance                  decimal.Decimal
		)
		if err := rows.Scan(&acctID, &memberID, &productID, &snapshotDays, &sumBalance); err != nil {
			return nil, err
		}
		line := domain.CalcLine(domain.CalcInputs{
			AccountID:          acctID,
			CounterpartyID:           memberID,
			ProductID:          productID,
			DaysInFY:           daysInFY,
			DaysWithSnapshots:  snapshotDays,
			SumOfDailyBalances: sumBalance,
		}, run.AGMRatePct, run.WHTRatePct)
		line.RunID = run.ID
		line.TenantID = run.TenantID
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

// ReplaceLinesTx wipes prior lines for the run and inserts the new set.
// Idempotent — safe to call multiple times during the draft/preview
// phase. Refuses if the run is already posted.
func (s *InterestStore) ReplaceLinesTx(
	ctx context.Context, tx pgx.Tx,
	runID uuid.UUID, lines []domain.InterestRunLine,
) error {
	// Refuse if already posted.
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM interest_runs WHERE id = $1`, runID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == string(domain.RunPosted) || status == string(domain.RunLocked) {
		return fmt.Errorf("cannot replace lines on a %s run", status)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM interest_run_lines WHERE run_id = $1`, runID); err != nil {
		return err
	}
	for i := range lines {
		l := &lines[i]
		_, err := tx.Exec(ctx, `
			INSERT INTO interest_run_lines (
				tenant_id, run_id, account_id, counterparty_id, product_id,
				days_in_fy, days_with_snapshots, sum_of_daily_balances,
				weighted_avg_balance, rate_applied_pct, wht_rate_pct,
				gross_interest, wht_amount, net_interest,
				payout_method
			) VALUES (
				current_tenant_id(), $1, $2, $3, $4,
				$5, $6, $7,
				$8, $9, $10,
				$11, $12, $13,
				$14
			)
		`,
			runID, l.AccountID, l.CounterpartyID, l.ProductID,
			l.DaysInFY, l.DaysWithSnapshots, l.SumOfDailyBalances,
			l.WeightedAvgBalance, l.RateAppliedPct, l.WHTRatePct,
			l.GrossInterest, l.WHTAmount, l.NetInterest,
			string(l.PayoutMethod),
		)
		if err != nil {
			return fmt.Errorf("insert line: %w", err)
		}
	}
	return nil
}

// LinesByRunTx fetches all lines for a run. Used for preview rendering
// and the posting loop.
func (s *InterestStore) LinesByRunTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID) ([]domain.InterestRunLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, run_id, account_id, counterparty_id, product_id,
		       days_in_fy, days_with_snapshots, sum_of_daily_balances,
		       weighted_avg_balance, rate_applied_pct, wht_rate_pct,
		       gross_interest, wht_amount, net_interest,
		       payout_method, payout_target_account_id,
		       payout_external_channel, payout_external_ref,
		       posted_at, posted_txn_id, share_txn_id, notes
		FROM interest_run_lines
		WHERE run_id = $1
		ORDER BY counterparty_id, account_id
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.InterestRunLine
	for rows.Next() {
		var l domain.InterestRunLine
		err := rows.Scan(
			&l.ID, &l.TenantID, &l.RunID, &l.AccountID, &l.CounterpartyID, &l.ProductID,
			&l.DaysInFY, &l.DaysWithSnapshots, &l.SumOfDailyBalances,
			&l.WeightedAvgBalance, &l.RateAppliedPct, &l.WHTRatePct,
			&l.GrossInterest, &l.WHTAmount, &l.NetInterest,
			&l.PayoutMethod, &l.PayoutTargetAccountID,
			&l.PayoutExternalChannel, &l.PayoutExternalRef,
			&l.PostedAt, &l.PostedTxnID, &l.ShareTxnID, &l.Notes,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateLinePayoutTx lets the operator override the payout method per line
// before posting.
func (s *InterestStore) UpdateLinePayoutTx(
	ctx context.Context, tx pgx.Tx,
	lineID uuid.UUID,
	method domain.InterestPayoutMethod,
	targetAcct *uuid.UUID,
	extChannel, extRef *string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE interest_run_lines
		   SET payout_method = $2,
		       payout_target_account_id = $3,
		       payout_external_channel = $4,
		       payout_external_ref = $5
		 WHERE id = $1
		   AND posted_at IS NULL
	`, lineID, string(method), targetAcct, extChannel, extRef)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkLinePostedTx records the line as posted, with the resulting txn
// ids (deposit-side and share-side).
func (s *InterestStore) MarkLinePostedTx(
	ctx context.Context, tx pgx.Tx,
	lineID uuid.UUID, depositTxnID, shareTxnID *uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE interest_run_lines
		   SET posted_at = now(),
		       posted_txn_id = $2,
		       share_txn_id  = $3
		 WHERE id = $1
	`, lineID, depositTxnID, shareTxnID)
	return err
}

// ─────────── Tax payable ledger ───────────

func (s *InterestStore) InsertTaxPayableTx(
	ctx context.Context, tx pgx.Tx,
	e *domain.TaxPayableEntry,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tax_payable_ledger (
			tenant_id, source_kind, source_id, counterparty_id, member_no, member_name,
			fy_label, gross_amount, wht_rate_pct, wht_amount, posted_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
	`,
		e.SourceKind, e.SourceID, e.CounterpartyID, e.MemberNo, e.MemberName,
		e.FYLabel, e.GrossAmount, e.WHTRatePct, e.WHTAmount, e.PostedBy,
	)
	return err
}

type WHTScheduleRow struct {
	CounterpartyID    uuid.UUID       `json:"counterparty_id"`
	MemberNo    string          `json:"member_no"`
	MemberName  string          `json:"member_name"`
	GrossAmount decimal.Decimal `json:"gross_amount"`
	WHTAmount   decimal.Decimal `json:"wht_amount"`
}

// WHTScheduleForFYTx aggregates the per-member WHT totals across all
// sources (interest, dividends, manual) for a given FY label. Used for
// the KRA remittance schedule.
func (s *InterestStore) WHTScheduleForFYTx(
	ctx context.Context, tx pgx.Tx,
	fyLabel string,
) ([]WHTScheduleRow, decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `
		SELECT counterparty_id, member_no, member_name,
		       SUM(gross_amount) AS gross,
		       SUM(wht_amount)   AS wht
		FROM tax_payable_ledger
		WHERE fy_label = $1
		GROUP BY counterparty_id, member_no, member_name
		ORDER BY wht DESC
	`, fyLabel)
	if err != nil {
		return nil, decimal.Zero, err
	}
	defer rows.Close()
	var out []WHTScheduleRow
	var total decimal.Decimal
	for rows.Next() {
		var r WHTScheduleRow
		if err := rows.Scan(&r.CounterpartyID, &r.MemberNo, &r.MemberName, &r.GrossAmount, &r.WHTAmount); err != nil {
			return nil, decimal.Zero, err
		}
		out = append(out, r)
		total = total.Add(r.WHTAmount)
	}
	return out, total, rows.Err()
}

// PerMemberCertificateTx gives a member's combined interest+dividend
// WHT entries for a given FY. Used for the tax-filing PDF.
func (s *InterestStore) PerMemberCertificateTx(
	ctx context.Context, tx pgx.Tx,
	memberID uuid.UUID, fyLabel string,
) ([]domain.TaxPayableEntry, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, source_kind, source_id, counterparty_id, member_no, member_name,
		       fy_label, gross_amount, wht_rate_pct, wht_amount,
		       posted_at, posted_by, remitted_at, remittance_ref
		FROM tax_payable_ledger
		WHERE counterparty_id = (SELECT counterparty_id FROM members WHERE id = $1) AND fy_label = $2
		ORDER BY posted_at
	`, memberID, fyLabel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaxPayableEntry
	for rows.Next() {
		var e domain.TaxPayableEntry
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.SourceKind, &e.SourceID, &e.CounterpartyID, &e.MemberNo, &e.MemberName,
			&e.FYLabel, &e.GrossAmount, &e.WHTRatePct, &e.WHTAmount,
			&e.PostedAt, &e.PostedBy, &e.RemittedAt, &e.RemittanceRef,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
