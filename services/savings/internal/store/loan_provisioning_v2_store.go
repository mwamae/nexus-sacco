// Loans Phase 3 provisioning store.
//
// Distinct from the legacy ProvisioningStore (which keeps powering
// /v1/provisioning/* for one release) because the calculation model
// changed materially:
//
//   - Inputs come from loan_dpd_snapshots, not a live recompute of DPD.
//     The classifier worker already wrote a snapshot per loan per day;
//     a provisioning run picks the row dated on or just before the
//     period end. This is the SASRA + IFRS 9 audit trail.
//
//   - Rates come from ecl_rate_matrix (per tenant, history-preserving)
//     rather than the four tenant_operations columns. The matrix is
//     keyed by (classification_sasra, classification_ifrs9_stage) so
//     IFRS 9 staging contributes to the rate.
//
//   - Runs are grouped by period_month (the 1st of the month). Only
//     one active run per (tenant, period_month) at a time.
//
// Posting is identical to the legacy path (DR 5210 / CR 1120 for the
// movement, legs flip on a release). The handler shares the JE-building
// code with the legacy handler via the posting.Client.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
)

// LoanProvisioningV2Store layers Phase 3 methods on the same connection
// pool as the legacy ProvisioningStore. They share scanProvRun, the
// provRunCols constant, and the ErrProvRunNotFound sentinel.
type LoanProvisioningV2Store struct {
	pool *db.Pool
}

func NewLoanProvisioningV2Store(pool *db.Pool) *LoanProvisioningV2Store {
	return &LoanProvisioningV2Store{pool: pool}
}

var ErrProvRunForMonthExists = errors.New("provisioning_v2: a run for this period already exists (draft/computed/posted) — cancel or supersede first")
var ErrProvRunNotCancellable = errors.New("provisioning_v2: run cannot be cancelled in its current state")
var ErrProvRunNotPostable    = errors.New("provisioning_v2: run cannot be posted in its current state")
var ErrProvNoSnapshots       = errors.New("provisioning_v2: no DPD snapshots in the requested period — has the dpd-classifier worker run?")

// firstOfMonth normalises any date to the first day of its month at
// 00:00 UTC. Used to canonicalise period_month inputs.
func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// lastOfMonth returns 23:59:59 on the last day of the month.
func lastOfMonth(t time.Time) time.Time {
	first := firstOfMonth(t)
	return first.AddDate(0, 1, 0).Add(-time.Second)
}

// CreateRunV2 computes a fresh provisioning run for the given month.
// Reads the latest dpd_snapshot per loan dated within the month, joins
// to ecl_rate_matrix for the per-classification ECL%, writes one
// provision_run_lines row per loan, and rolls totals to provision_runs.
//
// Returns ErrProvRunForMonthExists if a draft/computed/posted run
// already exists for the period — caller must cancel or supersede first.
// Returns ErrProvNoSnapshots if dpd_snapshots is empty for the period.
func (s *LoanProvisioningV2Store) CreateRunV2(
	ctx context.Context,
	tenantID uuid.UUID,
	periodMonth time.Time,
	notes *string,
	userID uuid.UUID,
) (*domain.ProvisionRun, error) {
	period := firstOfMonth(periodMonth)
	periodEnd := lastOfMonth(period)

	var run *domain.ProvisionRun
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var exists int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM provision_runs
			 WHERE period_month = $1
			   AND status IN ('draft','computed','posted')
		`, period).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			return ErrProvRunForMonthExists
		}

		// snapshot_as_of is the latest snapshot date in the period —
		// typically the last day of the month, but a period that ends
		// mid-month (e.g. running a Feb close on Feb-28) still picks
		// the latest snapshot before period_end.
		var snapshotAsOf *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT MAX(snapshot_date)
			  FROM loan_dpd_snapshots
			 WHERE snapshot_date <= $1
		`, periodEnd).Scan(&snapshotAsOf); err != nil {
			return fmt.Errorf("snapshot lookup: %w", err)
		}
		if snapshotAsOf == nil {
			return ErrProvNoSnapshots
		}

		// One row per loan from the latest snapshot. Join to loans for
		// counterparty_id, loan_no, product_id — those aren't on the
		// snapshot. Join to ecl_rate_matrix for the rate (latest
		// effective_from <= period_end).
		linesSQL := `
			WITH latest AS (
			  SELECT DISTINCT ON (loan_id)
			         loan_id, snapshot_date, dpd_days, principal_balance,
			         classification_sasra, classification_ifrs9_stage
			    FROM loan_dpd_snapshots
			   WHERE snapshot_date <= $1
			   ORDER BY loan_id, snapshot_date DESC
			), rate AS (
			  SELECT classification_sasra, classification_ifrs9_stage,
			         ecl_rate_pct,
			         row_number() OVER (
			           PARTITION BY classification_sasra, classification_ifrs9_stage
			           ORDER BY effective_from DESC
			         ) AS rn
			    FROM ecl_rate_matrix
			   WHERE effective_from <= $1
			)
			SELECT lat.loan_id, l.counterparty_id, l.loan_no, l.product_id,
			       lat.dpd_days, lat.classification_sasra, lat.classification_ifrs9_stage,
			       lat.principal_balance,
			       COALESCE(r.ecl_rate_pct, 0)::numeric AS ecl_rate
			  FROM latest lat
			  JOIN loans l ON l.id = lat.loan_id
			  LEFT JOIN rate r
			    ON r.classification_sasra = lat.classification_sasra
			   AND r.classification_ifrs9_stage = lat.classification_ifrs9_stage
			   AND r.rn = 1
			 WHERE l.status IN ('active','in_arrears','restructured')
			   AND lat.principal_balance > 0
		`

		rows, err := tx.Query(ctx, linesSQL, periodEnd)
		if err != nil {
			return fmt.Errorf("compute lines: %w", err)
		}

		type lineDraft struct {
			loanID       uuid.UUID
			cpID         uuid.UUID
			loanNo       string
			productID    uuid.UUID
			dpd          int
			sasra        string
			stage        int
			outstanding  decimal.Decimal
			rate         decimal.Decimal
		}
		var drafts []lineDraft
		var totalOutstanding, totalProvision decimal.Decimal
		for rows.Next() {
			var d lineDraft
			if err := rows.Scan(
				&d.loanID, &d.cpID, &d.loanNo, &d.productID,
				&d.dpd, &d.sasra, &d.stage,
				&d.outstanding, &d.rate,
			); err != nil {
				rows.Close()
				return err
			}
			drafts = append(drafts, d)
			totalOutstanding = totalOutstanding.Add(d.outstanding)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		runID := uuid.New()

		// Previous provision = latest posted run BEFORE this period.
		// Match the legacy semantics so the movement comparison works.
		var prevProvision decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(total_provision), 0)
			  FROM provision_runs
			 WHERE status = 'posted'
			   AND (period_month IS NULL OR period_month < $1)
		`, period).Scan(&prevProvision); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO provision_runs (
			  id, tenant_id, as_of_date, period_month,
			  status, loans_classified, total_outstanding, total_provision,
			  previous_provision, movement,
			  notes, computed_at, created_by
			) VALUES ($1, $2, $3, $4, 'draft', 0, 0, 0, $5, 0, $6, now(), $7)
		`, runID, tenantID, *snapshotAsOf, period, prevProvision, notes, userID); err != nil {
			return err
		}

		for _, d := range drafts {
			provision := d.outstanding.Mul(d.rate).Round(2)
			totalProvision = totalProvision.Add(provision)
			// previousProvision per line — best-effort lookup from
			// the most recent posted v2 run for this loan; null is
			// acceptable for a first-time line.
			var prevLineProvision decimal.Decimal
			_ = tx.QueryRow(ctx, `
				SELECT prl.provision_amount
				  FROM provision_run_lines prl
				  JOIN provision_runs pr ON pr.id = prl.run_id
				 WHERE prl.loan_id = $1
				   AND pr.status = 'posted'
				 ORDER BY pr.period_month DESC NULLS LAST, pr.as_of_date DESC
				 LIMIT 1
			`, d.loanID).Scan(&prevLineProvision)

			delta := provision.Sub(prevLineProvision)
			if _, err := tx.Exec(ctx, `
				INSERT INTO provision_run_lines (
				  tenant_id, run_id, loan_id, counterparty_id, loan_no, product_id,
				  days_past_due, classification, classification_ifrs9_stage,
				  outstanding, provision_rate, provision_amount,
				  previous_provision, delta
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			`,
				tenantID, runID, d.loanID, d.cpID, d.loanNo, d.productID,
				d.dpd, d.sasra, d.stage,
				d.outstanding, d.rate, provision,
				prevLineProvision, delta,
			); err != nil {
				return err
			}
		}

		movement := totalProvision.Sub(prevProvision)
		if _, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET loans_classified = $2,
			       total_outstanding = $3,
			       total_provision = $4,
			       movement = $5,
			       updated_at = now()
			 WHERE id = $1
		`, runID, len(drafts), totalOutstanding, totalProvision, movement); err != nil {
			return err
		}

		row := tx.QueryRow(ctx, `SELECT `+provRunCols+` FROM provision_runs WHERE id = $1`, runID)
		r, err := scanProvRun(row)
		if err != nil {
			return err
		}
		run = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// CancelRunV2 marks a draft (or computed) run as cancelled with a
// reason. Posted runs cannot be cancelled — supersede them instead.
func (s *LoanProvisioningV2Store) CancelRunV2(
	ctx context.Context, tenantID, runID uuid.UUID, userID uuid.UUID, reason string,
) (*domain.ProvisionRun, error) {
	var run *domain.ProvisionRun
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM provision_runs WHERE id = $1`, runID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProvRunNotFound
		}
		if err != nil {
			return err
		}
		if status != string(domain.ProvisionDraft) && status != string(domain.ProvisionComputed) {
			return ErrProvRunNotCancellable
		}
		if _, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'cancelled',
			       cancelled_at = now(),
			       cancelled_by = $2,
			       cancel_reason = $3,
			       updated_at = now()
			 WHERE id = $1
		`, runID, userID, reason); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `SELECT `+provRunCols+` FROM provision_runs WHERE id = $1`, runID)
		r, err := scanProvRun(row)
		if err != nil {
			return err
		}
		run = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// MarkPostedV2 sets status=posted on a draft run after the JE has been
// posted to the GL. Parallel to legacy MarkPosted but accepts both a
// ref string AND a JE uuid for the new accounting path.
func (s *LoanProvisioningV2Store) MarkPostedV2(
	ctx context.Context, tenantID, runID uuid.UUID,
	journalRef string, journalID *uuid.UUID, userID uuid.UUID,
) (*domain.ProvisionRun, error) {
	var run *domain.ProvisionRun
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM provision_runs WHERE id = $1`, runID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProvRunNotFound
		}
		if err != nil {
			return err
		}
		// Accept draft OR computed — the v2 flow uses 'draft' but a
		// legacy 'computed' run posted through the v2 endpoint should
		// still work (defensive).
		if status != string(domain.ProvisionDraft) && status != string(domain.ProvisionComputed) {
			return ErrProvRunNotPostable
		}
		if _, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'posted',
			       journal_entry_ref = $2,
			       journal_entry_id = $3,
			       posted_at = now(),
			       posted_by = $4,
			       updated_at = now()
			 WHERE id = $1
		`, runID, journalRef, journalID, userID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `SELECT `+provRunCols+` FROM provision_runs WHERE id = $1`, runID)
		r, err := scanProvRun(row)
		if err != nil {
			return err
		}
		run = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}
