// Phase 1.5b — store helpers for charge / insurance / custody / auction.

package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type CollateralAdvancedStore struct {
	pool *pgxpool.Pool
}

func NewCollateralAdvancedStore(pool *pgxpool.Pool) *CollateralAdvancedStore {
	return &CollateralAdvancedStore{pool: pool}
}

// ─────────── Charge registration ───────────

type RecordChargeInput struct {
	CollateralID    uuid.UUID
	Registry        string
	Reference       string
	RegisteredAt    time.Time
	RegisteredBy    uuid.UUID
	CertificatePath *string
}

func (s *CollateralAdvancedStore) RecordChargeTx(ctx context.Context, tx pgx.Tx, in RecordChargeInput) error {
	tag, err := tx.Exec(ctx, `
		UPDATE loan_collateral SET
		  charge_registry         = $2::charge_registry,
		  charge_reference        = $3,
		  charge_registered_at    = $4,
		  charge_registered_by    = $5,
		  charge_certificate_path = $6
		 WHERE id = $1
	`, in.CollateralID, in.Registry, in.Reference, in.RegisteredAt, in.RegisteredBy, in.CertificatePath)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCollateralNotFound
	}
	return nil
}

type DischargeChargeInput struct {
	CollateralID  uuid.UUID
	DischargeRef  string
	DischargedAt  time.Time
}

func (s *CollateralAdvancedStore) DischargeChargeTx(ctx context.Context, tx pgx.Tx, in DischargeChargeInput) error {
	tag, err := tx.Exec(ctx, `
		UPDATE loan_collateral SET
		  charge_discharge_ref = $2,
		  charge_discharged_at = $3
		 WHERE id = $1 AND charge_registered_at IS NOT NULL
	`, in.CollateralID, in.DischargeRef, in.DischargedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCollateralWrongState
	}
	return nil
}

// ─────────── Insurance ───────────

type RecordInsuranceInput struct {
	CollateralID   uuid.UUID
	ProviderName   string
	PolicyNo       string
	EffectiveFrom  time.Time
	EffectiveTo    time.Time
	PremiumAmount  *decimal.Decimal
	SumInsured     decimal.Decimal
	PolicyDocPath  *string
	Notes          *string
	CreatedBy      uuid.UUID
}

func (s *CollateralAdvancedStore) RecordInsuranceTx(ctx context.Context, tx pgx.Tx, in RecordInsuranceInput) (*domain.CollateralInsurancePolicy, error) {
	var p domain.CollateralInsurancePolicy
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_insurance_policies (
		  tenant_id, collateral_id, provider_name, policy_no,
		  effective_from, effective_to, premium_amount, sum_insured,
		  policy_doc_path, notes, created_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3,
		  $4, $5, $6, $7,
		  $8, $9, $10
		)
		RETURNING id, tenant_id, collateral_id, provider_name, policy_no,
		          effective_from, effective_to, premium_amount, sum_insured,
		          status, is_current, policy_doc_path, notes, created_at, created_by
	`, in.CollateralID, in.ProviderName, in.PolicyNo,
		in.EffectiveFrom, in.EffectiveTo, in.PremiumAmount, in.SumInsured,
		in.PolicyDocPath, in.Notes, in.CreatedBy,
	).Scan(
		&p.ID, &p.TenantID, &p.CollateralID, &p.ProviderName, &p.PolicyNo,
		&p.EffectiveFrom, &p.EffectiveTo, &p.PremiumAmount, &p.SumInsured,
		&p.Status, &p.IsCurrent, &p.PolicyDocPath, &p.Notes, &p.CreatedAt, &p.CreatedBy,
	)
	if err != nil {
		if isCollateralLienUniqueViolation(err) {
			return nil, errors.New("a current insurance policy already exists; supersede flow not yet wired")
		}
		return nil, err
	}
	// Supersede any prior current policy.
	if _, err := tx.Exec(ctx, `
		UPDATE collateral_insurance_policies
		   SET is_current = false
		 WHERE collateral_id = $1 AND id <> $2 AND is_current = true
	`, in.CollateralID, p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *CollateralAdvancedStore) CurrentInsuranceTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) (*domain.CollateralInsurancePolicy, error) {
	var p domain.CollateralInsurancePolicy
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, collateral_id, provider_name, policy_no,
		       effective_from, effective_to, premium_amount, sum_insured,
		       status, is_current, policy_doc_path, notes, created_at, created_by
		  FROM collateral_insurance_policies
		 WHERE collateral_id = $1 AND is_current = true
	`, collateralID).Scan(
		&p.ID, &p.TenantID, &p.CollateralID, &p.ProviderName, &p.PolicyNo,
		&p.EffectiveFrom, &p.EffectiveTo, &p.PremiumAmount, &p.SumInsured,
		&p.Status, &p.IsCurrent, &p.PolicyDocPath, &p.Notes, &p.CreatedAt, &p.CreatedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // no current policy — not an error
	}
	return &p, err
}

func (s *CollateralAdvancedStore) InsuranceHistoryTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) ([]domain.CollateralInsurancePolicy, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, provider_name, policy_no,
		       effective_from, effective_to, premium_amount, sum_insured,
		       status, is_current, policy_doc_path, notes, created_at, created_by
		  FROM collateral_insurance_policies
		 WHERE collateral_id = $1
		 ORDER BY effective_to DESC, created_at DESC
	`, collateralID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollateralInsurancePolicy
	for rows.Next() {
		var p domain.CollateralInsurancePolicy
		if err := rows.Scan(
			&p.ID, &p.TenantID, &p.CollateralID, &p.ProviderName, &p.PolicyNo,
			&p.EffectiveFrom, &p.EffectiveTo, &p.PremiumAmount, &p.SumInsured,
			&p.Status, &p.IsCurrent, &p.PolicyDocPath, &p.Notes, &p.CreatedAt, &p.CreatedBy,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─────────── Custody ───────────

type RecordCustodyInput struct {
	CollateralID          uuid.UUID
	DocumentKind          string
	Movement              string // 'checked_in' | 'checked_out' | 'returned_to_borrower'
	MovementBy            uuid.UUID
	CustodianUserID       *uuid.UUID
	BorrowerSignaturePath *string
	LocationCode          *string
	Notes                 *string
}

func (s *CollateralAdvancedStore) RecordCustodyTx(ctx context.Context, tx pgx.Tx, in RecordCustodyInput) (*domain.CollateralCustodyMovement, error) {
	var m domain.CollateralCustodyMovement
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_document_custody (
		  tenant_id, collateral_id, document_kind, movement,
		  movement_by, custodian_user_id, borrower_signature_path,
		  location_code, notes
		) VALUES (
		  current_tenant_id(), $1, $2, $3,
		  $4, $5, $6, $7, $8
		)
		RETURNING id, tenant_id, collateral_id, document_kind, movement,
		          movement_at, movement_by, custodian_user_id,
		          borrower_signature_path, location_code, notes
	`, in.CollateralID, in.DocumentKind, in.Movement,
		in.MovementBy, in.CustodianUserID, in.BorrowerSignaturePath,
		in.LocationCode, in.Notes,
	).Scan(
		&m.ID, &m.TenantID, &m.CollateralID, &m.DocumentKind, &m.Movement,
		&m.MovementAt, &m.MovementBy, &m.CustodianUserID,
		&m.BorrowerSignaturePath, &m.LocationCode, &m.Notes,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *CollateralAdvancedStore) CustodyTimelineTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) ([]domain.CollateralCustodyMovement, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, document_kind, movement,
		       movement_at, movement_by, custodian_user_id,
		       borrower_signature_path, location_code, notes
		  FROM collateral_document_custody
		 WHERE collateral_id = $1
		 ORDER BY movement_at DESC
	`, collateralID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollateralCustodyMovement
	for rows.Next() {
		var m domain.CollateralCustodyMovement
		if err := rows.Scan(
			&m.ID, &m.TenantID, &m.CollateralID, &m.DocumentKind, &m.Movement,
			&m.MovementAt, &m.MovementBy, &m.CustodianUserID,
			&m.BorrowerSignaturePath, &m.LocationCode, &m.Notes,
		); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ─────────── Auction events ───────────

type RecordAuctionEventInput struct {
	CollateralID   uuid.UUID
	LoanID         *uuid.UUID
	EventKind      string
	OccurredAt     time.Time
	Amount         *decimal.Decimal
	BuyerDetails   *string
	AuctioneerName *string
	Notes          *string
	DocPath        *string
	CreatedBy      uuid.UUID
}

func (s *CollateralAdvancedStore) RecordAuctionEventTx(ctx context.Context, tx pgx.Tx, in RecordAuctionEventInput) (*domain.CollateralAuctionEvent, error) {
	var e domain.CollateralAuctionEvent
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_auction_events (
		  tenant_id, collateral_id, loan_id, event_kind,
		  occurred_at, amount, buyer_details, auctioneer_name,
		  notes, doc_path, created_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3,
		  $4, $5, $6, $7,
		  $8, $9, $10
		)
		RETURNING id, tenant_id, collateral_id, loan_id, event_kind,
		          occurred_at, amount, buyer_details, auctioneer_name,
		          notes, doc_path, created_by
	`, in.CollateralID, in.LoanID, in.EventKind,
		in.OccurredAt, in.Amount, in.BuyerDetails, in.AuctioneerName,
		in.Notes, in.DocPath, in.CreatedBy,
	).Scan(
		&e.ID, &e.TenantID, &e.CollateralID, &e.LoanID, &e.EventKind,
		&e.OccurredAt, &e.Amount, &e.BuyerDetails, &e.AuctioneerName,
		&e.Notes, &e.DocPath, &e.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *CollateralAdvancedStore) AuctionEventsByCollateralTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) ([]domain.CollateralAuctionEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, loan_id, event_kind,
		       occurred_at, amount, buyer_details, auctioneer_name,
		       notes, doc_path, created_by
		  FROM collateral_auction_events
		 WHERE collateral_id = $1
		 ORDER BY occurred_at DESC
	`, collateralID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollateralAuctionEvent
	for rows.Next() {
		var e domain.CollateralAuctionEvent
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.CollateralID, &e.LoanID, &e.EventKind,
			&e.OccurredAt, &e.Amount, &e.BuyerDetails, &e.AuctioneerName,
			&e.Notes, &e.DocPath, &e.CreatedBy,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
