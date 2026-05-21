// Provisioning store — drives provision runs end-to-end.
//
// One run = one snapshot of every active loan's classification +
// required provision on a specific as-of date. The store:
//   1. Reads provisioning rates from tenant_operations
//   2. Iterates eligible loans (active / in_arrears / restructured),
//      computes DPD using the same earliest-unpaid-installment logic
//      RecalcDPDTx uses, classifies, multiplies outstanding by rate
//   3. Persists run + one line per loan
//   4. Reports the movement vs the previous posted run so the handler
//      can post the GL delta
//
// Outstanding = principal_balance only. Per IFRS 9 / SASRA, provisions
// are computed on principal exposure; interest/fees/penalties already
// have their own balance accounts and are accrued separately.

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

type ProvisioningStore struct {
	pool *db.Pool
}

func NewProvisioningStore(pool *db.Pool) *ProvisioningStore {
	return &ProvisioningStore{pool: pool}
}

var ErrProvRunNotFound = errors.New("provisioning: run not found")
var ErrProvAlreadyPosted = errors.New("provisioning: a posted run already exists for this date; supersede it first")
var ErrProvNotComputed = errors.New("provisioning: run is not in computed state")

const provRunCols = `
	id, tenant_id, as_of_date, status,
	loans_classified, total_outstanding, total_provision,
	previous_provision, movement,
	journal_entry_ref, notes,
	computed_at, posted_at, posted_by,
	created_at, created_by, updated_at
`

func scanProvRun(row pgx.Row) (*domain.ProvisionRun, error) {
	var r domain.ProvisionRun
	err := row.Scan(
		&r.ID, &r.TenantID, &r.AsOfDate, &r.Status,
		&r.LoansClassified, &r.TotalOutstanding, &r.TotalProvision,
		&r.PreviousProvision, &r.Movement,
		&r.JournalEntryRef, &r.Notes,
		&r.ComputedAt, &r.PostedAt, &r.PostedBy,
		&r.CreatedAt, &r.CreatedBy, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ─────────── Compute + persist ───────────

// CreateRun does the heavy lifting in one transaction:
//   - blocks if an already-posted run exists for the same as_of_date
//   - reads rates + DPD thresholds
//   - snapshots every eligible loan into run_lines (with classification
//     recomputed at as-of date so a stale loans.days_past_due doesn't
//     skew the report)
//   - computes movement vs the most recent posted run
//   - returns the run in 'computed' status, ready to post
func (s *ProvisioningStore) CreateRun(
	ctx context.Context,
	tenantID uuid.UUID,
	asOf time.Time,
	notes *string,
	userID uuid.UUID,
) (*domain.ProvisionRun, error) {
	var run *domain.ProvisionRun
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// A posted run for the same date blocks creation.
		var existing int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM provision_runs
			 WHERE as_of_date = $1 AND status = 'posted'
		`, asOf).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return ErrProvAlreadyPosted
		}

		// Any computed/pending/failed run for the same date is overwritten —
		// the new run becomes the live snapshot.
		if _, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'superseded', updated_at = now()
			 WHERE as_of_date = $1 AND status IN ('pending', 'computed', 'failed')
		`, asOf); err != nil {
			return err
		}

		var rates domain.ProvisioningRates
		if err := tx.QueryRow(ctx, `
			SELECT provisioning_watch_pct, provisioning_substandard_pct,
			       provisioning_doubtful_pct, provisioning_loss_pct
			  FROM tenant_operations
		`).Scan(&rates.Watch, &rates.Substandard, &rates.Doubtful, &rates.Loss); err != nil {
			return fmt.Errorf("read rates: %w", err)
		}
		var sub, doub, loss int
		if err := tx.QueryRow(ctx, `
			SELECT dpd_substandard_days, dpd_doubtful_days, dpd_loss_days
			  FROM tenant_operations
		`).Scan(&sub, &doub, &loss); err != nil {
			return fmt.Errorf("read dpd thresholds: %w", err)
		}

		runID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO provision_runs (id, tenant_id, as_of_date, status, notes, created_by)
			VALUES ($1, $2, $3, 'pending', $4, $5)
		`, runID, tenantID, asOf, notes, userID); err != nil {
			return err
		}

		// Iterate eligible loans. We re-derive DPD from the schedule rather
		// than trusting loans.days_past_due — the latter is daily-stale and
		// a provisioning run must reflect the as-of date precisely.
		rows, err := tx.Query(ctx, `
			SELECT l.id, l.member_id, l.loan_no, l.principal_balance, l.arrears_classification,
			       (SELECT MIN(due_date) FROM loan_repayment_schedule
			         WHERE loan_id = l.id
			           AND status NOT IN ('paid', 'cancelled')
			           AND due_date <= $1) AS earliest_unpaid_due
			  FROM loans l
			 WHERE l.status IN ('active', 'in_arrears', 'restructured')
		`, asOf)
		if err != nil {
			return fmt.Errorf("query loans: %w", err)
		}

		type lineDraft struct {
			loanID, memberID   uuid.UUID
			loanNo             string
			outstanding        decimal.Decimal
			dpd                int
			classification     string
			prevClassification string
		}
		var drafts []lineDraft
		var totalOutstanding, totalProvision decimal.Decimal
		for rows.Next() {
			var d lineDraft
			var earliestDue *time.Time
			if err := rows.Scan(&d.loanID, &d.memberID, &d.loanNo, &d.outstanding, &d.prevClassification, &earliestDue); err != nil {
				rows.Close()
				return err
			}
			if d.outstanding.IsZero() {
				continue
			}
			if earliestDue != nil {
				diff := int(asOf.Sub(*earliestDue).Hours() / 24)
				if diff > 0 {
					d.dpd = diff
				}
			}
			d.classification = ClassifyDPD(d.dpd, sub, doub, loss)
			drafts = append(drafts, d)
			totalOutstanding = totalOutstanding.Add(d.outstanding)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, d := range drafts {
			ratePct := rates.RateFor(d.classification)
			rate := ratePct.Div(decimal.NewFromInt(100))
			provision := d.outstanding.Mul(rate).Round(2)
			totalProvision = totalProvision.Add(provision)

			if _, err := tx.Exec(ctx, `
				INSERT INTO provision_run_lines (
				  tenant_id, run_id, loan_id, member_id, loan_no,
				  days_past_due, classification, outstanding,
				  provision_rate, provision_amount, previous_classification
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			`, tenantID, runID, d.loanID, d.memberID, d.loanNo,
				d.dpd, d.classification, d.outstanding, rate, provision, d.prevClassification); err != nil {
				return err
			}
		}

		var prevProvision decimal.Decimal
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(total_provision), 0)
			  FROM provision_runs
			 WHERE status = 'posted' AND as_of_date < $1
		`, asOf).Scan(&prevProvision); err != nil {
			return err
		}
		movement := totalProvision.Sub(prevProvision)

		if _, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'computed',
			       loans_classified = $2,
			       total_outstanding = $3,
			       total_provision = $4,
			       previous_provision = $5,
			       movement = $6,
			       computed_at = now(),
			       updated_at = now()
			 WHERE id = $1
		`, runID, len(drafts), totalOutstanding, totalProvision, prevProvision, movement); err != nil {
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

// MarkPosted records the journal entry reference and finalises the
// run. Caller is responsible for ensuring the GL entry was actually
// posted before invoking — this method assumes success.
func (s *ProvisioningStore) MarkPosted(
	ctx context.Context, tenantID, runID uuid.UUID, journalRef string, userID uuid.UUID,
) (*domain.ProvisionRun, error) {
	var run *domain.ProvisionRun
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx,
			`SELECT status FROM provision_runs WHERE id = $1`,
			runID,
		).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProvRunNotFound
			}
			return err
		}
		if status != string(domain.ProvisionComputed) {
			return ErrProvNotComputed
		}

		if _, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'posted',
			       journal_entry_ref = $2,
			       posted_at = now(),
			       posted_by = $3,
			       updated_at = now()
			 WHERE id = $1
		`, runID, journalRef, userID); err != nil {
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

func (s *ProvisioningStore) MarkFailed(ctx context.Context, tenantID, runID uuid.UUID, reason string) error {
	return s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'failed',
			       notes = COALESCE(notes,'') || E'\n[POST FAILED] ' || $2,
			       updated_at = now()
			 WHERE id = $1
		`, runID, reason)
		return err
	})
}

// ─────────── Read ───────────

func (s *ProvisioningStore) Get(ctx context.Context, tenantID, runID uuid.UUID) (*domain.ProvisionRun, error) {
	var run *domain.ProvisionRun
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+provRunCols+` FROM provision_runs WHERE id = $1`, runID)
		r, err := scanProvRun(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProvRunNotFound
		}
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

func (s *ProvisioningStore) List(ctx context.Context, tenantID uuid.UUID, limit int) ([]domain.ProvisionRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []domain.ProvisionRun{}
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+provRunCols+` FROM provision_runs ORDER BY as_of_date DESC, created_at DESC LIMIT $1`,
			limit,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanProvRun(rows)
			if err != nil {
				return err
			}
			out = append(out, *r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ProvisioningStore) LinesByRun(ctx context.Context, tenantID, runID uuid.UUID) ([]domain.ProvisionRunLine, error) {
	out := []domain.ProvisionRunLine{}
	err := s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, run_id, loan_id, member_id, loan_no,
			       days_past_due, classification, outstanding,
			       provision_rate, provision_amount,
			       previous_classification, previous_provision
			  FROM provision_run_lines
			 WHERE run_id = $1
			 ORDER BY CASE classification
			            WHEN 'loss' THEN 1 WHEN 'doubtful' THEN 2
			            WHEN 'substandard' THEN 3 WHEN 'watch' THEN 4
			            ELSE 5 END,
			          outstanding DESC
		`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.ProvisionRunLine
			if err := rows.Scan(
				&l.ID, &l.RunID, &l.LoanID, &l.MemberID, &l.LoanNo,
				&l.DaysPastDue, &l.Classification, &l.Outstanding,
				&l.ProvisionRate, &l.ProvisionAmount,
				&l.PreviousClassification, &l.PreviousProvision,
			); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Supersede lets an admin invalidate a posted run so a re-run for the
// same date is allowed. The GL entry already posted is *not* reversed
// here — the operator is expected to post a manual reversal first.
func (s *ProvisioningStore) Supersede(ctx context.Context, tenantID, runID uuid.UUID) error {
	return s.pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, `
			UPDATE provision_runs
			   SET status = 'superseded', updated_at = now()
			 WHERE id = $1 AND status = 'posted'
		`, runID)
		if err != nil {
			return err
		}
		if cmd.RowsAffected() == 0 {
			return ErrProvRunNotFound
		}
		return nil
	})
}
