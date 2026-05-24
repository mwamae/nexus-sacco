// Fiscal year close — posts the closing journal that zeros all P&L
// accounts into Retained Earnings (3010) and locks every monthly
// period in the year.
//
// Fiscal year for now = calendar year. The closing journal:
//   • DR every income account by its closing credit balance → 0
//   • CR every expense account by its closing debit balance → 0
//   • The net (income − expense) is the period surplus:
//       - Surplus  → CR Retained Earnings 3010
//       - Deficit  → DR Retained Earnings 3010
//
// Posted as one balanced entry with entry_type='adjustment' (we don't
// have a dedicated 'closing' enum value yet; the narration tags it).
//
// After posting, all 12 monthly periods of the year flip to 'closed'.

package handler

import (
	"context"
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
	"github.com/nexussacco/accounting/internal/posting"
	"github.com/nexussacco/accounting/internal/store"
)

type FiscalYearHandler struct {
	DB          *db.Pool
	FY          *store.FiscalYearStore
	Proposals   *store.FiscalYearProposalStore // PR #6 — Unified Inbox proposal rows
	Periods     *store.PeriodStore
	Engine      *posting.Engine
	Logger      *slog.Logger

	// PR #6 — workflow service integration. Empty means the
	// SubmitForClose endpoint returns a 409 "workflow service not
	// configured" and the legacy Close stays the only path.
	WorkflowURL           string
	AccountingSelfURL     string
	WorkflowInternalToken string
	HTTP                  *http.Client
}

func (h *FiscalYearHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	var items []store.FiscalYearClose
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.FY.ListTx(r.Context(), tx)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type closeYearReq struct {
	Notes string `json:"notes,omitempty"`
}

func (h *FiscalYearHandler) Close(w http.ResponseWriter, r *http.Request) {
	yearStr := chi.URLParam(r, "year")
	year, err := strconv.Atoi(yearStr)
	if err != nil || year < 2000 || year > 2999 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("year must be a 4-digit number"))
		return
	}
	var in closeYearReq
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(r, &in); err != nil {
			httpx.WriteErr(w, r, err)
			return
		}
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	fyStart := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	fyEnd := time.Date(year, 12, 31, 23, 59, 59, 0, time.UTC)

	var result *store.FiscalYearClose
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		recorded, err := h.executeCloseTx(r.Context(), tx, year, fyStart, fyEnd, userID, in.Notes)
		if err != nil {
			return err
		}
		result = recorded
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrYearAlreadyClosed) {
			httpx.WriteErr(w, r, httpx.ErrConflict("fiscal year "+yearStr+" is already closed"))
			return
		}
		h.Logger.Error("close fiscal year", "year", year, "err", err)
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, result)
}

func strPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// executeCloseTx is the body of the Close handler extracted so the
// Unified Inbox resolve callback (PR #6) can fire the same logic
// after a Board approval. Called inside a tenant-scoped tx.
func (h *FiscalYearHandler) executeCloseTx(
	ctx context.Context, tx pgx.Tx,
	year int, fyStart, fyEnd time.Time,
	userID uuid.UUID, notes string,
) (*store.FiscalYearClose, error) {
	yearStr := strconv.Itoa(year)
	alreadyClosed, err := h.FY.IsClosedTx(ctx, tx, year)
	if err != nil {
		return nil, err
	}
	if alreadyClosed {
		return nil, store.ErrYearAlreadyClosed
	}
	if _, err := h.Periods.EnsureOpenForDateTx(ctx, tx, fyEnd); err != nil {
		return nil, fmt.Errorf("december period: %w", err)
	}
	closing, err := h.FY.PLBalancesAsOfTx(ctx, tx, fyEnd)
	if err != nil {
		return nil, fmt.Errorf("read P&L balances: %w", err)
	}
	if len(closing) == 0 {
		return nil, httpx.ErrBadRequest("no income or expense activity in " + yearStr + " — nothing to close")
	}
	var totalIncome, totalExpense decimal.Decimal
	var incomeAcctCount, expenseAcctCount int
	lines := make([]posting.Line, 0, len(closing)+1)
	for _, cl := range closing {
		if cl.Class == "income" {
			totalIncome = totalIncome.Add(cl.Balance)
			incomeAcctCount++
			lines = append(lines, posting.Line{AccountCode: cl.AccountCode, Debit: cl.Balance, Narration: "Close " + cl.AccountName})
		} else {
			totalExpense = totalExpense.Add(cl.Balance)
			expenseAcctCount++
			lines = append(lines, posting.Line{AccountCode: cl.AccountCode, Credit: cl.Balance, Narration: "Close " + cl.AccountName})
		}
	}
	netSurplus := totalIncome.Sub(totalExpense)
	if netSurplus.IsPositive() {
		lines = append(lines, posting.Line{AccountCode: "3010", Credit: netSurplus, Narration: "FY " + yearStr + " net surplus → retained earnings"})
	} else if netSurplus.IsNegative() {
		lines = append(lines, posting.Line{AccountCode: "3010", Debit: netSurplus.Neg(), Narration: "FY " + yearStr + " net deficit ← retained earnings"})
	}
	entry, err := h.Engine.PostTx(ctx, tx, posting.PostInput{
		EntryDate:    fyEnd,
		ValueDate:    fyEnd,
		EntryType:    domain.TypeAdjustment,
		SourceModule: "accounting.fiscal-year-close",
		SourceRef:    yearStr,
		Narration:    "Year-end closing entry for FY " + yearStr,
		Lines:        lines,
		PostedBy:     &userID,
	})
	if err != nil {
		return nil, fmt.Errorf("post closing entry: %w", err)
	}
	recorded, err := h.FY.RecordCloseTx(ctx, tx, store.FiscalYearClose{
		Year:            year,
		FYStart:         fyStart,
		FYEnd:           fyEnd,
		ClosingEntryID:  entry.ID,
		TotalIncome:     totalIncome,
		TotalExpense:    totalExpense,
		NetSurplus:      netSurplus,
		IncomeAccounts:  incomeAcctCount,
		ExpenseAccounts: expenseAcctCount,
		ClosedBy:        userID,
		Notes:           strPtrIfNotEmpty(notes),
	})
	if err != nil {
		return nil, fmt.Errorf("record close: %w", err)
	}
	if err := h.FY.LockAllPeriodsInYearTx(ctx, tx, year, userID); err != nil {
		return nil, fmt.Errorf("lock periods: %w", err)
	}
	return recorded, nil
}
