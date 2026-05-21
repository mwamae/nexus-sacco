// Loan guarantee + collateral + document persistence.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type LoanGuaranteeStore struct {
	pool *pgxpool.Pool
}

func NewLoanGuaranteeStore(pool *pgxpool.Pool) *LoanGuaranteeStore {
	return &LoanGuaranteeStore{pool: pool}
}

// ─────────── Guarantees ───────────

func (s *LoanGuaranteeStore) CreateTx(ctx context.Context, tx pgx.Tx, g *domain.LoanGuarantee) (*domain.LoanGuarantee, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_guarantees (
			tenant_id, application_id, guarantor_member_id, amount_guaranteed,
			status, requested_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, 'pending_consent', $4
		)
		RETURNING id, tenant_id, application_id, loan_id, guarantor_member_id,
		          amount_guaranteed, status, requested_at, requested_by,
		          responded_at, released_at, called_upon_at, decline_reason, notes
	`, g.ApplicationID, g.GuarantorMemberID, g.AmountGuaranteed, g.RequestedBy)
	var out domain.LoanGuarantee
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.ApplicationID, &out.LoanID, &out.GuarantorMemberID,
		&out.AmountGuaranteed, &out.Status, &out.RequestedAt, &out.RequestedBy,
		&out.RespondedAt, &out.ReleasedAt, &out.CalledUponAt, &out.DeclineReason, &out.Notes,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *LoanGuaranteeStore) ByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.LoanGuarantee, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, application_id, loan_id, guarantor_member_id,
		       amount_guaranteed, status, requested_at, requested_by,
		       responded_at, released_at, called_upon_at, decline_reason, notes
		FROM loan_guarantees WHERE application_id = $1
		ORDER BY requested_at
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanGuarantee
	for rows.Next() {
		var g domain.LoanGuarantee
		if err := rows.Scan(
			&g.ID, &g.TenantID, &g.ApplicationID, &g.LoanID, &g.GuarantorMemberID,
			&g.AmountGuaranteed, &g.Status, &g.RequestedAt, &g.RequestedBy,
			&g.RespondedAt, &g.ReleasedAt, &g.CalledUponAt, &g.DeclineReason, &g.Notes,
		); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RespondTx records a guarantor's accept/decline. Idempotent on the
// terminal status.
func (s *LoanGuaranteeStore) RespondTx(
	ctx context.Context, tx pgx.Tx,
	guaranteeID uuid.UUID,
	accepted bool, declineReason *string,
) (*domain.LoanGuarantee, error) {
	status := "accepted"
	if !accepted {
		status = "declined"
	}
	row := tx.QueryRow(ctx, `
		UPDATE loan_guarantees
		   SET status = $2,
		       responded_at = now(),
		       decline_reason = $3
		 WHERE id = $1
		   AND status = 'pending_consent'
		 RETURNING id, tenant_id, application_id, loan_id, guarantor_member_id,
		           amount_guaranteed, status, requested_at, requested_by,
		           responded_at, released_at, called_upon_at, decline_reason, notes
	`, guaranteeID, status, declineReason)
	var g domain.LoanGuarantee
	err := row.Scan(
		&g.ID, &g.TenantID, &g.ApplicationID, &g.LoanID, &g.GuarantorMemberID,
		&g.AmountGuaranteed, &g.Status, &g.RequestedAt, &g.RequestedBy,
		&g.RespondedAt, &g.ReleasedAt, &g.CalledUponAt, &g.DeclineReason, &g.Notes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("guarantee not found or not pending consent")
	}
	return &g, err
}

// ExposureForMemberTx returns the total amount this member is currently
// guaranteeing across all active/pending guarantees. Used to check
// over-exposure when registering a new guarantee.
func (s *LoanGuaranteeStore) ExposureForMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_guaranteed), 0)
		FROM loan_guarantees
		WHERE guarantor_member_id = $1
		  AND status IN ('pending_consent', 'accepted')
	`, memberID).Scan(&total)
	return total, err
}

// BackfillLoanIDTx — once a loan record is created on offer acceptance,
// stamp loan_id onto every guarantee + collateral for the application.
func (s *LoanGuaranteeStore) BackfillLoanIDTx(ctx context.Context, tx pgx.Tx, appID, loanID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE loan_guarantees SET loan_id = $2 WHERE application_id = $1`, appID, loanID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE loan_collateral SET loan_id = $2 WHERE application_id = $1`, appID, loanID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE loan_documents SET loan_id = $2 WHERE application_id = $1`, appID, loanID); err != nil {
		return err
	}
	return nil
}

// ─────────── Collateral ───────────

func (s *LoanGuaranteeStore) CreateCollateralTx(ctx context.Context, tx pgx.Tx, c *domain.LoanCollateralItem) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_collateral (
			tenant_id, application_id, kind, description, estimated_value,
			forced_sale_value, valuation_date, valuation_path, ownership_path, notes
		) VALUES (
			current_tenant_id(), $1, $2, $3, $4,
			$5, $6, $7, $8, $9
		)
		RETURNING id, tenant_id, application_id, loan_id, kind, description, estimated_value,
		          forced_sale_value, valuation_date, valuation_path, ownership_path, status, notes, created_at
	`, c.ApplicationID, string(c.Kind), c.Description, c.EstimatedValue,
		c.ForcedSaleValue, c.ValuationDate, c.ValuationPath, c.OwnershipPath, c.Notes)
	var out domain.LoanCollateralItem
	if err := row.Scan(
		&out.ID, &out.TenantID, &out.ApplicationID, &out.LoanID, &out.Kind, &out.Description, &out.EstimatedValue,
		&out.ForcedSaleValue, &out.ValuationDate, &out.ValuationPath, &out.OwnershipPath, &out.Status, &out.Notes, &out.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *LoanGuaranteeStore) CollateralByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.LoanCollateralItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, application_id, loan_id, kind, description, estimated_value,
		       forced_sale_value, valuation_date, valuation_path, ownership_path, status, notes, created_at
		FROM loan_collateral WHERE application_id = $1
		ORDER BY created_at
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanCollateralItem
	for rows.Next() {
		var c domain.LoanCollateralItem
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.ApplicationID, &c.LoanID, &c.Kind, &c.Description, &c.EstimatedValue,
			&c.ForcedSaleValue, &c.ValuationDate, &c.ValuationPath, &c.OwnershipPath, &c.Status, &c.Notes, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
