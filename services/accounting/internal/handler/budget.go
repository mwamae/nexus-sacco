// Budget HTTP surface.
//
//   GET    /v1/budgets                          list (optionally ?year=)
//   POST   /v1/budgets                          create draft
//   GET    /v1/budgets/{id}                     budget + lines
//   POST   /v1/budgets/{id}/lines/bulk-upsert   set/replace lines
//   POST   /v1/budgets/{id}/submit              draft → submitted
//   POST   /v1/budgets/{id}/approve             submitted → approved (maker ≠ checker)
//   POST   /v1/budgets/{id}/archive             draft/submitted/approved → archived
//   GET    /v1/budgets/{id}/variance?from=&to=  budget vs actuals
//
// Status machine + locking is enforced by the store; the handler maps
// the typed errors to clean HTTP codes.

package handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/middleware"
	"github.com/nexussacco/accounting/internal/store"
)

type BudgetHandler struct {
	DB      *db.Pool
	Budgets *store.BudgetStore
	Logger  *slog.Logger
}

// ─────────── Budgets ───────────

func (h *BudgetHandler) List(w http.ResponseWriter, r *http.Request) {
	year := 0
	if v := r.URL.Query().Get("year"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			year = n
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.Budget
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Budgets.ListTx(r.Context(), tx, year)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type createBudgetReq struct {
	Name        string `json:"name"`
	FiscalYear  int    `json:"fiscal_year"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	Notes       string `json:"notes,omitempty"`
}

func (h *BudgetHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in createBudgetReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Name == "" || in.FiscalYear == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("name and fiscal_year are required"))
		return
	}
	periodStart, err := time.Parse("2006-01-02", in.PeriodStart)
	if err != nil {
		// Default to Jan 1 of fiscal year if not provided.
		if in.PeriodStart == "" {
			periodStart = time.Date(in.FiscalYear, 1, 1, 0, 0, 0, 0, time.UTC)
		} else {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("period_start must be YYYY-MM-DD"))
			return
		}
	}
	periodEnd, err := time.Parse("2006-01-02", in.PeriodEnd)
	if err != nil {
		if in.PeriodEnd == "" {
			periodEnd = time.Date(in.FiscalYear, 12, 31, 0, 0, 0, 0, time.UTC)
		} else {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("period_end must be YYYY-MM-DD"))
			return
		}
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var b *domain.Budget
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		b, err = h.Budgets.CreateTx(r.Context(), tx, store.CreateBudgetInput{
			Name: in.Name, FiscalYear: in.FiscalYear,
			PeriodStart: periodStart, PeriodEnd: periodEnd,
			Notes: strPtr(in.Notes), CreatedBy: userID,
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, b)
}

type budgetDetailResp struct {
	Budget *domain.Budget       `json:"budget"`
	Lines  []domain.BudgetLine  `json:"lines"`
}

func (h *BudgetHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var resp budgetDetailResp
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		b, err := h.Budgets.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		resp.Budget = b
		resp.Lines, err = h.Budgets.ListLinesTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBudgetNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("budget not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, resp)
}

// ─────────── Lines ───────────

type bulkUpsertLinesReq struct {
	Lines []struct {
		AccountCode string `json:"account_code"`
		PeriodMonth int    `json:"period_month"`
		Amount      string `json:"amount"`
		Notes       string `json:"notes,omitempty"`
	} `json:"lines"`
}

func (h *BudgetHandler) BulkUpsertLines(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in bulkUpsertLinesReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if len(in.Lines) == 0 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("lines must not be empty"))
		return
	}
	lines := make([]store.BudgetLineUpsert, 0, len(in.Lines))
	for i, l := range in.Lines {
		amt, err := decimal.NewFromString(l.Amount)
		if err != nil || amt.IsNegative() {
			httpx.WriteErr(w, r, httpx.ErrBadRequest(fmt.Sprintf("lines[%d].amount must be a non-negative decimal", i)))
			return
		}
		lines = append(lines, store.BudgetLineUpsert{
			AccountCode: l.AccountCode, PeriodMonth: l.PeriodMonth, Amount: amt,
			Notes: strPtr(l.Notes),
		})
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Budgets.UpsertLinesTx(r.Context(), tx, id, lines)
	})
	if err != nil {
		if errors.Is(err, store.ErrBudgetLocked) {
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
			return
		}
		if errors.Is(err, store.ErrBudgetNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("budget not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	// Return the refreshed budget + lines for convenience.
	h.Get(w, r)
}

// ─────────── Transitions ───────────

func (h *BudgetHandler) Submit(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, func(tx pgx.Tx, id, uid uuid.UUID) (*domain.Budget, error) {
		return h.Budgets.SubmitTx(r.Context(), tx, id, uid)
	})
}

func (h *BudgetHandler) Approve(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, func(tx pgx.Tx, id, uid uuid.UUID) (*domain.Budget, error) {
		return h.Budgets.ApproveTx(r.Context(), tx, id, uid)
	})
}

func (h *BudgetHandler) Archive(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, func(tx pgx.Tx, id, uid uuid.UUID) (*domain.Budget, error) {
		return h.Budgets.ArchiveTx(r.Context(), tx, id, uid)
	})
}

func (h *BudgetHandler) transition(w http.ResponseWriter, r *http.Request, fn func(pgx.Tx, uuid.UUID, uuid.UUID) (*domain.Budget, error)) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var b *domain.Budget
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		b, err = fn(tx, id, userID)
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrBudgetNotFound):
			httpx.WriteErr(w, r, httpx.ErrNotFound("budget not found"))
		case errors.Is(err, domain.ErrIllegalBudgetTransition):
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		case errors.Is(err, store.ErrMakerEqualsChecker):
			httpx.WriteErr(w, r, httpx.ErrForbidden(err.Error()))
		case errors.Is(err, store.ErrAlreadyApprovedYr):
			httpx.WriteErr(w, r, httpx.ErrConflict(err.Error()))
		default:
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.OK(w, b)
}

// ─────────── Variance ───────────

func (h *BudgetHandler) Variance(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	from, to, ok := parseDateRange(w, r)
	if !ok {
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var report *store.VarianceReport
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		report, err = h.Budgets.VarianceTx(r.Context(), tx, id, from, to)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBudgetNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("budget not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, report)
}
