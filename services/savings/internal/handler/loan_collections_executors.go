// Maker-checker executors for restructuring + write-off (stage 3).
// Each replays the existing handler tx-closure body using a typed
// payload + maker id.

package handler

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
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
