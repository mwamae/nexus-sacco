// Collateral lifecycle store — Phase 1.5a.
//
// Backs the offered → verified → valued → pledged → released/auctioned
// chain in loan_collateral, the supersede-friendly collateral_valuations
// history table, and the append-only loan_collateral_events audit log.
//
// State-transition matrix enforcement lives in the handler — this
// store does the SQL primitives.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type CollateralStore struct {
	pool *pgxpool.Pool
}

func NewCollateralStore(pool *pgxpool.Pool) *CollateralStore {
	return &CollateralStore{pool: pool}
}

// Errors callers may check.
var (
	ErrCollateralNotFound       = errors.New("collateral not found")
	ErrCollateralWrongState     = errors.New("collateral is not in the required state for this transition")
	ErrCollateralValuationGone  = errors.New("collateral has no current valuation")
)

const collateralCols = `
	id, tenant_id, application_id, loan_id, kind, description,
	estimated_value, forced_sale_value, valuation_date, valuation_path, ownership_path,
	status, notes, created_at,
	proposed_by, proposed_at, verified_by, verified_at,
	verification_notes, verification_photos,
	pledged_by, pledged_at, released_by, released_at,
	released_reason, rejected_reason,
	pledger_counterparty_id, pledger_consent_status, pledger_consent_at, pledger_consent_doc_path,
	charge_registry::text, charge_reference, charge_registered_at, charge_registered_by,
	charge_discharge_ref, charge_discharged_at, charge_certificate_path
`

func scanCollateral(row pgx.Row) (*domain.LoanCollateralItem, error) {
	var c domain.LoanCollateralItem
	var photosJSON []byte
	err := row.Scan(
		&c.ID, &c.TenantID, &c.ApplicationID, &c.LoanID, &c.Kind, &c.Description,
		&c.EstimatedValue, &c.ForcedSaleValue, &c.ValuationDate, &c.ValuationPath, &c.OwnershipPath,
		&c.Status, &c.Notes, &c.CreatedAt,
		&c.ProposedBy, &c.ProposedAt, &c.VerifiedBy, &c.VerifiedAt,
		&c.VerificationNotes, &photosJSON,
		&c.PledgedBy, &c.PledgedAt, &c.ReleasedBy, &c.ReleasedAt,
		&c.ReleasedReason, &c.RejectedReason,
		&c.PledgerCounterpartyID, &c.PledgerConsentStatus, &c.PledgerConsentAt, &c.PledgerConsentDocPath,
		&c.ChargeRegistry, &c.ChargeReference, &c.ChargeRegisteredAt, &c.ChargeRegisteredBy,
		&c.ChargeDischargeRef, &c.ChargeDischargedAt, &c.ChargeCertificatePath,
	)
	if err != nil {
		return nil, err
	}
	if len(photosJSON) > 0 {
		_ = json.Unmarshal(photosJSON, &c.VerificationPhotos)
	}
	return &c, nil
}

// ─────────── Create ───────────

type CreateCollateralInput struct {
	ApplicationID  uuid.UUID
	Kind           domain.LoanCollateralKind
	Description    string
	EstimatedValue decimal.Decimal
	OwnershipPath  *string
	Notes          *string
	ProposedBy     uuid.UUID
}

// CreateTx inserts a new collateral row with status = 'offered' (default
// from migration 0048). The proposed_by/at timestamps are stamped here.
func (s *CollateralStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateCollateralInput) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_collateral (
		  tenant_id, application_id, kind, description, estimated_value,
		  ownership_path, notes, status,
		  proposed_by, proposed_at
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4,
		  $5, $6, 'offered',
		  $7, now()
		)
		RETURNING `+collateralCols,
		in.ApplicationID, string(in.Kind), in.Description, in.EstimatedValue,
		in.OwnershipPath, in.Notes,
		in.ProposedBy,
	)
	return scanCollateral(row)
}

// ─────────── Read ───────────

func (s *CollateralStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `SELECT `+collateralCols+` FROM loan_collateral WHERE id = $1`, id)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralNotFound
	}
	return c, err
}

// ListByApplicationTx returns every collateral row for an application,
// each hydrated with its current valuation (the row in
// collateral_valuations with is_current = true).
func (s *CollateralStore) ListByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.LoanCollateralItem, error) {
	return s.listWhereTx(ctx, tx, `application_id = $1`, appID)
}

// ListByLoanTx — post-disbursement view; loan_id is backfilled when the
// application becomes a loan.
func (s *CollateralStore) ListByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID) ([]domain.LoanCollateralItem, error) {
	return s.listWhereTx(ctx, tx, `loan_id = $1`, loanID)
}

func (s *CollateralStore) listWhereTx(ctx context.Context, tx pgx.Tx, where string, arg uuid.UUID) ([]domain.LoanCollateralItem, error) {
	rows, err := tx.Query(ctx, `SELECT `+collateralCols+` FROM loan_collateral WHERE `+where+` ORDER BY proposed_at`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanCollateralItem
	for rows.Next() {
		c, err := scanCollateral(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate current valuations in one query.
	if len(out) > 0 {
		ids := make([]uuid.UUID, len(out))
		for i := range out {
			ids[i] = out[i].ID
		}
		vals, err := s.currentValuationsByCollateralTx(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for i := range out {
			if v, ok := vals[out[i].ID]; ok {
				vv := v
				out[i].CurrentValuation = &vv
			}
		}
	}
	return out, nil
}

// ─────────── Lifecycle transitions ───────────

// Each transition asserts the prior status and returns ErrCollateralWrongState
// when the row is in the wrong state. The handler maps that to 409.

func (s *CollateralStore) PatchOfferedTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, desc *string, est *decimal.Decimal, ownership *string, notes *string) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  description     = COALESCE($2, description),
		  estimated_value = COALESCE($3, estimated_value),
		  ownership_path  = COALESCE($4, ownership_path),
		  notes           = COALESCE($5, notes)
		WHERE id = $1 AND status = 'offered'
		RETURNING `+collateralCols,
		id, desc, est, ownership, notes,
	)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralWrongState
	}
	return c, err
}

func (s *CollateralStore) VerifyTx(ctx context.Context, tx pgx.Tx, id, actor uuid.UUID, notes string, photos []string) (*domain.LoanCollateralItem, error) {
	photosJSON, err := json.Marshal(photos)
	if err != nil {
		return nil, err
	}
	// Promote directly to 'valued' when a current valuation already
	// exists — covers the case where the officer attached the valuation
	// before verifying. Without this the row would land at 'verified'
	// despite having all the data needed for 'valued', and Pledge would
	// stay disabled.
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  status = CASE
		    WHEN EXISTS (
		      SELECT 1 FROM collateral_valuations
		       WHERE collateral_id = loan_collateral.id AND is_current = true
		    ) THEN 'valued'
		    ELSE 'verified'
		  END,
		  verified_by         = $2,
		  verified_at         = now(),
		  verification_notes  = $3,
		  verification_photos = $4::jsonb
		WHERE id = $1 AND status = 'offered'
		RETURNING `+collateralCols,
		id, actor, notes, photosJSON,
	)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralWrongState
	}
	return c, err
}

// RejectTx is a soft reject — status stays 'offered' so the officer can
// fix the row and resubmit for verification. rejected_reason carries the
// reviewer's note. Only valid from 'offered' or 'verified' (a verified
// item can be unwound if discovered fraudulent post-verification, but
// pledged items must go through release).
func (s *CollateralStore) RejectTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, reason string) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  status          = 'offered',
		  rejected_reason = $2,
		  verified_by     = NULL,
		  verified_at     = NULL
		WHERE id = $1 AND status IN ('offered','verified')
		RETURNING `+collateralCols,
		id, reason,
	)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralWrongState
	}
	return c, err
}

// PledgeTx flips a 'valued' item to 'pledged'. Approval-time action
// (officers with loans:approve permission).
func (s *CollateralStore) PledgeTx(ctx context.Context, tx pgx.Tx, id, actor uuid.UUID) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  status     = 'pledged',
		  pledged_by = $2,
		  pledged_at = now()
		WHERE id = $1 AND status = 'valued'
		RETURNING `+collateralCols,
		id, actor,
	)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralWrongState
	}
	return c, err
}

// MarkAuctionedTx flips 'pledged' → 'auctioned'. Terminal state —
// individual auction events (handover, notice, sale, proceeds) are
// tracked on collateral_auction_events. We re-use the released_*
// columns to stamp the actor + timestamp so the audit shape stays
// consistent with the release path.
func (s *CollateralStore) MarkAuctionedTx(ctx context.Context, tx pgx.Tx, id, actor uuid.UUID, reason string) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  status          = 'auctioned',
		  released_by     = $2,
		  released_at     = now(),
		  released_reason = $3
		WHERE id = $1 AND status = 'pledged'
		RETURNING `+collateralCols,
		id, actor, reason,
	)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralWrongState
	}
	return c, err
}

// ReleaseTx flips 'pledged' → 'released' with reason. Terminal state.
func (s *CollateralStore) ReleaseTx(ctx context.Context, tx pgx.Tx, id, actor uuid.UUID, reason string) (*domain.LoanCollateralItem, error) {
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  status          = 'released',
		  released_by     = $2,
		  released_at     = now(),
		  released_reason = $3
		WHERE id = $1 AND status = 'pledged'
		RETURNING `+collateralCols,
		id, actor, reason,
	)
	c, err := scanCollateral(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCollateralWrongState
	}
	return c, err
}

// DeleteTx removes a row only while it's in offered/verified. Pledged or
// released rows are permanent for audit.
func (s *CollateralStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		DELETE FROM loan_collateral WHERE id = $1 AND status IN ('offered','verified')
	`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCollateralWrongState
	}
	return nil
}

// ─────────── Valuation ───────────

type CreateValuationInput struct {
	CollateralID        uuid.UUID
	ValuerName          string
	ValuerContact       *string
	ValuationDate       time.Time
	MarketValue         decimal.Decimal
	ForcedSaleValue     decimal.Decimal
	ValuationReportPath *string
	ExpiresAt           *time.Time
	Notes               *string
	CreatedBy           uuid.UUID
}

// CreateValuationTx writes a new collateral_valuations row, flips any
// prior current valuation to is_current = false + links the chain via
// superseded_by_id, and (if the collateral is in 'verified') walks it
// to 'valued'. Pledged items can be revalued without changing status.
//
// Returns the new valuation + the (possibly status-mutated) collateral row.
func (s *CollateralStore) CreateValuationTx(ctx context.Context, tx pgx.Tx, in CreateValuationInput) (*domain.CollateralValuation, *domain.LoanCollateralItem, error) {
	// Insert the new valuation row.
	var v domain.CollateralValuation
	err := tx.QueryRow(ctx, `
		INSERT INTO collateral_valuations (
		  tenant_id, collateral_id, valuer_name, valuer_contact,
		  valuation_date, market_value, forced_sale_value,
		  valuation_report_path, expires_at, notes, created_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		RETURNING id, tenant_id, collateral_id, valuer_name, valuer_contact,
		          valuation_date, market_value, forced_sale_value,
		          valuation_report_path, expires_at, is_current, superseded_by_id,
		          notes, created_at, created_by
	`, in.CollateralID, in.ValuerName, in.ValuerContact,
		in.ValuationDate, in.MarketValue, in.ForcedSaleValue,
		in.ValuationReportPath, in.ExpiresAt, in.Notes, in.CreatedBy,
	).Scan(
		&v.ID, &v.TenantID, &v.CollateralID, &v.ValuerName, &v.ValuerContact,
		&v.ValuationDate, &v.MarketValue, &v.ForcedSaleValue,
		&v.ValuationReportPath, &v.ExpiresAt, &v.IsCurrent, &v.SupersededByID,
		&v.Notes, &v.CreatedAt, &v.CreatedBy,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("insert valuation: %w", err)
	}

	// Flip any prior current valuation. The partial unique index would
	// have blocked this if we didn't — do it before letting the index
	// see two rows with is_current = true. (Pgx runs statements in a
	// single command queue, so the new row is already committed within
	// the tx; an UPDATE with a WHERE clause excluding the new row is
	// fine here.)
	if _, err := tx.Exec(ctx, `
		UPDATE collateral_valuations
		   SET is_current      = false,
		       superseded_by_id = $2
		 WHERE collateral_id = $1 AND id <> $2 AND is_current = true
	`, in.CollateralID, v.ID); err != nil {
		return nil, nil, fmt.Errorf("supersede prior valuation: %w", err)
	}

	// Mirror the FSV onto loan_collateral.forced_sale_value so the
	// coverage evaluator (which sums collateral.forced_sale_value) sees
	// the latest figure without an extra join.
	//
	// Status transition: offered | verified → valued. valued/pledged/etc.
	// stay. (Officers sometimes attach a valuation before verifying —
	// promoting from either earlier state keeps the row consistent.)
	row := tx.QueryRow(ctx, `
		UPDATE loan_collateral SET
		  forced_sale_value = $2,
		  valuation_date    = $3,
		  status = CASE WHEN status IN ('offered','verified') THEN 'valued' ELSE status END
		WHERE id = $1
		RETURNING `+collateralCols,
		in.CollateralID, in.ForcedSaleValue, in.ValuationDate,
	)
	c, err := scanCollateral(row)
	if err != nil {
		return nil, nil, fmt.Errorf("update collateral after valuation: %w", err)
	}
	c.CurrentValuation = &v
	return &v, c, nil
}

// ValuationHistoryTx returns every valuation for an item, newest first.
func (s *CollateralStore) ValuationHistoryTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) ([]domain.CollateralValuation, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, valuer_name, valuer_contact,
		       valuation_date, market_value, forced_sale_value,
		       valuation_report_path, expires_at, is_current, superseded_by_id,
		       notes, created_at, created_by
		  FROM collateral_valuations
		 WHERE collateral_id = $1
		 ORDER BY valuation_date DESC, created_at DESC
	`, collateralID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollateralValuation
	for rows.Next() {
		var v domain.CollateralValuation
		if err := rows.Scan(
			&v.ID, &v.TenantID, &v.CollateralID, &v.ValuerName, &v.ValuerContact,
			&v.ValuationDate, &v.MarketValue, &v.ForcedSaleValue,
			&v.ValuationReportPath, &v.ExpiresAt, &v.IsCurrent, &v.SupersededByID,
			&v.Notes, &v.CreatedAt, &v.CreatedBy,
		); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *CollateralStore) currentValuationsByCollateralTx(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) (map[uuid.UUID]domain.CollateralValuation, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, valuer_name, valuer_contact,
		       valuation_date, market_value, forced_sale_value,
		       valuation_report_path, expires_at, is_current, superseded_by_id,
		       notes, created_at, created_by
		  FROM collateral_valuations
		 WHERE collateral_id = ANY($1) AND is_current = true
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[uuid.UUID]domain.CollateralValuation, len(ids))
	for rows.Next() {
		var v domain.CollateralValuation
		if err := rows.Scan(
			&v.ID, &v.TenantID, &v.CollateralID, &v.ValuerName, &v.ValuerContact,
			&v.ValuationDate, &v.MarketValue, &v.ForcedSaleValue,
			&v.ValuationReportPath, &v.ExpiresAt, &v.IsCurrent, &v.SupersededByID,
			&v.Notes, &v.CreatedAt, &v.CreatedBy,
		); err != nil {
			return nil, err
		}
		out[v.CollateralID] = v
	}
	return out, rows.Err()
}

// ─────────── Events (audit timeline) ───────────

type AppendEventInput struct {
	CollateralID uuid.UUID
	Kind         string
	ActorUserID  *uuid.UUID
	Details      map[string]interface{}
}

func (s *CollateralStore) AppendEventTx(ctx context.Context, tx pgx.Tx, in AppendEventInput) error {
	var detailsJSON []byte
	if in.Details != nil {
		var err error
		detailsJSON, err = json.Marshal(in.Details)
		if err != nil {
			return fmt.Errorf("marshal event details: %w", err)
		}
	} else {
		detailsJSON = []byte("{}")
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO loan_collateral_events (
		  tenant_id, collateral_id, actor_user_id, kind, details
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4::jsonb
		)
	`, in.CollateralID, in.ActorUserID, in.Kind, detailsJSON)
	return err
}

func (s *CollateralStore) EventsByCollateralTx(ctx context.Context, tx pgx.Tx, collateralID uuid.UUID) ([]domain.CollateralEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, collateral_id, occurred_at, actor_user_id, kind, details
		  FROM loan_collateral_events
		 WHERE collateral_id = $1
		 ORDER BY occurred_at DESC
		 LIMIT 200
	`, collateralID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CollateralEvent
	for rows.Next() {
		var e domain.CollateralEvent
		var detailsJSON []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.CollateralID, &e.OccurredAt, &e.ActorUserID, &e.Kind, &detailsJSON); err != nil {
			return nil, err
		}
		if len(detailsJSON) > 0 {
			_ = json.Unmarshal(detailsJSON, &e.Details)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─────────── Coverage helpers ───────────

// SumPledgedFSVByApplicationTx returns the sum of forced_sale_value over
// every collateral row whose status = 'pledged' for the given app.
// Returns zero when no pledged items exist.
func (s *CollateralStore) SumPledgedFSVByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(forced_sale_value), 0)
		  FROM loan_collateral
		 WHERE application_id = $1
		   AND status = 'pledged'
		   AND forced_sale_value IS NOT NULL
	`, appID).Scan(&sum)
	return sum, err
}

// SumAcceptedGuaranteesByApplicationTx returns the sum of
// amount_guaranteed across every loan_guarantees row in 'accepted'
// status for the application.
func (s *CollateralStore) SumAcceptedGuaranteesByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_guaranteed), 0)
		  FROM loan_guarantees
		 WHERE application_id = $1
		   AND status = 'accepted'
	`, appID).Scan(&sum)
	return sum, err
}

// ─────────── Coverage override audit ───────────

type RecordOverrideInput struct {
	ApplicationID         uuid.UUID
	OverriddenBy          uuid.UUID
	Reason                string
	SecurityModel         string
	LoanAmount            decimal.Decimal
	GuarantorPledged      decimal.Decimal
	CollateralFSV         decimal.Decimal
	MinGuarantorCoverPct  decimal.Decimal
	MinCollateralCoverPct decimal.Decimal
	GuarantorCoverPct     decimal.Decimal
	CollateralCoverPct    decimal.Decimal
	EvaluatorReason       string
}

func (s *CollateralStore) RecordCoverageOverrideTx(ctx context.Context, tx pgx.Tx, in RecordOverrideInput) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO loan_coverage_overrides (
		  tenant_id, application_id, overridden_by, reason,
		  security_model, loan_amount,
		  guarantor_pledged, collateral_fsv,
		  min_guarantor_cover_pct, min_collateral_cover_pct,
		  guarantor_cover_pct, collateral_cover_pct,
		  evaluator_reason
		) VALUES (
		  current_tenant_id(), $1, $2, $3,
		  $4, $5,
		  $6, $7,
		  $8, $9,
		  $10, $11,
		  $12
		)
	`, in.ApplicationID, in.OverriddenBy, in.Reason,
		in.SecurityModel, in.LoanAmount,
		in.GuarantorPledged, in.CollateralFSV,
		in.MinGuarantorCoverPct, in.MinCollateralCoverPct,
		in.GuarantorCoverPct, in.CollateralCoverPct,
		in.EvaluatorReason,
	)
	return err
}
