// Loan reporting HTTP handlers (Phase 6f).
//
//   GET   /v1/loan-reports/portfolio                      portfolio summary + breakdowns
//   GET   /v1/loan-reports/aging                          arrears aging + provisioning + NPL ratio
//   GET   /v1/loan-reports/maturing?within_days=30        loans whose final installment is within N days
//   GET   /v1/loan-reports/restructured?kind=reschedule   restructured loan register
//   GET   /v1/loan-reports/writeoffs                      written-off register (+ recoveries to date)
//   GET   /v1/loan-reports/crb-submission                 CRB submission JSON skeleton
//
//   GET   /v1/members/{counterparty_id}/loan-history            per-member loan history
//
//   POST  /v1/loans/{loan_id}/writeoff                    write off remaining balance (audit + state flip)

package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

type LoanReportsHandler struct {
	DB        *db.Pool
	Reports   *store.LoanReportsStore
	Loans     *store.LoanStore
	Approvals *store.ApprovalsStore
	// Posting fires the write-off JE (direct OR through-provision per
	// tenant_operations.writeoff_through_provision). Optional in
	// tests; nil disables the JE write.
	Posting *posting.Client
	Logger  *slog.Logger

	Workflow       *workflowclient.Client
	SavingsSelfURL string
}

// ─────────── Portfolio summary ───────────

func (h *LoanReportsHandler) Portfolio(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.PortfolioSummary
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Reports.PortfolioSummaryTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Aging ───────────

func (h *LoanReportsHandler) Aging(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.AgingReport
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Reports.AgingReportTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Per-member loan history ───────────

func (h *LoanReportsHandler) MemberHistory(w http.ResponseWriter, r *http.Request) {
	memberID, err := parseUUIDParam(r, "counterparty_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.MemberLoanHistory
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		out, err = h.Reports.MemberLoanHistoryTx(r.Context(), tx, memberID)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Maturing loans ───────────

func (h *LoanReportsHandler) Maturing(w http.ResponseWriter, r *http.Request) {
	within := 30
	if v := r.URL.Query().Get("within_days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			within = n
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.MaturingLoanRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Reports.MaturingLoansTx(r.Context(), tx, within)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.MaturingLoanRow{}
	}
	httpx.OK(w, map[string]any{"within_days": within, "items": items})
}

// ─────────── Restructured register ───────────

func (h *LoanReportsHandler) Restructured(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.RestructuringRegisterRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Reports.RestructuringRegisterTx(r.Context(), tx, kind)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.RestructuringRegisterRow{}
	}
	httpx.OK(w, map[string]any{"items": items})
}

// ─────────── Writeoffs ───────────

type writeoffReq struct {
	Reason string `json:"reason"`
}

func (h *LoanReportsHandler) WriteOff(w http.ResponseWriter, r *http.Request) {
	loanID, err := parseUUIDParam(r, "loan_id")
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	var in writeoffReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Reason == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("reason is required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	payload := LoanWriteoffPayload{LoanID: loanID, Reason: in.Reason}
	var result *LoanWriteoffResult
	var pending *domain.PendingApproval
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		toggles, err := h.Approvals.GetTogglesTx(r.Context(), tx)
		if err != nil {
			return err
		}
		if toggles.LoanWriteoff {
			loan, err := h.Loans.GetTx(r.Context(), tx, loanID)
			if err != nil {
				return err
			}
			memberID := loan.CounterpartyID
			amt := loan.PrincipalBalance.Add(loan.InterestBalance).Add(loan.FeesBalance).Add(loan.PenaltyBalance)
			pa, qerr := queueApproval(r.Context(), tx, QueueApprovalDeps{
				Workflow:       h.Workflow,
				Approvals:      h.Approvals,
				SavingsSelfURL: h.SavingsSelfURL,
			}, QueueApprovalInput{
				TenantID:        tid,
				Kind:            domain.ApprovalKindLoanWriteoff,
				Title:           "Write off loan " + loan.LoanNo,
				SubjectID:       loanID,
				SubjectMemberID: &memberID,
				SubjectLoanID:   &loanID,
				Amount:          &amt,
				Payload:         payload,
				MakerUserID:     userID,
				SummarySuffix:   " — " + amt.StringFixed(2),
				Narration:       in.Reason,
				ContextExtras: map[string]any{
					"loan_no": loan.LoanNo,
					"reason":  in.Reason,
				},
			})
			if qerr != nil {
				return qerr
			}
			pending = pa
			return nil
		}
		res, err := h.ExecuteWriteoffTx(r.Context(), tx, payload, userID)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if pending != nil {
		writePendingResponse(w, r, pending)
		return
	}
	httpx.OK(w, result)
}

func (h *LoanReportsHandler) WriteoffRegister(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.WriteoffRegisterRow
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Reports.WriteoffRegisterTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.WriteoffRegisterRow{}
	}
	httpx.OK(w, map[string]any{"items": items})
}

// ─────────── CRB ───────────

func (h *LoanReportsHandler) CRB(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.CRBLoanRecord
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Reports.CRBSubmissionTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if items == nil {
		items = []store.CRBLoanRecord{}
	}
	httpx.OK(w, map[string]any{"records": items, "record_count": len(items)})
}
