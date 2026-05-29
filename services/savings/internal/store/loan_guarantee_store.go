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
	// Phase D sub-PR 3: g.GuarantorMemberID is a counterparty.id directly
	// (the loan-application request payload now carries counterparty.id).
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_guarantees (
			tenant_id, application_id, guarantor_counterparty_id, amount_guaranteed,
			status, requested_by
		) VALUES (
			current_tenant_id(), $1, $2, $3, 'pending_consent', $4
		)
		RETURNING id, tenant_id, application_id, loan_id, guarantor_counterparty_id,
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
	// LEFT JOIN counterparty_directory so the UI can render the
	// guarantor's display name + member_no without a follow-up call
	// per row. LEFT (not INNER) because a counterparty might lack a
	// directory row in degraded data states; we still want the
	// guarantee row to render with empty display fields rather than
	// disappear entirely.
	rows, err := tx.Query(ctx, `
		SELECT g.id, g.tenant_id, g.application_id, g.loan_id, g.guarantor_counterparty_id,
		       g.amount_guaranteed, g.status, g.requested_at, g.requested_by,
		       g.responded_at, g.released_at, g.called_upon_at, g.decline_reason, g.notes,
		       COALESCE(cd.full_name, '') AS guarantor_name,
		       COALESCE(cd.member_no, '') AS guarantor_member_no
		  FROM loan_guarantees g
		  LEFT JOIN counterparty_directory cd
		    ON cd.counterparty_id = g.guarantor_counterparty_id
		 WHERE g.application_id = $1
		 ORDER BY g.requested_at
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
			&g.GuarantorName, &g.GuarantorMemberNo,
		); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RespondTx records a guarantor's accept/decline. Idempotent on the
// terminal status.
//
// `proofDocID` is the optional loan_documents row attached as written
// consent evidence — set when an admin captures consent on the
// guarantor's behalf and uploads a signed form. Member self-service
// passes nil (the JWT identifies the consenting member).
//
// `respondedBy` is required: identity-service user id of whoever
// clicked. For admin path it's the officer; for portal path it's
// the member's user account.
func (s *LoanGuaranteeStore) RespondTx(
	ctx context.Context, tx pgx.Tx,
	guaranteeID uuid.UUID,
	accepted bool, declineReason *string,
	proofDocID *uuid.UUID, respondedBy uuid.UUID,
) (*domain.LoanGuarantee, error) {
	status := "accepted"
	if !accepted {
		status = "declined"
	}
	row := tx.QueryRow(ctx, `
		UPDATE loan_guarantees
		   SET status            = $2,
		       responded_at      = now(),
		       decline_reason    = $3,
		       proof_document_id = COALESCE($4, proof_document_id),
		       responded_by      = $5
		 WHERE id = $1
		   AND status = 'pending_consent'
		 RETURNING id, tenant_id, application_id, loan_id, guarantor_counterparty_id,
		           amount_guaranteed, status, requested_at, requested_by,
		           responded_at, released_at, called_upon_at, decline_reason, notes,
		           '' AS guarantor_name, '' AS guarantor_member_no
	`, guaranteeID, status, declineReason, proofDocID, respondedBy)
	var g domain.LoanGuarantee
	err := row.Scan(
		&g.ID, &g.TenantID, &g.ApplicationID, &g.LoanID, &g.GuarantorMemberID,
		&g.AmountGuaranteed, &g.Status, &g.RequestedAt, &g.RequestedBy,
		&g.RespondedAt, &g.ReleasedAt, &g.CalledUponAt, &g.DeclineReason, &g.Notes,
		&g.GuarantorName, &g.GuarantorMemberNo,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("guarantee not found or not pending consent")
	}
	return &g, err
}

// GuarantorshipRow joins a loan_guarantees row with the borrower's
// name + loan number for the Member Profile → People tab. Lives at
// this level so callers don't have to issue follow-up queries to
// resolve the foreign keys.
type GuarantorshipRow struct {
	domain.LoanGuarantee
	LoanNo         *string `json:"loan_no"`
	ApplicationNo  string  `json:"application_no"`
	BorrowerID     uuid.UUID `json:"borrower_member_id"`
	BorrowerName   string  `json:"borrower_full_name"`
	ProductCode    string  `json:"product_code"`
	ProductName    string  `json:"product_name"`
}

// ByGuarantorMemberTx returns every loan-guarantee this member is on,
// joined with the borrower's identity + loan reference. Sorted newest
// first. Empty slice (nil error) if the member is not guaranteeing
// anyone, so the caller can render a "no guarantorships on record"
// empty state without distinguishing the no-rows case at the SQL
// level.
func (s *LoanGuaranteeStore) ByGuarantorMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) ([]GuarantorshipRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT g.id, g.tenant_id, g.application_id, g.loan_id, g.guarantor_counterparty_id,
		       g.amount_guaranteed, g.status, g.requested_at, g.requested_by,
		       g.responded_at, g.released_at, g.called_upon_at, g.decline_reason, g.notes,
		       l.loan_no,
		       a.application_no,
		       a.counterparty_id        AS borrower_member_id,
		       cd.full_name             AS borrower_full_name,
		       p.code                   AS product_code,
		       p.name                   AS product_name
		  FROM loan_guarantees g
		  JOIN loan_applications        a ON a.id = g.application_id
		  -- Was: JOIN members m ON m.id = a.counterparty_id — broken
		  -- since the Phase D refactor (members.id ≠ counterparties.id).
		  -- counterparty_directory bridges both individual + org
		  -- borrowers and joins on the correct id.
		  JOIN counterparty_directory   cd ON cd.counterparty_id = a.counterparty_id
		  JOIN loan_products            p ON p.id = a.product_id
		  LEFT JOIN loans       l ON l.id = g.loan_id
		 WHERE g.guarantor_counterparty_id = $1
		 ORDER BY g.requested_at DESC
	`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GuarantorshipRow
	for rows.Next() {
		var r GuarantorshipRow
		if err := rows.Scan(
			&r.ID, &r.TenantID, &r.ApplicationID, &r.LoanID, &r.GuarantorMemberID,
			&r.AmountGuaranteed, &r.Status, &r.RequestedAt, &r.RequestedBy,
			&r.RespondedAt, &r.ReleasedAt, &r.CalledUponAt, &r.DeclineReason, &r.Notes,
			&r.LoanNo, &r.ApplicationNo, &r.BorrowerID, &r.BorrowerName, &r.ProductCode, &r.ProductName,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExposureForMemberTx returns the total amount this member is currently
// guaranteeing across all active/pending guarantees. Used to check
// over-exposure when registering a new guarantee.
func (s *LoanGuaranteeStore) ExposureForMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (decimal.Decimal, error) {
	var total decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_guaranteed), 0)
		FROM loan_guarantees
		WHERE guarantor_counterparty_id = $1
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

// ReleaseByApplicationTx flips every still-committing guarantee row
// for an application to 'released'. Called from the chokepoint
// LoanApplicationStore.UpdateStatusTx whenever an application moves
// to a terminal-non-success state (declined / cancelled / expired /
// offer_declined). Idempotent — already-released rows are skipped.
//
// Returns the count released for observability + a note that callers
// can log; never errors on "nothing to do".
func (s *LoanGuaranteeStore) ReleaseByApplicationTx(
	ctx context.Context, tx pgx.Tx, appID uuid.UUID, reason string,
) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE loan_guarantees
		   SET status      = 'released',
		       released_at = COALESCE(released_at, now()),
		       notes       = COALESCE(notes, '') ||
		                     CASE WHEN COALESCE(notes,'') = '' THEN '' ELSE E'\n' END ||
		                     '[released] ' || $2
		 WHERE application_id = $1
		   AND status IN ('pending_consent','accepted','called_upon')
	`, appID, reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ReleaseByLoanTx releases every still-committing guarantee tied to
// a loan. Called when the loan reaches 'settled' (natural repayment
// chain or manual settle). Idempotent — already-released rows
// skipped.
//
// IMPORTANT: NOT called on 'written_off' loans. A write-off doesn't
// extinguish the guarantor's obligation; the SACCO may still call
// upon them. Release write-off guarantees only via explicit policy.
func (s *LoanGuaranteeStore) ReleaseByLoanTx(
	ctx context.Context, tx pgx.Tx, loanID uuid.UUID, reason string,
) (int, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE loan_guarantees
		   SET status      = 'released',
		       released_at = COALESCE(released_at, now()),
		       notes       = COALESCE(notes, '') ||
		                     CASE WHEN COALESCE(notes,'') = '' THEN '' ELSE E'\n' END ||
		                     '[released] ' || $2
		 WHERE loan_id = $1
		   AND status IN ('pending_consent','accepted','called_upon')
	`, loanID, reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
