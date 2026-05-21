// /internal/v1/post — the auto-posting surface that other services
// (savings, loans, shares, …) call when a business transaction needs
// to land in the General Ledger.
//
// Gated by the shared X-Internal-Token header. The caller passes the
// tenant_id explicitly in the body since this endpoint isn't behind
// the subdomain-resolution middleware.
//
// The endpoint accepts account CODES (not UUIDs) on each line — the
// caller doesn't need to know account ids; the posting engine
// resolves them via the tenant's Chart of Accounts.

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/httpx"
	"github.com/nexussacco/accounting/internal/posting"
	"github.com/nexussacco/accounting/internal/store"
)

type InternalPostHandler struct {
	DB            *db.Pool
	Engine        *posting.Engine
	InternalToken string
	Logger        *slog.Logger
}

type postLineDTO struct {
	AccountCode string `json:"account_code"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Narration   string `json:"narration,omitempty"`
}

type postRequest struct {
	TenantID     uuid.UUID     `json:"tenant_id"`
	EntryDate    string        `json:"entry_date,omitempty"` // YYYY-MM-DD; defaults to today
	ValueDate    string        `json:"value_date,omitempty"`
	SourceModule string        `json:"source_module"`
	SourceRef    string        `json:"source_ref"`
	Narration    string        `json:"narration"`
	Lines        []postLineDTO `json:"lines"`
}

func (h *InternalPostHandler) Post(w http.ResponseWriter, r *http.Request) {
	if h.InternalToken != "" && r.Header.Get("X-Internal-Token") != h.InternalToken {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
		return
	}
	var in postRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TenantID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id is required"))
		return
	}
	if in.SourceModule == "" || in.SourceRef == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("source_module and source_ref are required"))
		return
	}
	if in.Narration == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("narration is required"))
		return
	}
	if len(in.Lines) < 2 {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("at least two lines required"))
		return
	}

	entryDate := time.Now()
	if in.EntryDate != "" {
		t, err := time.Parse("2006-01-02", in.EntryDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("entry_date must be YYYY-MM-DD"))
			return
		}
		entryDate = t
	}
	valueDate := entryDate
	if in.ValueDate != "" {
		t, err := time.Parse("2006-01-02", in.ValueDate)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("value_date must be YYYY-MM-DD"))
			return
		}
		valueDate = t
	}

	lines, perr := parsePostLines(in.Lines)
	if perr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(perr.Error()))
		return
	}

	// Idempotency — if a prior call already posted an entry with this
	// (source_module, source_ref), return that entry instead of
	// creating a duplicate. Saves the caller from having to track
	// "did the previous call commit before its TCP socket dropped?".
	var existing *domain.JournalEntry
	if err := h.DB.WithTenantTx(r.Context(), in.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(r.Context(),
			`SELECT id FROM journal_entries
			 WHERE source_module = $1 AND source_ref = $2 AND status = 'posted'`,
			in.SourceModule, in.SourceRef,
		)
		var id uuid.UUID
		err := row.Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		e, err := h.Engine.Journals.GetTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		existing = e
		return nil
	}); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if existing != nil {
		httpx.OK(w, map[string]any{
			"entry":      existing,
			"idempotent": true,
		})
		return
	}

	var posted *domain.JournalEntry
	err := h.DB.WithTenantTx(r.Context(), in.TenantID, func(tx pgx.Tx) error {
		e, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
			EntryDate:    entryDate,
			ValueDate:    valueDate,
			EntryType:    domain.TypeAuto,
			SourceModule: in.SourceModule,
			SourceRef:    in.SourceRef,
			Narration:    in.Narration,
			Lines:        lines,
		})
		if err != nil {
			return err
		}
		posted = e
		return nil
	})
	if err != nil {
		mapped := mapPostingErr(err)
		httpx.WriteErr(w, r, mapped)
		return
	}
	httpx.Created(w, posted)
}

func parsePostLines(in []postLineDTO) ([]posting.Line, error) {
	out := make([]posting.Line, 0, len(in))
	for i, ln := range in {
		if ln.AccountCode == "" {
			return nil, errLine(i, "account_code is required")
		}
		d, derr := parseAmt(ln.Debit)
		if derr != nil {
			return nil, errLine(i, "debit "+derr.Error())
		}
		c, cerr := parseAmt(ln.Credit)
		if cerr != nil {
			return nil, errLine(i, "credit "+cerr.Error())
		}
		out = append(out, posting.Line{
			AccountCode: ln.AccountCode, Debit: d, Credit: c, Narration: ln.Narration,
		})
	}
	return out, nil
}

func parseAmt(s string) (decimal.Decimal, error) {
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

func errLine(i int, msg string) error {
	return errors.New("line " + (intStr(i+1)) + ": " + msg)
}
func intStr(n int) string {
	// Tiny helper to avoid pulling strconv for one use.
	if n < 10 {
		return string('0' + byte(n))
	}
	// Fallback for two-digit-plus line counts (rare for a single journal).
	return "" + fmtInt(n)
}
func fmtInt(n int) string {
	digits := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// mapPostingErr turns engine-level errors into the right HTTP status.
func mapPostingErr(err error) error {
	switch {
	case errors.Is(err, store.ErrPeriodClosed):
		return httpx.ErrConflict("accounting period is closed for the entry_date")
	case errors.Is(err, posting.ErrUnknownAccount),
		errors.Is(err, posting.ErrInactiveAccount):
		return httpx.ErrBadRequest(err.Error())
	case errors.Is(err, store.ErrUnbalanced),
		errors.Is(err, store.ErrEmptyEntry),
		errors.Is(err, store.ErrBadLine):
		return httpx.ErrBadRequest(err.Error())
	default:
		return err
	}
}

// Force the context import to stay even if we later trim helpers.
var _ = context.Background
