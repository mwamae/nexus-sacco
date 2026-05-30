// DSID Phase 2.2 — Standing orders HTTP surface.
//
//   POST   /v1/members/{counterparty_id}/standing-orders
//   GET    /v1/members/{counterparty_id}/standing-orders
//   PATCH  /v1/standing-orders/{id}
//   DELETE /v1/standing-orders/{id}                        (soft → cancelled)
//   GET    /v1/standing-orders/{id}/runs
//   POST   /v1/standing-orders/{id}/resume
//
// Resume is workflow-gated when the order was suspended within the
// last 7 days (process_kind=standing_order_resume, seeded by workflow
// migration 0013); outside that window resume is direct.

package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

type StandingOrdersHandler struct {
	DB                *db.Pool
	RecurringDeposits *store.RecurringDepositStore
	Workflow          *workflowclient.Client // nil-safe
}

// ─────────── POST /v1/members/{cp_id}/standing-orders ───────────

type createSOReq struct {
	TargetAccountID       uuid.UUID        `json:"target_account_id"`
	Source                string           `json:"source"`
	SourceAccountID       *uuid.UUID       `json:"source_account_id,omitempty"`
	SourceMSISDN          string           `json:"source_msisdn,omitempty"`
	SourcePayrollEmployer string           `json:"source_payroll_employer,omitempty"`
	Amount                decimal.Decimal  `json:"amount"`
	Frequency             string           `json:"frequency"`
	StartDate             string           `json:"start_date"`        // YYYY-MM-DD
	EndDate               string           `json:"end_date,omitempty"`
}

func (h *StandingOrdersHandler) Create(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	var in createSOReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TargetAccountID == uuid.Nil ||
		!validSource(in.Source) ||
		in.Amount.LessThanOrEqual(decimal.Zero) ||
		!validFrequency(in.Frequency) ||
		in.StartDate == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("target_account_id, source, amount, frequency, start_date required"))
		return
	}
	switch in.Source {
	case "fosa_debit":
		if in.SourceAccountID == nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("source_account_id required for fosa_debit"))
			return
		}
	case "payroll":
		if in.SourcePayrollEmployer == "" {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("source_payroll_employer required for payroll"))
			return
		}
	}
	startDate, perr := time.Parse("2006-01-02", in.StartDate)
	if perr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("start_date must be YYYY-MM-DD"))
		return
	}
	var endDate *time.Time
	if in.EndDate != "" {
		t, perr := time.Parse("2006-01-02", in.EndDate)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("end_date must be YYYY-MM-DD"))
			return
		}
		endDate = &t
	}

	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	var out *store.RecurringDeposit
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		ci := store.CreateRecurringDepositInput{
			TenantID:        tid,
			CounterpartyID:  cpID,
			TargetAccountID: in.TargetAccountID,
			Source:          in.Source,
			SourceAccountID: in.SourceAccountID,
			Amount:          in.Amount,
			Frequency:       in.Frequency,
			StartDate:       startDate,
			EndDate:         endDate,
			CreatedBy:       uid,
		}
		if in.SourceMSISDN != "" {
			s := in.SourceMSISDN
			ci.SourceMSISDN = &s
		}
		if in.SourcePayrollEmployer != "" {
			s := in.SourcePayrollEmployer
			ci.SourcePayrollEmployer = &s
		}
		rd, err := h.RecurringDeposits.CreateTx(r.Context(), tx, ci)
		if err != nil {
			return err
		}
		out = rd
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, out)
}

// ─────────── GET /v1/members/{cp_id}/standing-orders ───────────

func (h *StandingOrdersHandler) ListByMember(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.RecurringDeposit
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		l, err := h.RecurringDeposits.ListTx(r.Context(), tx, store.ListRecurringDepositsFilter{
			CounterpartyID: &cpID,
			Status:         r.URL.Query().Get("status"),
		})
		if err != nil {
			return err
		}
		items = l
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}

// ─────────── PATCH /v1/standing-orders/{id} ───────────

type patchSOReq struct {
	Amount      *decimal.Decimal `json:"amount,omitempty"`
	Frequency   *string          `json:"frequency,omitempty"`
	EndDate     *string          `json:"end_date,omitempty"`
	Status      *string          `json:"status,omitempty"` // 'active' | 'paused'
	ReasonNotes *string          `json:"reason_notes,omitempty"`
}

func (h *StandingOrdersHandler) Patch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in patchSOReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Status != nil {
		switch *in.Status {
		case "active", "paused":
		default:
			httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be 'active' or 'paused'"))
			return
		}
	}
	if in.Frequency != nil && !validFrequency(*in.Frequency) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid frequency"))
		return
	}
	upd := store.UpdateRecurringDepositInput{ID: id}
	upd.Amount = in.Amount
	upd.Frequency = in.Frequency
	upd.Status = in.Status
	upd.ReasonNotes = in.ReasonNotes
	if in.EndDate != nil {
		if *in.EndDate == "" {
			// Empty string clears end_date.
			var zero time.Time
			upd.EndDate = &zero
		} else {
			t, perr := time.Parse("2006-01-02", *in.EndDate)
			if perr != nil {
				httpx.WriteErr(w, r, httpx.ErrBadRequest("end_date must be YYYY-MM-DD"))
				return
			}
			upd.EndDate = &t
		}
	}

	tid, _ := middleware.TenantIDFrom(r)
	var out *store.RecurringDeposit
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rd, err := h.RecurringDeposits.UpdateTx(r.Context(), tx, upd)
		if err != nil {
			return err
		}
		out = rd
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── DELETE /v1/standing-orders/{id} ───────────

func (h *StandingOrdersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	reason := "cancelled by officer"
	cancelled := "cancelled"
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		_, err := h.RecurringDeposits.UpdateTx(r.Context(), tx, store.UpdateRecurringDepositInput{
			ID:          id,
			Status:      &cancelled,
			ReasonNotes: &reason,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── GET /v1/standing-orders/{id}/runs ───────────

func (h *StandingOrdersHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.RecurringDepositRun
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		l, err := h.RecurringDeposits.ListRunsTx(r.Context(), tx, id, 200)
		if err != nil {
			return err
		}
		items = l
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}

// ─────────── POST /v1/standing-orders/{id}/resume ───────────
//
// If last_suspended_at is within the last 7 days, files a workflow
// approval (process_kind=standing_order_resume); otherwise flips
// status='active' immediately.

func (h *StandingOrdersHandler) Resume(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)

	var current *store.RecurringDeposit
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rd, err := h.RecurringDeposits.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		current = rd
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if current.Status != "suspended" && current.Status != "paused" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("standing order is not paused or suspended"))
		return
	}

	gate := current.LastSuspendedAt != nil && time.Since(*current.LastSuspendedAt) < 7*24*time.Hour
	if gate && h.Workflow != nil {
		var wfID uuid.UUID
		err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			if !h.Workflow.HasActiveDefinitionTx(r.Context(), tx, tid, "standing_order_resume") {
				return nil
			}
			fid, ferr := h.Workflow.CreateInstanceTx(r.Context(), tx, workflowclient.CreateInstanceInput{
				TenantID:    tid,
				ProcessKind: "standing_order_resume",
				SubjectKind: "standing_order",
				SubjectID:   id,
				Context: map[string]any{
					"standing_order_id": id.String(),
					"action":            "resume",
					"requested_by":      uid.String(),
				},
				MakerUserID: uid,
				Summary:     "Resume suspended standing order",
			})
			if ferr != nil {
				return ferr
			}
			wfID = fid
			return nil
		})
		if err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
		if wfID != uuid.Nil {
			httpx.OK(w, map[string]any{
				"status":               "approval_required",
				"workflow_instance_id": wfID,
			})
			return
		}
		// No active definition seeded — fall through to direct flip.
	}

	// Direct flip.
	active := "active"
	var out *store.RecurringDeposit
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		rd, err := h.RecurringDeposits.UpdateTx(r.Context(), tx, store.UpdateRecurringDepositInput{
			ID:     id,
			Status: &active,
		})
		if err != nil {
			return err
		}
		out = rd
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── helpers ───────────

func validSource(s string) bool {
	switch s {
	case "manual_reminder", "payroll", "mpesa_pull", "fosa_debit":
		return true
	}
	return false
}

func validFrequency(s string) bool {
	switch s {
	case "weekly", "biweekly", "monthly", "quarterly":
		return true
	}
	return false
}

