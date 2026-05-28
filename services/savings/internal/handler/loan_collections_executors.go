// Maker-checker executors for restructuring + write-off (stage 3).
// Each replays the existing handler tx-closure body using a typed
// payload + maker id.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/postingops"
	"github.com/nexussacco/savings/internal/store"
)

// ─────────── Payloads ───────────

type LoanReschedulePayload struct {
	LoanID             uuid.UUID        `json:"loan_id"`
	NewTermMonths      int              `json:"new_term_months"`
	NewInterestRatePct *decimal.Decimal `json:"new_interest_rate_pct,omitempty"`
	NewFirstDueDate    *string          `json:"new_first_due_date,omitempty"`
	Reason             string           `json:"reason"`
}

type LoanMoratoriumPayload struct {
	LoanID           uuid.UUID `json:"loan_id"`
	MoratoriumMonths int       `json:"moratorium_months"`
	SuspendInterest  bool      `json:"suspend_interest"`
	Reason           string    `json:"reason"`
}

type LoanSettlementDiscountPayload struct {
	LoanID         uuid.UUID       `json:"loan_id"`
	DiscountAmount decimal.Decimal `json:"discount_amount"`
	Reason         string          `json:"reason"`
}

type LoanWriteoffPayload struct {
	LoanID uuid.UUID `json:"loan_id"`
	Reason string    `json:"reason"`
}

// ─────────── Result shapes ───────────

type LoanRestructureResult struct {
	Restructuring domain.LoanRestructuring `json:"restructuring"`
	Loan          domain.Loan              `json:"loan"`
}

type LoanWriteoffResult struct {
	Writeoff store.LoanWriteoff `json:"writeoff"`
	Loan     domain.Loan        `json:"loan"`
}

// ─────────── Executors ───────────

func (h *LoanCollectionsHandler) ExecuteRescheduleTx(
	ctx context.Context, tx pgx.Tx,
	p LoanReschedulePayload, makerID uuid.UUID,
) (*LoanRestructureResult, error) {
	var firstDue *time.Time
	if p.NewFirstDueDate != nil && *p.NewFirstDueDate != "" {
		d, err := time.Parse("2006-01-02", *p.NewFirstDueDate)
		if err != nil {
			return nil, httpx.ErrBadRequest("new_first_due_date must be YYYY-MM-DD")
		}
		firstDue = &d
	}
	rec, loan, err := h.Restructure.RescheduleTx(ctx, tx, h.Loans, store.RescheduleInput{
		LoanID:             p.LoanID,
		NewTermMonths:      p.NewTermMonths,
		NewInterestRatePct: p.NewInterestRatePct,
		NewFirstDueDate:    firstDue,
		Reason:             p.Reason,
		By:                 makerID,
	})
	if err != nil {
		return nil, err
	}
	return &LoanRestructureResult{Restructuring: *rec, Loan: *loan}, nil
}

func (h *LoanCollectionsHandler) ExecuteMoratoriumTx(
	ctx context.Context, tx pgx.Tx,
	p LoanMoratoriumPayload, makerID uuid.UUID,
) (*LoanRestructureResult, error) {
	rec, loan, err := h.Restructure.MoratoriumTx(ctx, tx, h.Loans, store.MoratoriumInput{
		LoanID:           p.LoanID,
		MoratoriumMonths: p.MoratoriumMonths,
		SuspendInterest:  p.SuspendInterest,
		Reason:           p.Reason,
		By:               makerID,
	})
	if err != nil {
		return nil, err
	}
	return &LoanRestructureResult{Restructuring: *rec, Loan: *loan}, nil
}

func (h *LoanCollectionsHandler) ExecuteSettlementDiscountTx(
	ctx context.Context, tx pgx.Tx,
	p LoanSettlementDiscountPayload, makerID uuid.UUID,
) (*LoanRestructureResult, error) {
	rec, loan, err := h.Restructure.SettlementDiscountTx(ctx, tx, h.Loans, store.SettlementDiscountInput{
		LoanID:         p.LoanID,
		DiscountAmount: p.DiscountAmount,
		Reason:         p.Reason,
		By:             makerID,
	})
	if err != nil {
		return nil, err
	}
	return &LoanRestructureResult{Restructuring: *rec, Loan: *loan}, nil
}

// Write-off executor — used by both the gated handler and the dispatcher.
func (h *LoanReportsHandler) ExecuteWriteoffTx(
	ctx context.Context, tx pgx.Tx,
	p LoanWriteoffPayload, makerID uuid.UUID,
) (*LoanWriteoffResult, error) {
	wo, loan, err := h.Reports.WriteOffLoanTx(ctx, tx, h.Loans, store.WriteOffInput{
		LoanID: p.LoanID,
		Reason: p.Reason,
		By:     makerID,
	})
	if err != nil {
		return nil, err
	}
	return &LoanWriteoffResult{Writeoff: *wo, Loan: *loan}, nil
}

// ── wf_callbacks-facing Run wrappers ──────────────────────────────

// RunRescheduleTx — wf_callbacks wrapper for loan_reschedule.
// Reschedule has no GL leg; returns uuid.Nil.
func (h *LoanCollectionsHandler) RunRescheduleTx(
	ctx context.Context, tx pgx.Tx, _ uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanReschedulePayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_reschedule context: %w", err)
	}
	if _, err := h.ExecuteRescheduleTx(ctx, tx, env.Payload, makerID); err != nil {
		return uuid.Nil, err
	}
	return uuid.Nil, nil
}

// RunMoratoriumTx — wf_callbacks wrapper for loan_moratorium.
// Moratorium has no GL leg; returns uuid.Nil.
func (h *LoanCollectionsHandler) RunMoratoriumTx(
	ctx context.Context, tx pgx.Tx, _ uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanMoratoriumPayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_moratorium context: %w", err)
	}
	if _, err := h.ExecuteMoratoriumTx(ctx, tx, env.Payload, makerID); err != nil {
		return uuid.Nil, err
	}
	return uuid.Nil, nil
}

// RunSettlementDiscountTx — wf_callbacks wrapper for
// loan_settlement_discount. Posts the discount JE via postingops if
// the executor returned a discount txn id; mirrors the legacy
// executePayloadTx case behaviour.
func (h *LoanCollectionsHandler) RunSettlementDiscountTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanSettlementDiscountPayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_settlement_discount context: %w", err)
	}
	res, err := h.ExecuteSettlementDiscountTx(ctx, tx, env.Payload, makerID)
	if err != nil {
		return uuid.Nil, err
	}
	if res.Restructuring.DiscountWriteoffTxnID == nil {
		return uuid.Nil, nil
	}
	id := *res.Restructuring.DiscountWriteoffTxnID
	discountAmt := decimal.Zero
	if res.Restructuring.DiscountAmount != nil {
		discountAmt = *res.Restructuring.DiscountAmount
	}
	if err := postingops.PostLoanSettlementDiscountTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
	}, postingops.LoanSettlementDiscountInput{
		TenantID:        tenantID,
		DiscountTxnID:   id,
		LoanNo:          res.Loan.LoanNo,
		DiscountAmount:  discountAmt,
		WaivedComponent: "interest",
		Reason:          env.Payload.Reason,
	}); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// RunWriteoffTx — wf_callbacks wrapper for loan_write_off.
// Posts the write-off JE via postingops; reads the per-tenant
// writeoff_through_provision flag to choose the correct legs.
func (h *LoanReportsHandler) RunWriteoffTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	contextJSON []byte, makerID uuid.UUID,
) (uuid.UUID, error) {
	var env struct {
		Payload LoanWriteoffPayload `json:"payload"`
	}
	if err := json.Unmarshal(contextJSON, &env); err != nil {
		return uuid.Nil, fmt.Errorf("decode loan_write_off context: %w", err)
	}
	res, err := h.ExecuteWriteoffTx(ctx, tx, env.Payload, makerID)
	if err != nil {
		return uuid.Nil, err
	}
	if res.Writeoff.WriteoffTxnID == nil {
		return uuid.Nil, nil
	}
	throughProvision, terr := readWriteoffThroughProvisionTx(ctx, tx)
	if terr != nil {
		return uuid.Nil, terr
	}
	id := *res.Writeoff.WriteoffTxnID
	if err := postingops.PostLoanWriteoffTx(ctx, tx, postingops.Deps{
		Posting: h.Posting,
	}, postingops.LoanWriteoffInput{
		TenantID:         tenantID,
		TxnID:            id,
		LoanNo:           res.Loan.LoanNo,
		Amount:           res.Writeoff.TotalWrittenOff,
		Reason:           env.Payload.Reason,
		ThroughProvision: throughProvision,
	}); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
