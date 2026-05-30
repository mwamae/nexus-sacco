// Phase-1 follow-up — append-only score history for the Score tab
// timeline. Every successful re-score writes one row that captures the
// scoring snapshot at the moment of the event.

package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
)

type LoanScoreHistoryStore struct {
	pool *pgxpool.Pool
}

func NewLoanScoreHistoryStore(pool *pgxpool.Pool) *LoanScoreHistoryStore {
	return &LoanScoreHistoryStore{pool: pool}
}

type ScoreHistoryInsert struct {
	ApplicationID           uuid.UUID
	ScoredBy                *uuid.UUID
	CreditScore             *int
	RiskBand                *string
	AffordabilityPass       *bool
	DTIRatio                *decimal.Decimal
	NetDisposableIncome     *decimal.Decimal
	ComputedMaxAmount       *decimal.Decimal
	ComputedMaxInstallment  *decimal.Decimal
	RecommendedAmount       *decimal.Decimal
	RecommendedTermMonths   *int
	ScoringDetailsJSON      []byte // pre-marshalled to avoid double-encoding
	ScoringFlagsJSON        []byte
	TriggerReason           string
}

func (s *LoanScoreHistoryStore) InsertTx(ctx context.Context, tx pgx.Tx, in ScoreHistoryInsert) error {
	if in.TriggerReason == "" {
		in.TriggerReason = "initial_score"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO loan_application_score_history (
		  tenant_id, application_id, scored_by,
		  credit_score, risk_band, affordability_pass,
		  dti_ratio, net_disposable_income,
		  computed_max_amount, computed_max_installment,
		  recommended_amount, recommended_term_months,
		  scoring_details, scoring_flags,
		  trigger_reason
		) VALUES (
		  current_tenant_id(), $1, $2,
		  $3, $4, $5,
		  $6, $7,
		  $8, $9,
		  $10, $11,
		  NULLIF($12, '')::jsonb, NULLIF($13, '')::jsonb,
		  $14
		)
	`, in.ApplicationID, in.ScoredBy,
		in.CreditScore, in.RiskBand, in.AffordabilityPass,
		in.DTIRatio, in.NetDisposableIncome,
		in.ComputedMaxAmount, in.ComputedMaxInstallment,
		in.RecommendedAmount, in.RecommendedTermMonths,
		string(in.ScoringDetailsJSON), string(in.ScoringFlagsJSON),
		in.TriggerReason,
	)
	return err
}

func (s *LoanScoreHistoryStore) ListByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.LoanScoreHistoryEntry, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, application_id, scored_at, scored_by,
		       credit_score, risk_band, affordability_pass,
		       dti_ratio, net_disposable_income,
		       computed_max_amount, computed_max_installment,
		       recommended_amount, recommended_term_months,
		       COALESCE(scoring_details::text, '')::bytea,
		       COALESCE(scoring_flags::text, '')::bytea,
		       trigger_reason
		  FROM loan_application_score_history
		 WHERE application_id = $1
		 ORDER BY scored_at DESC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanScoreHistoryEntry
	for rows.Next() {
		var e domain.LoanScoreHistoryEntry
		var detailsRaw, flagsRaw []byte
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.ApplicationID, &e.ScoredAt, &e.ScoredBy,
			&e.CreditScore, &e.RiskBand, &e.AffordabilityPass,
			&e.DTIRatio, &e.NetDisposableIncome,
			&e.ComputedMaxAmount, &e.ComputedMaxInstallment,
			&e.RecommendedAmount, &e.RecommendedTermMonths,
			&detailsRaw, &flagsRaw,
			&e.TriggerReason,
		); err != nil {
			return nil, err
		}
		if len(detailsRaw) > 0 {
			e.ScoringDetails = detailsRaw
		}
		if len(flagsRaw) > 0 {
			e.ScoringFlags = flagsRaw
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// _ — pinned to keep time import even if removed in future trims.
var _ = time.Time{}
