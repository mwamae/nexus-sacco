// Journal entries — list / get / draft create / approve / reject.
//
// Manual entries created here go through maker/checker: a finance
// user creates a draft (status = pending_approval), a different user
// approves (status = posted) or rejects. The store enforces validation
// (balanced, ≥2 lines, each line single-sided); the handler enforces
// maker ≠ checker.
//
//   GET    /v1/journal-entries            list with filters
//   POST   /v1/journal-entries            create draft (pending_approval)
//   GET    /v1/journal-entries/{id}       detail with lines
//   POST   /v1/journal-entries/{id}/approve
//   POST   /v1/journal-entries/{id}/reject

package handler

import (
	"errors"
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

type JournalHandler struct {
	DB       *db.Pool
	CoA      *store.CoAStore
	Journals *store.JournalStore
	Periods  *store.PeriodStore
	Engine   *posting.Engine
	Logger   *slog.Logger

	// PR #7 — Unified Inbox workflow integration. Empty WorkflowURL
	// disables the gate; the legacy maker/checker stays the only path.
	WorkflowURL           string
	AccountingSelfURL     string
	WorkflowInternalToken string
	HTTP                  *http.Client
}

// ─────────── List / Get ───────────

func (h *JournalHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.EntryListFilter{
		Status:       q.Get("status"),
		EntryType:    q.Get("entry_type"),
		SourceModule: q.Get("source_module"),
		Limit:        limit,
		Offset:       offset,
	}
	if d := q.Get("from"); d != "" {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			f.FromDate = &t
		}
	}
	if d := q.Get("to"); d != "" {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			f.ToDate = &t
		}
	}
	var items []domain.JournalEntry
	var total int
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, total, err = h.Journals.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

func (h *JournalHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var (
		entry *domain.JournalEntry
		lines []domain.JournalLine
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var lerr error
		entry, lerr = h.Journals.GetTx(r.Context(), tx, id)
		if lerr != nil {
			return lerr
		}
		lines, lerr = h.Journals.LinesForTx(r.Context(), tx, id)
		return lerr
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrNotFound("journal entry not found"))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	entry.Lines = lines
	httpx.OK(w, entry)
}

// ─────────── Create draft ───────────

type lineDTO struct {
	AccountCode string `json:"account_code"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Narration   string `json:"narration,omitempty"`
}

type createEntryReq struct {
	EntryDate string    `json:"entry_date"`           // YYYY-MM-DD
	ValueDate string    `json:"value_date,omitempty"` // optional, defaults to entry_date
	EntryType string    `json:"entry_type,omitempty"` // manual / adjustment (defaults to manual)
	Narration string    `json:"narration"`
	Lines     []lineDTO `json:"lines"`
}

func (h *JournalHandler) Create(w http.ResponseWriter, r *http.Request) {
	var in createEntryReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.Narration == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("narration is required"))
		return
	}
	entryDate, err := time.Parse("2006-01-02", in.EntryDate)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("entry_date must be YYYY-MM-DD"))
		return
	}
	valueDate := entryDate
	if in.ValueDate != "" {
		valueDate, err = time.Parse("2006-01-02", in.ValueDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
	}
	entryType := domain.JournalEntryType(in.EntryType)
	if entryType == "" {
		entryType = domain.TypeManual
	}
	if entryType != domain.TypeManual && entryType != domain.TypeAdjustment {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("entry_type must be manual or adjustment"))
		return
	}
	if len(in.Lines) < 2 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least two lines are required"))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	actor, _ := middleware.UserIDFrom(r)

	var (
		out          *domain.JournalEntry
		totalDebit   decimal.Decimal
		affectsEquity bool
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Resolve account codes + parse decimals up-front so errors
		// surface before any insert.
		lines := make([]store.EntryLineInput, 0, len(in.Lines))
		for i, ln := range in.Lines {
			d, derr := parseAmount(ln.Debit)
			if derr != nil {
				return httpx.ErrBadRequest("line " + strconv.Itoa(i+1) + ": debit " + derr.Error())
			}
			c, cerr := parseAmount(ln.Credit)
			if cerr != nil {
				return httpx.ErrBadRequest("line " + strconv.Itoa(i+1) + ": credit " + cerr.Error())
			}
			if ln.AccountCode == "" {
				return httpx.ErrBadRequest("line " + strconv.Itoa(i+1) + ": account_code is required")
			}
			acct, aerr := h.CoA.GetByCodeTx(r.Context(), tx, ln.AccountCode)
			if aerr != nil {
				return httpx.ErrBadRequest("line " + strconv.Itoa(i+1) + ": unknown account_code " + ln.AccountCode)
			}
			// Track the totals that feed the workflow condition:
			// `amount` is the total debit side (== total credit, the
			// engine balance-checks at post time); `affects_equity`
			// is true if any line touches an equity account.
			totalDebit = totalDebit.Add(d)
			if acct.Class == domain.ClassEquity {
				affectsEquity = true
			}
			lines = append(lines, store.EntryLineInput{
				AccountID: acct.ID, Debit: d, Credit: c, Narration: ln.Narration,
			})
		}
		// Confirm the period is open. We don't auto-open here — drafts
		// for closed periods should be rejected before they pile up.
		if _, perr := h.Periods.EnsureOpenForDateTx(r.Context(), tx, entryDate); perr != nil {
			return httpx.ErrConflict(perr.Error())
		}
		creator := nullableUserID(actor)
		entry, cerr := h.Journals.CreateDraftTx(r.Context(), tx, store.CreateEntryInput{
			EntryDate: entryDate, ValueDate: valueDate,
			EntryType: entryType, Narration: in.Narration,
			Lines: lines, CreatedBy: creator,
		})
		if cerr != nil {
			return mapStoreErr(cerr)
		}
		out = entry
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	// PR #7 — when the tenant is on the Unified Inbox, every manual
	// JE goes through the workflow engine. The seeded definition's
	// condition skips both levels when amount ≤ 100k AND
	// affects_equity = false; in that case the engine auto-approves
	// at creation and fires the resolve callback immediately, which
	// posts the entry. Above-threshold or equity-touching entries
	// stop at the Reviewer level until a human acts.
	if h.WorkflowURL != "" && h.tenantHasUnifiedInbox(r.Context(), tid) {
		wfID, wfErr := h.createJEWorkflowInstance(r, tid, out, totalDebit, affectsEquity, actor, "manual_journal_entry")
		if wfErr != nil {
			// Roll back is hard at this point (separate tx); surface
			// the error but leave the draft in pending_approval —
			// the legacy Approve endpoint still works as a fallback.
			httpx.WriteErr(w, r, wfErr)
			return
		}
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			_, e := tx.Exec(r.Context(),
				`UPDATE journal_entries SET workflow_instance_id = $1 WHERE id = $2`, wfID, out.ID)
			return e
		})
		out.WorkflowInstanceID = &wfID
	}
	httpx.Created(w, out)
}

// ─────────── Approve / Reject ───────────

func (h *JournalHandler) Approve(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	actor, _ := middleware.UserIDFrom(r)
	var out *domain.JournalEntry
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		entry, lerr := h.Journals.GetTx(r.Context(), tx, id)
		if lerr != nil {
			return lerr
		}
		if entry.Status != domain.EntryPendingApproval {
			return httpx.ErrConflict("entry is " + string(entry.Status))
		}
		if entry.CreatedBy != nil && *entry.CreatedBy == actor {
			return httpx.ErrConflict("approver must differ from creator (maker/checker)")
		}
		// Re-confirm the period is open at approval time.
		if _, perr := h.Periods.EnsureOpenForDateTx(r.Context(), tx, entry.EntryDate); perr != nil {
			return httpx.ErrConflict(perr.Error())
		}
		posted, perr := h.Journals.ApproveAndPostTx(r.Context(), tx, id, actor)
		if perr != nil {
			return perr
		}
		out = posted
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteErr(w, r, httpx.ErrNotFound("journal entry not found"))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

type rejectReq struct {
	Reason string `json:"reason"`
}

func (h *JournalHandler) Reject(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in rejectReq
	if r.ContentLength > 0 {
		_ = httpx.DecodeJSON(r, &in)
	}
	tid, _ := middleware.TenantIDFrom(r)
	actor, _ := middleware.UserIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return h.Journals.RejectTx(r.Context(), tx, id, actor, in.Reason)
	})
	if errors.Is(err, store.ErrNotEditable) {
		httpx.WriteErr(w, r, httpx.ErrConflict("entry is no longer pending approval"))
		return
	}
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// ─────────── Helpers ───────────

func parseAmount(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, errors.New("must be a decimal number")
	}
	if d.IsNegative() {
		return decimal.Zero, errors.New("must be non-negative")
	}
	return d, nil
}

func nullableUserID(u uuid.UUID) *uuid.UUID {
	if u == uuid.Nil {
		return nil
	}
	return &u
}

// mapStoreErr surfaces business-validation errors from the store as
// 400/409 rather than letting them fall through to 500.
func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrUnbalanced),
		errors.Is(err, store.ErrEmptyEntry),
		errors.Is(err, store.ErrBadLine):
		return httpx.ErrBadRequest(err.Error())
	case errors.Is(err, store.ErrNotEditable):
		return httpx.ErrConflict(err.Error())
	default:
		return err
	}
}
