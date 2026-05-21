// Amortization snapshot for loan applications — computed at submission
// time and again on approval (so the reviewer sees what they're approving
// and the applicant sees what they will actually pay).

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type ScheduleSnapshot struct {
	GeneratedAt       time.Time             `json:"generated_at"`
	Principal         decimal.Decimal       `json:"principal"`
	InterestRatePct   decimal.Decimal       `json:"interest_rate_pct"`
	TermMonths        int                   `json:"term_months"`
	GracePeriodMonths int                   `json:"grace_period_months"`
	InterestMethod    domain.LoanInterestMethod  `json:"interest_method"`
	RepaymentMethod   domain.LoanRepaymentMethod `json:"repayment_method"`
	StartDate         time.Time             `json:"start_date"`
	FirstDueDate      time.Time             `json:"first_due_date"`
	Rows              []domain.ScheduleRow  `json:"rows"`
	TotalPrincipal    decimal.Decimal       `json:"total_principal"`
	TotalInterest     decimal.Decimal       `json:"total_interest"`
	TotalPayable      decimal.Decimal       `json:"total_payable"`
	Installment       decimal.Decimal       `json:"installment"` // first installment (for constant-payment methods)
}

// ComputeScheduleSnapshot is a pure function — no DB, no clock for
// schedule math other than the supplied start_date.
func ComputeScheduleSnapshot(
	principal, ratePct decimal.Decimal,
	termMonths, graceMonths int,
	interestMethod domain.LoanInterestMethod,
	repayMethod domain.LoanRepaymentMethod,
	startDate time.Time,
) *ScheduleSnapshot {
	if termMonths <= 0 || principal.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	rows := domain.GenerateSchedule(
		principal, ratePct, termMonths, graceMonths,
		startDate, interestMethod, repayMethod,
	)
	snap := &ScheduleSnapshot{
		GeneratedAt:       time.Now().UTC(),
		Principal:         principal,
		InterestRatePct:   ratePct,
		TermMonths:        termMonths,
		GracePeriodMonths: graceMonths,
		InterestMethod:    interestMethod,
		RepaymentMethod:   repayMethod,
		StartDate:         startDate,
		Rows:              rows,
	}
	if len(rows) > 0 {
		snap.FirstDueDate = rows[0].DueDate
		snap.Installment = rows[0].TotalDue
		for _, r := range rows {
			snap.TotalPrincipal = snap.TotalPrincipal.Add(r.PrincipalDue)
			snap.TotalInterest = snap.TotalInterest.Add(r.InterestDue)
			snap.TotalPayable = snap.TotalPayable.Add(r.TotalDue)
		}
	}
	return snap
}

// StoreAppScheduleSnapshotTx persists the snapshot on the loan_applications
// row. Replaces any prior snapshot.
func (s *LoanApplicationStore) StoreAppScheduleSnapshotTx(
	ctx context.Context, tx pgx.Tx,
	appID uuid.UUID,
	snap *ScheduleSnapshot,
) error {
	if snap == nil {
		return nil
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE loan_applications SET
			repayment_schedule_snapshot       = $2::jsonb,
			repayment_schedule_snapshot_at    = now(),
			repayment_schedule_total_payable  = $3,
			repayment_schedule_total_interest = $4,
			repayment_schedule_installment    = $5
		WHERE id = $1
	`, appID, payload, snap.TotalPayable, snap.TotalInterest, snap.Installment)
	return err
}

// GetAppScheduleSnapshotTx returns the stored snapshot, or nil if none.
func (s *LoanApplicationStore) GetAppScheduleSnapshotTx(
	ctx context.Context, tx pgx.Tx,
	appID uuid.UUID,
) (*ScheduleSnapshot, error) {
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT repayment_schedule_snapshot
		FROM loan_applications WHERE id = $1
	`, appID).Scan(&payload)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, nil
	}
	var snap ScheduleSnapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
