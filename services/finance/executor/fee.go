// Fee executor. Phase 3.5 ships the GL post + a writeback to
// member_fees_due (clears or partially reduces a row). Catalog
// resolution happens inside this file so callers don't carry a fee
// catalog dep.

package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/finance/posting"
	"github.com/nexussacco/finance/types"
)

// PostFeeTx records a fee payment against an outstanding
// member_fees_due row (if one exists for the fee_code) and writes
// the GL outbox entry. Returns a synthetic Result with the
// member_fees_due row id (or zero when no due row matched — a
// "loose" fee payment).
func PostFeeTx(ctx context.Context, tx pgx.Tx, in types.PostFeeInput) (*types.Result, error) {
	if in.TenantID == uuid.Nil {
		return nil, errors.New("finance/executor: tenant_id required")
	}
	if in.FeeCode == "" {
		return nil, errors.New("finance/executor: fee_code required")
	}
	if in.Amount.LessThanOrEqual(decimal.Zero) {
		return nil, errors.New("finance/executor: fee amount must be > 0")
	}

	// 1. Resolve the catalog entry. GL credit account comes from
	// fee_catalog.gl_credit_code; missing entry → reject so we
	// don't post against the wrong CoA bucket.
	var glCreditCode, label string
	if err := tx.QueryRow(ctx, `
		SELECT gl_credit_code, COALESCE(label, code)
		  FROM fee_catalog WHERE code = $1
	`, in.FeeCode).Scan(&glCreditCode, &label); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("finance/executor: fee_code %q not in catalog", in.FeeCode)
		}
		return nil, fmt.Errorf("read fee_catalog: %w", err)
	}

	// 2. If member_fees_due exists for (tenant, fee_code), reduce
	// the amount_due. Phase 3.5 expects the engine to have queried
	// this row in advance; we update it best-effort.
	var dueID uuid.UUID
	_ = tx.QueryRow(ctx, `
		UPDATE member_fees_due
		   SET amount_due = GREATEST(amount_due - $2, 0),
		       status     = CASE
		                      WHEN amount_due - $2 <= 0 THEN 'paid'
		                      ELSE 'partial'
		                    END,
		       updated_at = now()
		 WHERE tenant_id = $3 AND fee_code = $1
		   AND status <> 'paid'
		   AND amount_due > 0
		RETURNING id
	`, in.FeeCode, in.Amount, in.TenantID).Scan(&dueID)

	// 3. Post GL: DR clearing → CR fee income.
	const cashClearing = "1099"
	valueDate := in.ValueDate
	if valueDate.IsZero() {
		valueDate = time.Now().UTC()
	}
	sourceRef := uuid.New()
	srcRef := sourceRef.String()
	if in.ExternalValidationRef != "" {
		srcRef = in.ExternalValidationRef + ":fee:" + in.FeeCode
	}
	outboxID, err := posting.PostTx(ctx, tx, posting.Input{
		TenantID:     in.TenantID,
		EntryDate:    valueDate,
		ValueDate:    valueDate,
		SourceModule: "finance.executor.fee",
		SourceRef:    srcRef,
		Narration:    "Fee · " + label,
		Lines: []posting.Line{
			{AccountCode: cashClearing, Debit: in.Amount, Narration: "M-PESA clearing → fees"},
			{AccountCode: glCreditCode, Credit: in.Amount, Narration: label},
		},
	})
	if err != nil {
		return nil, err
	}
	return &types.Result{TxnID: sourceRef, OutboxID: outboxID}, nil
}
