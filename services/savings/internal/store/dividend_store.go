// Dividend run + line persistence + the three calc methods.
//
// Calc methods are implemented as separate SQL queries against
// share_transactions.balance_after_shares. Each returns the per-account
// shares_basis to which the AGM rate will be applied.
//
//   closing_balance — DISTINCT ON account, latest txn ≤ fy_end. If no
//                     prior txn (members onboarded after fy_end), basis
//                     is zero.
//   average_monthly — for each of the 12 month-end dates in the FY,
//                     pick the last balance ≤ that date; average them
//                     all (months where the account didn't exist count
//                     as zero, preserving the spec's prorating intent).
//   pro_rated       — closing_balance × (days_held / days_in_fy),
//                     where days_held = min(fy_end, today) - max(fy_start, first_purchase_at).

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

type DividendStore struct {
	pool *pgxpool.Pool
}

func NewDividendStore(pool *pgxpool.Pool) *DividendStore {
	return &DividendStore{pool: pool}
}

// ─────────── Run CRUD ───────────

const divRunCols = `
	id, tenant_id, run_no, financial_year_label, fy_start, fy_end, status, calc_method,
	agm_rate_pct, agm_resolution_ref, agm_resolution_date, wht_rate_pct,
	member_count, total_share_basis, total_gross_dividend, total_wht, total_net_dividend,
	notes,
	created_at, created_by,
	computed_at, computed_by,
	submitted_at, submitted_by, workflow_instance_id,
	approved_at, approved_by,
	posted_at, posted_by,
	locked_at,
	cancelled_at, cancelled_by, cancellation_reason
`

func scanDivRun(row pgx.Row) (*domain.DividendRun, error) {
	var r domain.DividendRun
	err := row.Scan(
		&r.ID, &r.TenantID, &r.RunNo, &r.FinancialYearLabel, &r.FYStart, &r.FYEnd, &r.Status, &r.CalcMethod,
		&r.AGMRatePct, &r.AGMResolutionRef, &r.AGMResolutionDate, &r.WHTRatePct,
		&r.MemberCount, &r.TotalShareBasis, &r.TotalGrossDividend, &r.TotalWHT, &r.TotalNetDividend,
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

func (s *DividendStore) CreateRunTx(ctx context.Context, tx pgx.Tx, in domain.DividendRun) (*domain.DividendRun, error) {
	runNo, err := nextSeq(ctx, tx, "dividend_run", "DV")
	if err != nil {
		return nil, err
	}
	in.RunNo = runNo
	row := tx.QueryRow(ctx, `
		INSERT INTO dividend_runs (
			tenant_id, run_no, financial_year_label, fy_start, fy_end, status, calc_method,
			agm_rate_pct, agm_resolution_ref, agm_resolution_date, wht_rate_pct,
			notes, created_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4, 'draft', $5,
			$6, $7, $8, $9,
			$10, $11
		)
		RETURNING `+divRunCols,
		in.RunNo, in.FinancialYearLabel, in.FYStart, in.FYEnd, string(in.CalcMethod),
		in.AGMRatePct, in.AGMResolutionRef, in.AGMResolutionDate, in.WHTRatePct,
		in.Notes, in.CreatedBy,
	)
	return scanDivRun(row)
}

func (s *DividendStore) GetRunTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.DividendRun, error) {
	row := tx.QueryRow(ctx, `SELECT `+divRunCols+` FROM dividend_runs WHERE id = $1`, id)
	r, err := scanDivRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

type DivRunListFilter struct {
	Status string
	FYLike string
	Limit  int
	Offset int
}

func (s *DividendStore) ListRunsTx(ctx context.Context, tx pgx.Tx, f DivRunListFilter) ([]domain.DividendRun, int, error) {
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
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM dividend_runs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM dividend_runs %s
		ORDER BY fy_end DESC, created_at DESC
		LIMIT $%d OFFSET $%d
	`, divRunCols, where, idx, idx+1), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []domain.DividendRun
	for rows.Next() {
		r, err := scanDivRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

// ─────────── Status transitions ───────────

type DivStatusTransition struct {
	To              domain.DividendRunStatus
	By              uuid.UUID
	WorkflowID      *uuid.UUID
	CancelReason    *string
	Aggregates      *DivRunAggregates
}

type DivRunAggregates struct {
	MemberCount         int
	TotalShareBasis     decimal.Decimal
	TotalGrossDividend  decimal.Decimal
	TotalWHT            decimal.Decimal
	TotalNetDividend    decimal.Decimal
}

func (s *DividendStore) UpdateStatusTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, t DivStatusTransition) (*domain.DividendRun, error) {
	fields := []string{"status = $2"}
	args := []any{runID, string(t.To)}
	idx := 3
	switch t.To {
	case domain.DivPreview:
		fields = append(fields, fmt.Sprintf("computed_at = now(), computed_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.Aggregates != nil {
			fields = append(fields,
				fmt.Sprintf("member_count = $%d", idx),
				fmt.Sprintf("total_share_basis = $%d", idx+1),
				fmt.Sprintf("total_gross_dividend = $%d", idx+2),
				fmt.Sprintf("total_wht = $%d", idx+3),
				fmt.Sprintf("total_net_dividend = $%d", idx+4),
			)
			args = append(args,
				t.Aggregates.MemberCount,
				t.Aggregates.TotalShareBasis,
				t.Aggregates.TotalGrossDividend,
				t.Aggregates.TotalWHT,
				t.Aggregates.TotalNetDividend,
			)
			idx += 5
		}
	case domain.DivApproved:
		fields = append(fields, fmt.Sprintf("approved_at = now(), approved_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.WorkflowID != nil {
			fields = append(fields, fmt.Sprintf("workflow_instance_id = $%d, submitted_at = COALESCE(submitted_at, now()), submitted_by = COALESCE(submitted_by, $%d)", idx, idx+1))
			args = append(args, *t.WorkflowID, t.By); idx += 2
		}
	case domain.DivPosted:
		fields = append(fields, fmt.Sprintf("posted_at = now(), posted_by = $%d", idx))
		args = append(args, t.By); idx++
	case domain.DivLocked:
		fields = append(fields, "locked_at = now()")
	case domain.DivCancelled:
		fields = append(fields, fmt.Sprintf("cancelled_at = now(), cancelled_by = $%d", idx))
		args = append(args, t.By); idx++
		if t.CancelReason != nil {
			fields = append(fields, fmt.Sprintf("cancellation_reason = $%d", idx))
			args = append(args, *t.CancelReason); idx++
		}
	}
	q := fmt.Sprintf(`UPDATE dividend_runs SET %s WHERE id = $1 RETURNING `+divRunCols, joinFields(fields))
	row := tx.QueryRow(ctx, q, args...)
	return scanDivRun(row)
}

func (s *DividendStore) UpdateWorkflowIDTx(ctx context.Context, tx pgx.Tx, runID, wfID, by uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE dividend_runs
		   SET workflow_instance_id = $2,
		       submitted_at = COALESCE(submitted_at, now()),
		       submitted_by = COALESCE(submitted_by, $3)
		 WHERE id = $1
	`, runID, wfID, by)
	return err
}

// ─────────── Compute (per method) ───────────

// AccountBasis is the per-account intermediate output the calculator
// emits. handler.postDividendLine converts it into a DividendRunLine.
type AccountBasis struct {
	AccountID    uuid.UUID
	CounterpartyID     uuid.UUID
	SharesBasis  decimal.Decimal
	DaysHeldInFY *int   // only populated for pro_rated
}

// ComputeBasisTx runs the appropriate SQL for the requested calc method
// and returns the per-account basis values. Caller multiplies by par
// and applies rate/WHT (in domain.DivCalcLine).
func (s *DividendStore) ComputeBasisTx(
	ctx context.Context, tx pgx.Tx,
	method domain.DividendCalcMethod,
	fyStart, fyEnd time.Time,
) ([]AccountBasis, error) {
	switch method {
	case domain.CalcClosingBalance:
		return s.computeClosingBasisTx(ctx, tx, fyEnd)
	case domain.CalcAverageMonthly:
		return s.computeAverageMonthlyBasisTx(ctx, tx, fyStart, fyEnd)
	case domain.CalcProRated:
		return s.computeProRatedBasisTx(ctx, tx, fyStart, fyEnd)
	}
	return nil, domain.ErrDivInvalidCalcMethod
}

// computeClosingBasisTx — latest balance_after_shares on or before
// fy_end (end-of-day) per account. We anchor on share_accounts so we
// include accounts with zero pre-fy_end txns too (they're filtered out
// by the HAVING clause / loop). The +1 day - 1 microsecond extends the
// inclusive comparison to the very end of the fy_end day so txns
// posted any time on that date are captured.
func (s *DividendStore) computeClosingBasisTx(ctx context.Context, tx pgx.Tx, fyEnd time.Time) ([]AccountBasis, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.counterparty_id,
		       COALESCE((
		         SELECT t.balance_after_shares
		         FROM share_transactions t
		         WHERE t.account_id = a.id
		           AND t.posted_at < ($1::date + INTERVAL '1 day')
		         ORDER BY t.posted_at DESC, t.id DESC
		         LIMIT 1
		       ), 0) AS closing_shares
		FROM share_accounts a
		JOIN members m ON m.counterparty_id = a.counterparty_id
		WHERE a.status = 'active'
		  AND m.status NOT IN ('blacklisted', 'exited', 'deceased', 'rejected')
		ORDER BY a.id
	`, fyEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountBasis
	for rows.Next() {
		var b AccountBasis
		var shares int
		if err := rows.Scan(&b.AccountID, &b.CounterpartyID, &shares); err != nil {
			return nil, err
		}
		if shares <= 0 {
			continue
		}
		b.SharesBasis = decimal.NewFromInt(int64(shares))
		out = append(out, b)
	}
	return out, rows.Err()
}

// computeAverageMonthlyBasisTx — for each month-end in [fy_start, fy_end],
// take the latest balance ≤ that date for each account, then average across
// all months. Months where the account didn't yet exist count as 0,
// preserving the spec's "average balance over the year" semantics.
func (s *DividendStore) computeAverageMonthlyBasisTx(ctx context.Context, tx pgx.Tx, fyStart, fyEnd time.Time) ([]AccountBasis, error) {
	// Build the list of month-end dates inside the FY range.
	monthEnds := monthEndsBetween(fyStart, fyEnd)
	if len(monthEnds) == 0 {
		return nil, fmt.Errorf("FY range produced no month-ends")
	}
	rows, err := tx.Query(ctx, `
		WITH month_ends AS (
		  SELECT UNNEST($1::date[]) AS d
		),
		basis AS (
		  SELECT a.id AS account_id, a.counterparty_id, me.d,
		         COALESCE((
		           SELECT t.balance_after_shares
		           FROM share_transactions t
		           WHERE t.account_id = a.id
		             AND t.posted_at < (me.d + INTERVAL '1 day')
		           ORDER BY t.posted_at DESC, t.id DESC
		           LIMIT 1
		         ), 0) AS shares_at_month_end
		  FROM share_accounts a
		  JOIN members m ON m.counterparty_id = a.counterparty_id
		  CROSS JOIN month_ends me
		  WHERE a.status = 'active'
		    AND m.status NOT IN ('blacklisted', 'exited', 'deceased', 'rejected')
		)
		SELECT account_id, counterparty_id, AVG(shares_at_month_end)::numeric(20,4) AS avg_shares
		FROM basis
		GROUP BY account_id, counterparty_id
		HAVING AVG(shares_at_month_end) > 0
		ORDER BY account_id
	`, monthEnds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountBasis
	for rows.Next() {
		var b AccountBasis
		if err := rows.Scan(&b.AccountID, &b.CounterpartyID, &b.SharesBasis); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// computeProRatedBasisTx — closing balance × (days_held / days_in_fy).
// days_held = min(fy_end, now) - max(fy_start, first_purchase_at).
// Stores days_held_in_fy on the resulting line for audit traceability.
func (s *DividendStore) computeProRatedBasisTx(ctx context.Context, tx pgx.Tx, fyStart, fyEnd time.Time) ([]AccountBasis, error) {
	rows, err := tx.Query(ctx, `
		WITH closing AS (
		  SELECT a.id AS account_id, a.counterparty_id, a.first_purchase_at,
		         COALESCE((
		           SELECT t.balance_after_shares
		           FROM share_transactions t
		           WHERE t.account_id = a.id
		             AND t.posted_at < ($2::date + INTERVAL '1 day')
		           ORDER BY t.posted_at DESC, t.id DESC
		           LIMIT 1
		         ), 0) AS closing_shares
		  FROM share_accounts a
		  JOIN members m ON m.counterparty_id = a.counterparty_id
		  WHERE a.status = 'active'
		    AND m.status NOT IN ('blacklisted', 'exited', 'deceased', 'rejected')
		),
		ratios AS (
		  SELECT account_id, counterparty_id, closing_shares,
		         GREATEST(0, LEAST(
		           ($2::date - $1::date + 1),
		           ($2::date - GREATEST($1::date, COALESCE(first_purchase_at::date, $1::date)) + 1)
		         )) AS days_held,
		         ($2::date - $1::date + 1) AS days_in_fy
		  FROM closing
		)
		SELECT account_id, counterparty_id, closing_shares, days_held, days_in_fy
		FROM ratios
		WHERE closing_shares > 0
		ORDER BY account_id
	`, fyStart, fyEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountBasis
	for rows.Next() {
		var b AccountBasis
		var closingShares int
		var daysHeld, daysInFY int
		if err := rows.Scan(&b.AccountID, &b.CounterpartyID, &closingShares, &daysHeld, &daysInFY); err != nil {
			return nil, err
		}
		if daysInFY <= 0 || daysHeld <= 0 {
			continue
		}
		// pro-rated basis = closing × (days_held / days_in_fy)
		b.SharesBasis = decimal.NewFromInt(int64(closingShares)).
			Mul(decimal.NewFromInt(int64(daysHeld))).
			Div(decimal.NewFromInt(int64(daysInFY))).
			Round(4)
		dh := daysHeld
		b.DaysHeldInFY = &dh
		out = append(out, b)
	}
	return out, rows.Err()
}

// monthEndsBetween returns the LAST DAY of each calendar month that
// intersects [start, end]. Used by computeAverageMonthlyBasisTx.
func monthEndsBetween(start, end time.Time) []time.Time {
	var out []time.Time
	year, month, _ := start.Date()
	loc := start.Location()
	for {
		// Last day of month = first of next month - 1 day.
		monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, loc)
		nextMonth := monthStart.AddDate(0, 1, 0)
		monthEnd := nextMonth.AddDate(0, 0, -1)
		if monthEnd.After(end) {
			// Cap last month-end at fy_end so we don't overshoot.
			monthEnd = end
		}
		out = append(out, monthEnd)
		if !monthEnd.Before(end) {
			break
		}
		month++
		if month > 12 {
			month = 1
			year++
		}
	}
	return out
}

// ─────────── Lines persistence ───────────

func (s *DividendStore) ReplaceLinesTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID, lines []domain.DividendRunLine) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM dividend_runs WHERE id = $1`, runID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == string(domain.DivPosted) || status == string(domain.DivLocked) {
		return fmt.Errorf("cannot replace lines on a %s run", status)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM dividend_run_lines WHERE run_id = $1`, runID); err != nil {
		return err
	}
	for i := range lines {
		l := &lines[i]
		_, err := tx.Exec(ctx, `
			INSERT INTO dividend_run_lines (
				tenant_id, run_id, share_account_id, counterparty_id,
				calc_method, shares_basis, par_value_at_run, capital_basis,
				days_held_in_fy, days_in_fy, rate_applied_pct, wht_rate_pct,
				gross_dividend, wht_amount, net_dividend,
				payout_method
			) VALUES (
				current_tenant_id(), $1, $2, $3,
				$4, $5, $6, $7,
				$8, $9, $10, $11,
				$12, $13, $14,
				$15
			)
		`,
			runID, l.ShareAccountID, l.CounterpartyID,
			string(l.CalcMethod), l.SharesBasis, l.ParValueAtRun, l.CapitalBasis,
			l.DaysHeldInFY, l.DaysInFY, l.RateAppliedPct, l.WHTRatePct,
			l.GrossDividend, l.WHTAmount, l.NetDividend,
			string(l.PayoutMethod),
		)
		if err != nil {
			return fmt.Errorf("insert dividend line: %w", err)
		}
	}
	return nil
}

func (s *DividendStore) LinesByRunTx(ctx context.Context, tx pgx.Tx, runID uuid.UUID) ([]domain.DividendRunLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, run_id, share_account_id, counterparty_id,
		       calc_method, shares_basis, par_value_at_run, capital_basis,
		       days_held_in_fy, days_in_fy, rate_applied_pct, wht_rate_pct,
		       gross_dividend, wht_amount, net_dividend,
		       payout_method, payout_target_account_id,
		       payout_external_channel, payout_external_ref,
		       posted_at, posted_deposit_txn_id, posted_share_txn_id, notes
		FROM dividend_run_lines
		WHERE run_id = $1
		ORDER BY counterparty_id, share_account_id
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.DividendRunLine
	for rows.Next() {
		var l domain.DividendRunLine
		err := rows.Scan(
			&l.ID, &l.TenantID, &l.RunID, &l.ShareAccountID, &l.CounterpartyID,
			&l.CalcMethod, &l.SharesBasis, &l.ParValueAtRun, &l.CapitalBasis,
			&l.DaysHeldInFY, &l.DaysInFY, &l.RateAppliedPct, &l.WHTRatePct,
			&l.GrossDividend, &l.WHTAmount, &l.NetDividend,
			&l.PayoutMethod, &l.PayoutTargetAccountID,
			&l.PayoutExternalChannel, &l.PayoutExternalRef,
			&l.PostedAt, &l.PostedDepositTxnID, &l.PostedShareTxnID, &l.Notes,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *DividendStore) UpdateLinePayoutTx(
	ctx context.Context, tx pgx.Tx,
	lineID uuid.UUID,
	method domain.InterestPayoutMethod,
	targetAcct *uuid.UUID,
	extChannel, extRef *string,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE dividend_run_lines
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

func (s *DividendStore) MarkLinePostedTx(
	ctx context.Context, tx pgx.Tx,
	lineID uuid.UUID, depositTxnID, shareTxnID *uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE dividend_run_lines
		   SET posted_at = now(),
		       posted_deposit_txn_id = $2,
		       posted_share_txn_id = $3
		 WHERE id = $1
	`, lineID, depositTxnID, shareTxnID)
	return err
}
