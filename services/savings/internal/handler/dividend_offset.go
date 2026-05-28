// Loans Phase 4 — dividend offset policy endpoints.
//
// GET  /v1/dividends/runs/{run_id}/arrears-offset-preview
//      One row per member with net_dividend > 0 AND loans in arrears.
//      Columns: member, dividend_payable, total_arrears,
//               suggested_offset (min of the two), residual_payout.
//
// POST /v1/dividends/runs/{run_id}/arrears-offset-postings
//      Posts one or more offsets. Body: {"member_ids": [...]} to post
//      only specific members, or {"all": true} to post for every
//      member in the preview. Each posting inserts a
//      dividend_offset_postings row + (in a future PR) an offsetting
//      JE. Idempotent on source_ref (UNIQUE on the table).
//
// The actual JE posting is layered on the existing posting.Client
// using the loan's repayment waterfall (penalty → interest → fees →
// principal). For Phase 4 we post per-line:
//   DR Member Dividend Payable  amount
//   CR Loan Loss / Interest / Penalty / Principal  per allocation
//
// Permissions:
//   loans:view        — read preview
//   dividends:approve — post offset (same gate as posting the JE)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type DividendOffsetHandler struct {
	DB        *db.Pool
	Dividends *store.DividendStore
	Loans     *store.LoanStore
	Posting   *posting.Client
	Logger    *slog.Logger
}

// ─────────── Preview ───────────

type offsetPreviewRow struct {
	MemberID         uuid.UUID       `json:"member_id"`
	MemberName       string          `json:"member_name"`
	DividendPayable  decimal.Decimal `json:"dividend_payable"`
	TotalArrears     decimal.Decimal `json:"total_arrears"`
	SuggestedOffset  decimal.Decimal `json:"suggested_offset"`
	ResidualPayout   decimal.Decimal `json:"residual_payout"`
	Loans            []offsetLoanRow `json:"loans"`
}

type offsetLoanRow struct {
	LoanID            uuid.UUID       `json:"loan_id"`
	LoanNo            string          `json:"loan_no"`
	PrincipalOverdue  decimal.Decimal `json:"principal_overdue"`
	InterestBalance   decimal.Decimal `json:"interest_balance"`
	PenaltyBalance    decimal.Decimal `json:"penalty_balance"`
	TotalArrears      decimal.Decimal `json:"total_arrears"`
}

func (h *DividendOffsetHandler) Preview(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var rows []offsetPreviewRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		rows, err = buildOffsetPreview(r.Context(), tx, runID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.OK(w, map[string]any{"items": rows, "total": len(rows)})
}

// buildOffsetPreview is exported-as-helper so tests can drive it
// without an HTTP request.
func buildOffsetPreview(ctx context.Context, tx pgx.Tx, runID uuid.UUID) ([]offsetPreviewRow, error) {
	// Per-member dividend payable across all run lines.
	memberRows, err := tx.Query(ctx, `
		SELECT drl.counterparty_id, cd.full_name,
		       SUM(drl.net_dividend) AS payable
		  FROM dividend_run_lines drl
		  JOIN counterparty_directory cd ON cd.counterparty_id = drl.counterparty_id
		 WHERE drl.run_id = $1 AND drl.net_dividend > 0
		 GROUP BY drl.counterparty_id, cd.full_name
		 HAVING SUM(drl.net_dividend) > 0
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}

	type memberAgg struct {
		ID       uuid.UUID
		Name     string
		Payable  decimal.Decimal
	}
	var members []memberAgg
	for memberRows.Next() {
		var m memberAgg
		if err := memberRows.Scan(&m.ID, &m.Name, &m.Payable); err != nil {
			memberRows.Close()
			return nil, err
		}
		members = append(members, m)
	}
	memberRows.Close()
	if err := memberRows.Err(); err != nil {
		return nil, err
	}

	out := make([]offsetPreviewRow, 0, len(members))
	for _, m := range members {
		// Per-member loans in arrears. Principal-overdue is calculated
		// from the repayment schedule: sum of unpaid principal_due on
		// installments whose due_date <= today AND status NOT IN
		// (paid, cancelled).
		loanRows, err := tx.Query(ctx, `
			WITH overdue AS (
			  SELECT lrs.loan_id,
			         COALESCE(SUM(lrs.principal_due - lrs.principal_paid),0)::numeric AS principal_overdue
			    FROM loan_repayment_schedule lrs
			    JOIN loans l ON l.id = lrs.loan_id
			   WHERE l.counterparty_id = $1
			     AND lrs.due_date <= CURRENT_DATE
			     AND lrs.status NOT IN ('paid','cancelled')
			   GROUP BY lrs.loan_id
			)
			SELECT l.id, l.loan_no,
			       COALESCE(o.principal_overdue, 0)::numeric,
			       l.interest_balance, l.penalty_balance
			  FROM loans l
			  LEFT JOIN overdue o ON o.loan_id = l.id
			 WHERE l.counterparty_id = $1
			   AND l.status IN ('active','in_arrears','restructured')
			   AND (COALESCE(o.principal_overdue,0) > 0 OR l.interest_balance > 0 OR l.penalty_balance > 0)
		`, m.ID)
		if err != nil {
			return nil, fmt.Errorf("query loans for %s: %w", m.ID, err)
		}
		var loans []offsetLoanRow
		var total decimal.Decimal
		for loanRows.Next() {
			var l offsetLoanRow
			if err := loanRows.Scan(&l.LoanID, &l.LoanNo, &l.PrincipalOverdue, &l.InterestBalance, &l.PenaltyBalance); err != nil {
				loanRows.Close()
				return nil, err
			}
			l.TotalArrears = l.PrincipalOverdue.Add(l.InterestBalance).Add(l.PenaltyBalance)
			loans = append(loans, l)
			total = total.Add(l.TotalArrears)
		}
		loanRows.Close()
		if err := loanRows.Err(); err != nil {
			return nil, err
		}
		if total.IsZero() {
			continue
		}
		offset := decimalMin(m.Payable, total)
		out = append(out, offsetPreviewRow{
			MemberID:        m.ID,
			MemberName:      m.Name,
			DividendPayable: m.Payable,
			TotalArrears:    total,
			SuggestedOffset: offset,
			ResidualPayout:  m.Payable.Sub(offset),
			Loans:           loans,
		})
	}
	return out, nil
}

func decimalMin(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

// ─────────── Post offsets ───────────

type postOffsetReq struct {
	All       bool        `json:"all"`
	MemberIDs []uuid.UUID `json:"member_ids"`
}

type postOffsetResultRow struct {
	MemberID  uuid.UUID       `json:"member_id"`
	OffsetID  uuid.UUID       `json:"offset_posting_id"`
	Amount    decimal.Decimal `json:"amount"`
	SourceRef string          `json:"source_ref"`
}

func (h *DividendOffsetHandler) PostOffsets(w http.ResponseWriter, r *http.Request) {
	runID, err := uuid.Parse(chi.URLParam(r, "run_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid run_id"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	if uid == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	var in postOffsetReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err); return
	}
	if !in.All && len(in.MemberIDs) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("either all=true or member_ids[] required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var results []postOffsetResultRow
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		preview, err := buildOffsetPreview(r.Context(), tx, runID)
		if err != nil {
			return err
		}
		// Filter to the requested members.
		wanted := map[uuid.UUID]bool{}
		if !in.All {
			for _, id := range in.MemberIDs {
				wanted[id] = true
			}
		}
		for _, row := range preview {
			if !in.All && !wanted[row.MemberID] {
				continue
			}
			if row.SuggestedOffset.IsZero() {
				continue
			}
			// Per-member offset: spread across the member's arrears
			// loans in the canonical waterfall (penalty → interest →
			// principal-overdue). Each loan gets its own offset row.
			remaining := row.SuggestedOffset
			for _, l := range row.Loans {
				if remaining.LessThanOrEqual(decimal.Zero) {
					break
				}
				take := decimalMin(remaining, l.TotalArrears)
				if take.IsZero() {
					continue
				}
				// Per-loan allocation in waterfall order.
				alloc := allocateInWaterfall(take, l)
				allocJSON, _ := json.Marshal(alloc)
				sourceRef := fmt.Sprintf("manual:%s/%s/%s", runID, row.MemberID, l.LoanID)
				var id uuid.UUID
				err := tx.QueryRow(r.Context(), `
					INSERT INTO dividend_offset_postings (
					  tenant_id, dividend_run_id, member_id, loan_id,
					  amount, allocation, posted_by, source_ref
					) VALUES (current_tenant_id(), $1, $2, $3, $4, $5, $6, $7)
					ON CONFLICT (source_ref) DO NOTHING
					RETURNING id
				`, runID, row.MemberID, l.LoanID, take, allocJSON, uid, sourceRef).Scan(&id)
				if err != nil {
					if errors.Is(err, pgx.ErrNoRows) {
						// Already posted (idempotent); skip.
						remaining = remaining.Sub(take)
						continue
					}
					return err
				}
				results = append(results, postOffsetResultRow{
					MemberID:  row.MemberID,
					OffsetID:  id,
					Amount:    take,
					SourceRef: sourceRef,
				})
				remaining = remaining.Sub(take)
			}
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err); return
	}
	httpx.Created(w, map[string]any{"posted": results, "count": len(results)})
}

type offsetAllocation struct {
	Penalty          decimal.Decimal `json:"penalty"`
	Interest         decimal.Decimal `json:"interest"`
	PrincipalOverdue decimal.Decimal `json:"principal_overdue"`
}

// allocateInWaterfall splits the offset amount across the loan's
// arrears in the canonical waterfall: penalty first, then interest,
// then principal-overdue.
func allocateInWaterfall(amount decimal.Decimal, l offsetLoanRow) offsetAllocation {
	var a offsetAllocation
	rem := amount
	if rem.GreaterThan(decimal.Zero) && l.PenaltyBalance.GreaterThan(decimal.Zero) {
		a.Penalty = decimalMin(rem, l.PenaltyBalance)
		rem = rem.Sub(a.Penalty)
	}
	if rem.GreaterThan(decimal.Zero) && l.InterestBalance.GreaterThan(decimal.Zero) {
		a.Interest = decimalMin(rem, l.InterestBalance)
		rem = rem.Sub(a.Interest)
	}
	if rem.GreaterThan(decimal.Zero) && l.PrincipalOverdue.GreaterThan(decimal.Zero) {
		a.PrincipalOverdue = decimalMin(rem, l.PrincipalOverdue)
		rem = rem.Sub(a.PrincipalOverdue)
	}
	return a
}
