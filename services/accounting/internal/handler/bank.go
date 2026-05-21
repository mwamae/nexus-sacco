// Bank reconciliation HTTP surface.
//
// Routes:
//   GET    /v1/bank-accounts
//   POST   /v1/bank-accounts
//   GET    /v1/bank-accounts/{id}
//   PATCH  /v1/bank-accounts/{id}
//   GET    /v1/bank-accounts/{id}/statements
//   POST   /v1/bank-accounts/{id}/statements    — multipart CSV upload
//   GET    /v1/bank-accounts/{id}/reconciliation?as_of=YYYY-MM-DD
//
//   GET    /v1/bank-statements/{id}             — header + lines
//   GET    /v1/bank-statement-lines/{id}/suggest-matches
//   POST   /v1/bank-statement-lines/{id}/match
//   POST   /v1/bank-statement-lines/{id}/unmatch
//   POST   /v1/bank-statement-lines/{id}/exclude
//   POST   /v1/bank-statement-lines/{id}/post-adjustment
//
// CSV format expected for upload (header row required):
//   txn_date,value_date,description,reference,debit,credit,running_balance
//   2026-05-01,2026-05-01,"Salary",PAY123,0,50000,150000
//   2026-05-02,2026-05-02,"Bank charge",CHG,250,0,149750
//
// The first row of data establishes line_no = 1.

package handler

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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

type BankHandler struct {
	DB      *db.Pool
	Bank    *store.BankStore
	CoA     *store.CoAStore
	Engine  *posting.Engine
	Logger  *slog.Logger
}

// ─────────── Accounts ───────────

func (h *BankHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	activeOnly := r.URL.Query().Get("active") == "true"
	var items []domain.BankAccount
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Bank.ListAccountsTx(r.Context(), tx, activeOnly)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

type createBankAccountReq struct {
	GLAccountCode string  `json:"gl_account_code"`
	BankName      string  `json:"bank_name"`
	AccountNumber string  `json:"account_number"`
	Branch        string  `json:"branch,omitempty"`
	CurrencyCode  string  `json:"currency_code,omitempty"`
	Notes         string  `json:"notes,omitempty"`
}

func (h *BankHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var in createBankAccountReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.GLAccountCode == "" || in.BankName == "" || in.AccountNumber == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("gl_account_code, bank_name, account_number are required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var created *domain.BankAccount
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Validate the GL account exists + is a cash-like account.
		a, err := h.CoA.GetByCodeTx(r.Context(), tx, in.GLAccountCode)
		if err != nil {
			return httpx.ErrBadRequest("gl_account_code not found in chart of accounts")
		}
		if string(a.Class) != "asset" {
			return httpx.ErrBadRequest("gl_account_code must be an asset account")
		}
		created, err = h.Bank.CreateAccountTx(r.Context(), tx, store.CreateBankAccountInput{
			GLAccountCode: in.GLAccountCode,
			BankName:      in.BankName,
			AccountNumber: in.AccountNumber,
			Branch:        strPtr(in.Branch),
			CurrencyCode:  in.CurrencyCode,
			Notes:         strPtr(in.Notes),
		})
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, created)
}

func (h *BankHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var b *domain.BankAccount
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		b, err = h.Bank.GetAccountTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBankAccountNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank account not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, b)
}

type updateBankAccountReq struct {
	BankName      *string `json:"bank_name,omitempty"`
	AccountNumber *string `json:"account_number,omitempty"`
	Branch        *string `json:"branch,omitempty"`
	CurrencyCode  *string `json:"currency_code,omitempty"`
	IsActive      *bool   `json:"is_active,omitempty"`
	Notes         *string `json:"notes,omitempty"`
}

func (h *BankHandler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in updateBankAccountReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var updated *domain.BankAccount
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		updated, err = h.Bank.UpdateAccountTx(r.Context(), tx, id, store.UpdateBankAccountInput{
			BankName: in.BankName, AccountNumber: in.AccountNumber, Branch: in.Branch,
			CurrencyCode: in.CurrencyCode, IsActive: in.IsActive, Notes: in.Notes,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBankAccountNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank account not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, updated)
}

// ─────────── Statement upload ───────────

func (h *BankHandler) UploadStatement(w http.ResponseWriter, r *http.Request) {
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

	// Accept either multipart/form-data with `file=` or a raw CSV body.
	var (
		reader   io.Reader
		filename string
	)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid multipart form: "+err.Error()))
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("file field required"))
			return
		}
		defer f.Close()
		reader = f
		filename = hdr.Filename
	} else {
		reader = r.Body
		filename = r.Header.Get("X-Filename")
	}

	parsed, hdr, err := parseStatementCSV(reader)
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("csv parse: "+err.Error()))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	var stmt *domain.BankStatement
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		if _, err := h.Bank.GetAccountTx(r.Context(), tx, id); err != nil {
			return err
		}
		var statementDate time.Time
		if len(parsed) > 0 {
			statementDate = parsed[len(parsed)-1].TxnDate
		} else {
			statementDate = time.Now()
		}
		input := store.CreateStatementInput{
			BankAccountID:  id,
			StatementDate:  statementDate,
			SourceFormat:   "csv",
			SourceFilename: strPtr(filename),
			UploadedBy:     userID,
			Lines:          parsed,
		}
		if hdr.PeriodStart != nil {
			input.PeriodStart = hdr.PeriodStart
		}
		if hdr.PeriodEnd != nil {
			input.PeriodEnd = hdr.PeriodEnd
		}
		if hdr.OpeningBalance != nil {
			input.OpeningBalance = hdr.OpeningBalance
		}
		if hdr.ClosingBalance != nil {
			input.ClosingBalance = hdr.ClosingBalance
		}
		var err error
		stmt, err = h.Bank.CreateStatementWithLinesTx(r.Context(), tx, input)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBankAccountNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank account not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.Created(w, stmt)
}

func (h *BankHandler) ListStatements(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.BankStatement
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.Bank.ListStatementsTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items)})
}

func (h *BankHandler) GetStatement(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var stmt *domain.BankStatement
	var lines []domain.BankStatementLine
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		stmt, err = h.Bank.GetStatementTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		lines, err = h.Bank.ListLinesByStatementTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBankStatementNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank statement not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"statement": stmt, "lines": lines})
}

// ─────────── Match operations ───────────

func (h *BankHandler) SuggestMatches(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	dayTolerance := 5
	if v := r.URL.Query().Get("tolerance"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 30 {
			dayTolerance = n
		}
	}
	tid, _ := middleware.TenantIDFrom(r)
	var candidates []store.MatchCandidate
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		line, err := h.Bank.GetLineTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		candidates, err = h.Bank.SuggestMatchesTx(r.Context(), tx, *line, dayTolerance)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBankLineNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank statement line not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": candidates, "total": len(candidates)})
}

type matchReq struct {
	JournalLineID uuid.UUID `json:"journal_line_id"`
	Notes         string    `json:"notes,omitempty"`
	Manual        bool      `json:"manual,omitempty"`
}

func (h *BankHandler) Match(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in matchReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.JournalLineID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("journal_line_id is required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	kind := domain.BankLineMatched
	if in.Manual {
		kind = domain.BankLineManualMatch
	}

	tid, _ := middleware.TenantIDFrom(r)
	var line *domain.BankStatementLine
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		line, err = h.Bank.MatchTx(r.Context(), tx, id, in.JournalLineID, kind, userID, strPtr(in.Notes))
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrBankLineNotFound):
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank statement line not found"))
		case errors.Is(err, store.ErrLineAlreadyMatched):
			httpx.WriteErr(w, r, httpx.ErrConflict("line is already matched"))
		case errors.Is(err, store.ErrJournalAlreadyMatched):
			httpx.WriteErr(w, r, httpx.ErrConflict("journal line is already matched to another bank line"))
		default:
			httpx.WriteErr(w, r, err)
		}
		return
	}
	httpx.OK(w, line)
}

func (h *BankHandler) Unmatch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var line *domain.BankStatementLine
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		line, err = h.Bank.UnmatchTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, line)
}

type excludeReq struct {
	Reason string `json:"reason"`
}

func (h *BankHandler) Exclude(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in excludeReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var line *domain.BankStatementLine
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		line, err = h.Bank.ExcludeTx(r.Context(), tx, id, userID, in.Reason)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, line)
}

// PostAdjustment — for an unmatched bank line (e.g. a bank charge or
// interest credit the SACCO didn't book), create a journal entry that
// debits or credits the matching expense/income account and links the
// resulting GL line back to this bank line as match_status='adjusted'.
type adjustmentReq struct {
	OffsetAccountCode string `json:"offset_account_code"`
	Narration         string `json:"narration"`
	Notes             string `json:"notes,omitempty"`
}

func (h *BankHandler) PostAdjustment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in adjustmentReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.OffsetAccountCode == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("offset_account_code is required"))
		return
	}
	userID, _ := middleware.UserIDFrom(r)
	if userID == uuid.Nil {
		httpx.WriteErr(w, r, httpx.ErrUnauthorized("user identity required"))
		return
	}

	tid, _ := middleware.TenantIDFrom(r)
	var line *domain.BankStatementLine
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		bankLine, err := h.Bank.GetLineTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if bankLine.MatchStatus != domain.BankLineUnmatched {
			return httpx.ErrConflict("only unmatched lines can be adjusted")
		}
		acct, err := h.Bank.GetAccountTx(r.Context(), tx, bankLine.BankAccountID)
		if err != nil {
			return err
		}

		// Build the journal entry:
		//   bank credit (money in): DR bank GL, CR offset account
		//   bank debit  (money out): DR offset account, CR bank GL
		var lines []posting.Line
		narration := in.Narration
		if narration == "" {
			narration = "Bank adjustment for unmatched statement line"
		}
		if !bankLine.Credit.IsZero() {
			lines = []posting.Line{
				{AccountCode: acct.GLAccountCode, Debit: bankLine.Credit, Narration: narration},
				{AccountCode: in.OffsetAccountCode, Credit: bankLine.Credit, Narration: narration},
			}
		} else if !bankLine.Debit.IsZero() {
			lines = []posting.Line{
				{AccountCode: in.OffsetAccountCode, Debit: bankLine.Debit, Narration: narration},
				{AccountCode: acct.GLAccountCode, Credit: bankLine.Debit, Narration: narration},
			}
		} else {
			return httpx.ErrBadRequest("bank line has no amount to adjust")
		}

		entry, err := h.Engine.PostTx(r.Context(), tx, posting.PostInput{
			EntryDate:    bankLine.TxnDate,
			ValueDate:    bankLine.TxnDate,
			EntryType:    domain.TypeAdjustment,
			SourceModule: "accounting.bank-reconciliation",
			SourceRef:    fmt.Sprintf("bank-line-%s", bankLine.ID),
			Narration:    narration,
			Lines:        lines,
			PostedBy:     &userID,
		})
		if err != nil {
			return fmt.Errorf("post adjustment: %w", err)
		}

		// Find the journal_line on the bank GL account for this entry —
		// that's the leg to link to.
		var jLineID uuid.UUID
		if err := tx.QueryRow(r.Context(), `
			SELECT l.id FROM journal_lines l
			  JOIN chart_of_accounts a ON a.id = l.account_id
			 WHERE l.entry_id = $1 AND a.code = $2
			 LIMIT 1
		`, entry.ID, acct.GLAccountCode).Scan(&jLineID); err != nil {
			return fmt.Errorf("find bank leg of adjustment: %w", err)
		}

		notes := in.Notes
		if notes == "" {
			notes = "Auto-adjustment posted: " + narration
		}
		line, err = h.Bank.MarkAdjustedTx(r.Context(), tx, id, jLineID, userID, notes)
		return err
	})
	if err != nil {
		if e, ok := err.(*httpx.APIError); ok {
			httpx.WriteErr(w, r, e)
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, line)
}

// ─────────── Reconciliation report ───────────

func (h *BankHandler) Reconciliation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	asOf := time.Now()
	if v := r.URL.Query().Get("as_of"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("as_of must be YYYY-MM-DD"))
			return
		}
		asOf = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	}
	tid, _ := middleware.TenantIDFrom(r)
	var report *store.ReconciliationReport
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		report, err = h.Bank.ReconciliationTx(r.Context(), tx, id, asOf)
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrBankAccountNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("bank account not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, report)
}

// ─────────── CSV parsing ───────────

type csvHeaderInfo struct {
	PeriodStart    *time.Time
	PeriodEnd      *time.Time
	OpeningBalance *decimal.Decimal
	ClosingBalance *decimal.Decimal
}

// parseStatementCSV reads the upload. Header row required. Returns the
// parsed lines + derived metadata (opening/closing balance from the
// running_balance column if present).
func parseStatementCSV(r io.Reader) ([]store.ParsedStatementLine, csvHeaderInfo, error) {
	var info csvHeaderInfo
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, info, fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, col := range header {
		idx[strings.ToLower(strings.TrimSpace(col))] = i
	}
	required := []string{"txn_date", "debit", "credit"}
	for _, k := range required {
		if _, ok := idx[k]; !ok {
			return nil, info, fmt.Errorf("missing required column %q", k)
		}
	}
	out := []store.ParsedStatementLine{}
	for {
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, info, fmt.Errorf("read row %d: %w", len(out)+2, err)
		}
		var ln store.ParsedStatementLine
		ln.TxnDate, err = parseFlexibleDate(row[idx["txn_date"]])
		if err != nil {
			return nil, info, fmt.Errorf("row %d txn_date: %w", len(out)+2, err)
		}
		if i, ok := idx["value_date"]; ok && i < len(row) && strings.TrimSpace(row[i]) != "" {
			vd, err := parseFlexibleDate(row[i])
			if err == nil {
				ln.ValueDate = &vd
			}
		}
		if i, ok := idx["description"]; ok && i < len(row) {
			ln.Description = strings.TrimSpace(row[i])
		}
		if i, ok := idx["reference"]; ok && i < len(row) {
			ln.Reference = strings.TrimSpace(row[i])
		}
		ln.Debit, err = parseDec(row[idx["debit"]])
		if err != nil {
			return nil, info, fmt.Errorf("row %d debit: %w", len(out)+2, err)
		}
		ln.Credit, err = parseDec(row[idx["credit"]])
		if err != nil {
			return nil, info, fmt.Errorf("row %d credit: %w", len(out)+2, err)
		}
		if i, ok := idx["running_balance"]; ok && i < len(row) && strings.TrimSpace(row[i]) != "" {
			rb, err := parseDec(row[i])
			if err == nil {
				ln.RunningBalance = &rb
			}
		}
		out = append(out, ln)
	}
	if len(out) > 0 {
		// Derive period start/end from first/last txn date.
		first := out[0].TxnDate
		last := out[len(out)-1].TxnDate
		info.PeriodStart = &first
		info.PeriodEnd = &last
		// Closing balance = last row's running_balance (if present).
		if out[len(out)-1].RunningBalance != nil {
			info.ClosingBalance = out[len(out)-1].RunningBalance
		}
		// Opening = first row's running_balance − (its debit/credit applied)
		if first := out[0]; first.RunningBalance != nil {
			opening := first.RunningBalance.Sub(first.Credit).Add(first.Debit)
			info.OpeningBalance = &opening
		}
	}
	return out, info, nil
}

func parseFlexibleDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "02/01/2006", "01/02/2006", "2006/01/02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date %q", s)
}

func parseDec(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	if s == "" || s == "-" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
