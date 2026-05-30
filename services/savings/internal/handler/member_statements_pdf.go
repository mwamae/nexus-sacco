// DSID Phase 2.1 — member statement PDF endpoints + email-to-member.
//
// Five endpoints under /v1/members/{counterparty_id}/statements:
//   GET  /deposits.pdf?from=&to=&account_id?=
//   GET  /shares.pdf?fy=
//   GET  /interest.pdf?fy=
//   GET  /dividend.pdf?fy=
//   POST /email           body: {kind, period, account_id?}
//
// The four GETs render synchronously: build the payload struct from
// the underlying tables, hand off to notifier.GeneratePDF, fetch the
// rendered bytes via the public-download token, stream back. The 24h
// cache lives in-process and is keyed on (member, kind, period, max_txn_at).
//
// The POST queues an email via notifier.Notify with the matching
// PDFAttachmentSpec — the notification service re-renders the PDF on
// its side, attaches the bytes to the outbound email, and dispatches
// via SMTP.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/notifier"
	"github.com/nexussacco/savings/internal/store"
)

type MemberStatementsPDFHandler struct {
	DB         *db.Pool
	Statements *store.StatementsStore
	Notifier   *notifier.Client
	Logger     *slog.Logger

	cache sync.Map // key: cacheKey  value: cachedPDF
}

type cacheKey struct {
	MemberID uuid.UUID
	Kind     string
	Period   string
	Variant  string // account_id when scoped per-account
}

type cachedPDF struct {
	Bytes      []byte
	GeneratedAt time.Time
	MaxTxnAt   time.Time
}

const statementCacheTTL = 24 * time.Hour

// ─────────── shared loader for tenant + member info ───────────

type statementContext struct {
	TenantName       string
	TenantAddress    string
	TenantDisclaimer string
	MemberName       string
	MemberNo         string
	MemberEmail      string
	MemberID         uuid.UUID
}

func (h *MemberStatementsPDFHandler) loadStatementContextTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (*statementContext, error) {
	var (
		tenantName, tenantAddr string
		memberName, memberNo string
		memberEmail            *string
		memberID               *uuid.UUID
	)
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(t.name, ''),
		  COALESCE(t.legal_name, t.name, ''),
		  COALESCE(cd.full_name, ''),
		  COALESCE(cd.member_no, ''),
		  m.email::text,
		  m.id
		  FROM tenants t
		  CROSS JOIN counterparty_directory cd
		  LEFT JOIN members m ON m.id = cd.member_id
		 WHERE cd.counterparty_id = $1
		   AND t.id = current_tenant_id()
		 LIMIT 1
	`, cpID).Scan(&tenantName, &tenantAddr, &memberName, &memberNo, &memberEmail, &memberID)
	if err != nil {
		return nil, fmt.Errorf("load statement context: %w", err)
	}
	out := &statementContext{
		TenantName:       tenantName,
		TenantAddress:    tenantAddr,
		TenantDisclaimer: "This statement is computer-generated and does not require a signature.",
		MemberName:       memberName,
		MemberNo:         memberNo,
	}
	if memberEmail != nil {
		out.MemberEmail = *memberEmail
	}
	if memberID != nil {
		out.MemberID = *memberID
	} else {
		// Institutional counterparties don't have a `members` row — the
		// statements assemblers still work off counterparty_id for shares /
		// deposits, but interest / dividend tables key off member_id.
		out.MemberID = cpID
	}
	return out, nil
}

// ─────────── render helper ───────────

func (h *MemberStatementsPDFHandler) renderAndStream(
	w http.ResponseWriter, r *http.Request,
	key cacheKey, filename string,
	docType string, subjectMemberID *uuid.UUID, subjectLabel string,
	payload map[string]any, maxTxnAt time.Time,
) {
	// Cache check — same (member, kind, period, max_txn_at) re-clicks
	// return the cached bytes without re-rendering.
	if v, ok := h.cache.Load(key); ok {
		c := v.(cachedPDF)
		if time.Since(c.GeneratedAt) < statementCacheTTL && !c.MaxTxnAt.Before(maxTxnAt) {
			h.streamPDF(w, c.Bytes, filename)
			return
		}
	}
	if h.Notifier == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("PDF generation is not configured"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)
	resp, err := h.Notifier.GeneratePDF(r.Context(), notifier.PDFGenerateRequest{
		TenantID:        tid,
		DocumentType:    docType,
		SubjectMemberID: subjectMemberID,
		SubjectLabel:    subjectLabel,
		Payload:         payload,
		GeneratedBy:     &uid,
	})
	if err != nil {
		h.Logger.Error("statement pdf generate", "doc_type", docType, "err", err)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	bytes, ferr := h.Notifier.FetchPDFBytes(r.Context(), resp.DownloadToken)
	if ferr != nil {
		h.Logger.Error("statement pdf fetch", "doc_type", docType, "err", ferr)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	h.cache.Store(key, cachedPDF{Bytes: bytes, GeneratedAt: time.Now(), MaxTxnAt: maxTxnAt})
	h.streamPDF(w, bytes, filename)
}

func (h *MemberStatementsPDFHandler) streamPDF(w http.ResponseWriter, body []byte, filename string) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.Header().Set("Content-Disposition", `inline; filename="`+sanitizeFilename(filename)+`"`)
	_, _ = io.Copy(w, &byteReader{p: body})
}

type byteReader struct{ p []byte; i int }

func (b *byteReader) Read(p []byte) (int, error) {
	if b.i >= len(b.p) {
		return 0, io.EOF
	}
	n := copy(p, b.p[b.i:])
	b.i += n
	return n, nil
}

func sanitizeFilename(s string) string {
	if s == "" {
		return "statement.pdf"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// ─────────── Deposits ───────────

func (h *MemberStatementsPDFHandler) DepositsPDF(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	from, to, perr := parseStatementDateRange(r)
	if perr != nil {
		httpx.WriteErr(w, r, perr)
		return
	}
	var accountID *uuid.UUID
	if s := strings.TrimSpace(r.URL.Query().Get("account_id")); s != "" {
		id, perr := uuid.Parse(s)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid account_id"))
			return
		}
		accountID = &id
	}

	tid, _ := middleware.TenantIDFrom(r)
	var payload *store.DepositStatementPayload
	var sctx *statementContext
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, lerr := h.loadStatementContextTx(r.Context(), tx, cpID)
		if lerr != nil {
			return lerr
		}
		sctx = c
		period := from.Format("2006-01-02") + " — " + to.Format("2006-01-02")
		common := store.StatementCommon{
			TenantName:       c.TenantName,
			TenantAddress:    c.TenantAddress,
			TenantDisclaimer: c.TenantDisclaimer,
			MemberName:       c.MemberName,
			MemberNo:         c.MemberNo,
			PeriodLabel:      period,
			GeneratedDate:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		}
		p, perr := h.Statements.BuildDepositStatementTx(r.Context(), tx, common, cpID, accountID, from, to)
		if perr != nil {
			return perr
		}
		payload = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	period := from.Format("20060102") + "-" + to.Format("20060102")
	variant := ""
	if accountID != nil {
		variant = accountID.String()
	}
	h.renderAndStream(
		w, r,
		cacheKey{MemberID: cpID, Kind: "deposit_statement", Period: period, Variant: variant},
		fmt.Sprintf("deposit-statement-%s-%s.pdf", sctx.MemberNo, period),
		"deposit_statement", &cpID, sctx.MemberName,
		payload.ToPayload(), payload.MaxTxnAt,
	)
}

// ─────────── Shares ───────────

func (h *MemberStatementsPDFHandler) SharesPDF(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	fy := strings.TrimSpace(r.URL.Query().Get("fy"))
	from, to, perr := parseFYRange(fy)
	if perr != nil {
		httpx.WriteErr(w, r, perr)
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var payload *store.ShareStatementPayload
	var sctx *statementContext
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, lerr := h.loadStatementContextTx(r.Context(), tx, cpID)
		if lerr != nil {
			return lerr
		}
		sctx = c
		common := store.StatementCommon{
			TenantName:       c.TenantName,
			TenantAddress:    c.TenantAddress,
			TenantDisclaimer: c.TenantDisclaimer,
			MemberName:       c.MemberName,
			MemberNo:         c.MemberNo,
			PeriodLabel:      fy,
			GeneratedDate:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		}
		p, perr := h.Statements.BuildShareStatementTx(r.Context(), tx, common, cpID, from, to)
		if perr != nil {
			return perr
		}
		payload = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.renderAndStream(
		w, r,
		cacheKey{MemberID: cpID, Kind: "share_statement", Period: fy},
		fmt.Sprintf("share-statement-%s-%s.pdf", sctx.MemberNo, fy),
		"share_statement", &cpID, sctx.MemberName,
		payload.ToPayload(), payload.MaxTxnAt,
	)
}

// ─────────── Interest ───────────

func (h *MemberStatementsPDFHandler) InterestPDF(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	fy := strings.TrimSpace(r.URL.Query().Get("fy"))
	if fy == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var payload *store.InterestStatementPayload
	var sctx *statementContext
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, lerr := h.loadStatementContextTx(r.Context(), tx, cpID)
		if lerr != nil {
			return lerr
		}
		sctx = c
		common := store.StatementCommon{
			TenantName:       c.TenantName,
			TenantAddress:    c.TenantAddress,
			TenantDisclaimer: c.TenantDisclaimer,
			MemberName:       c.MemberName,
			MemberNo:         c.MemberNo,
			PeriodLabel:      fy,
			GeneratedDate:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		}
		// interest_run_lines uses counterparty_id (migrated from member_id).
		p, perr := h.Statements.BuildInterestStatementTx(r.Context(), tx, common, cpID, fy)
		if perr != nil {
			return perr
		}
		payload = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.renderAndStream(
		w, r,
		cacheKey{MemberID: cpID, Kind: "interest_statement", Period: fy},
		fmt.Sprintf("interest-statement-%s-%s.pdf", sctx.MemberNo, fy),
		"interest_statement", &cpID, sctx.MemberName,
		payload.ToPayload(), payload.MaxTxnAt,
	)
}

// ─────────── Dividend ───────────

func (h *MemberStatementsPDFHandler) DividendPDF(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	fy := strings.TrimSpace(r.URL.Query().Get("fy"))
	if fy == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("fy is required"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var payload *store.DividendStatementPayload
	var sctx *statementContext
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, lerr := h.loadStatementContextTx(r.Context(), tx, cpID)
		if lerr != nil {
			return lerr
		}
		sctx = c
		common := store.StatementCommon{
			TenantName:       c.TenantName,
			TenantAddress:    c.TenantAddress,
			TenantDisclaimer: c.TenantDisclaimer,
			MemberName:       c.MemberName,
			MemberNo:         c.MemberNo,
			PeriodLabel:      fy,
			GeneratedDate:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		}
		p, perr := h.Statements.BuildDividendStatementTx(r.Context(), tx, common, cpID, fy)
		if perr != nil {
			return perr
		}
		payload = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.renderAndStream(
		w, r,
		cacheKey{MemberID: cpID, Kind: "dividend_statement", Period: fy},
		fmt.Sprintf("dividend-statement-%s-%s.pdf", sctx.MemberNo, fy),
		"dividend_statement", &cpID, sctx.MemberName,
		payload.ToPayload(), payload.MaxTxnAt,
	)
}

// ─────────── Email ───────────

type emailStatementReq struct {
	Kind      string  `json:"kind"`      // 'deposits'|'shares'|'interest'|'dividend'
	Period    string  `json:"period"`    // FY label or 'YYYY-MM-DD..YYYY-MM-DD'
	AccountID *string `json:"account_id,omitempty"`
}

func (h *MemberStatementsPDFHandler) Email(w http.ResponseWriter, r *http.Request) {
	cpID, err := uuid.Parse(chi.URLParam(r, "counterparty_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid counterparty_id"))
		return
	}
	var in emailStatementReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	docType := docTypeForKind(in.Kind)
	if docType == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind must be deposits|shares|interest|dividend"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	uid, _ := middleware.UserIDFrom(r)

	// Build the payload server-side so the notification service's
	// PDF-generate call has everything it needs without re-querying
	// savings (no service-to-service round-trip from the notification
	// renderer side).
	var (
		payload map[string]any
		sctx    *statementContext
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		c, lerr := h.loadStatementContextTx(r.Context(), tx, cpID)
		if lerr != nil {
			return lerr
		}
		sctx = c
		p, perr := h.buildPayloadForEmailTx(r.Context(), tx, c, cpID, in)
		if perr != nil {
			return perr
		}
		payload = p
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if sctx.MemberEmail == "" {
		httpx.WriteErr(w, r, httpx.ErrConflict("member has no registered email"))
		return
	}

	periodLabel := in.Period
	filename := fmt.Sprintf("%s-%s-%s.pdf", strings.TrimSuffix(docType, "_statement"), sctx.MemberNo, periodLabel)
	subject := fmt.Sprintf("%s · %s for %s", sctx.TenantName, humanStatementSubject(in.Kind), periodLabel)
	emailBody := fmt.Sprintf("Dear %s,\n\nPlease find attached your %s for %s.\n\nKind regards,\n%s",
		sctx.MemberName, humanStatementSubject(in.Kind), periodLabel, sctx.TenantName)
	emailAddr := sctx.MemberEmail

	if h.Notifier != nil {
		h.Notifier.Notify(r.Context(), notifier.Request{
			TenantID:          tid,
			EventCode:         "MEMBER_STATEMENT_EMAIL",
			Channels:          []notifier.Channel{notifier.ChannelEmail},
			RecipientMemberID: &cpID,
			RecipientName:     sctx.MemberName,
			RecipientEmail:    &emailAddr,
			Payload: map[string]any{
				"subject": subject,
				"body":    emailBody,
			},
			PDFAttachments: []notifier.PDFAttachmentSpec{{
				DocumentType:    docType,
				Filename:        sanitizeFilename(filename),
				SubjectMemberID: &cpID,
				SubjectLabel:    sctx.MemberName,
				Payload:         payload,
			}},
			InitiatedBy: &uid,
		})
	}
	httpx.OK(w, map[string]any{
		"status":          "queued",
		"email":           sctx.MemberEmail,
		"document_type":   docType,
		"period":          periodLabel,
	})
}

// buildPayloadForEmailTx — dispatch table from kind string → assembler.
func (h *MemberStatementsPDFHandler) buildPayloadForEmailTx(
	ctx context.Context, tx pgx.Tx, c *statementContext, cpID uuid.UUID, in emailStatementReq,
) (map[string]any, error) {
	common := store.StatementCommon{
		TenantName:       c.TenantName,
		TenantAddress:    c.TenantAddress,
		TenantDisclaimer: c.TenantDisclaimer,
		MemberName:       c.MemberName,
		MemberNo:         c.MemberNo,
		PeriodLabel:      in.Period,
		GeneratedDate:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	}
	switch in.Kind {
	case "deposits":
		from, to, err := splitRangePeriod(in.Period)
		if err != nil {
			return nil, err
		}
		var accountID *uuid.UUID
		if in.AccountID != nil && *in.AccountID != "" {
			id, perr := uuid.Parse(*in.AccountID)
			if perr != nil {
				return nil, httpx.ErrBadRequest("invalid account_id")
			}
			accountID = &id
		}
		p, perr := h.Statements.BuildDepositStatementTx(ctx, tx, common, cpID, accountID, from, to)
		if perr != nil {
			return nil, perr
		}
		return p.ToPayload(), nil
	case "shares":
		from, to, err := parseFYRange(in.Period)
		if err != nil {
			return nil, err
		}
		p, perr := h.Statements.BuildShareStatementTx(ctx, tx, common, cpID, from, to)
		if perr != nil {
			return nil, perr
		}
		return p.ToPayload(), nil
	case "interest":
		p, perr := h.Statements.BuildInterestStatementTx(ctx, tx, common, cpID, in.Period)
		if perr != nil {
			return nil, perr
		}
		return p.ToPayload(), nil
	case "dividend":
		p, perr := h.Statements.BuildDividendStatementTx(ctx, tx, common, cpID, in.Period)
		if perr != nil {
			return nil, perr
		}
		return p.ToPayload(), nil
	}
	return nil, httpx.ErrBadRequest("invalid kind")
}

// ─────────── helpers: period parsers ───────────

func parseStatementDateRange(r *http.Request) (time.Time, time.Time, error) {
	fromStr := strings.TrimSpace(r.URL.Query().Get("from"))
	toStr := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromStr == "" || toStr == "" {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("from + to (YYYY-MM-DD) are required")
	}
	from, ferr := time.Parse("2006-01-02", fromStr)
	if ferr != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("from must be YYYY-MM-DD")
	}
	to, terr := time.Parse("2006-01-02", toStr)
	if terr != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("to must be YYYY-MM-DD")
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("to must be after from")
	}
	return from, to.AddDate(0, 0, 1), nil // exclusive upper bound
}

// parseFYRange — accepts 'FY2025-2026', 'FY-2025-2026', '2025-2026'
// and returns [start, end) for July-to-June FY (Kenyan convention).
func parseFYRange(fy string) (time.Time, time.Time, error) {
	fy = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(fy), "FY"), "-")
	parts := strings.Split(fy, "-")
	if len(parts) != 2 || len(parts[0]) != 4 || len(parts[1]) != 4 {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("fy must be 'YYYY-YYYY' (e.g. 2025-2026)")
	}
	var y1, y2 int
	if _, err := fmt.Sscanf(parts[0], "%4d", &y1); err != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("fy: first year invalid")
	}
	if _, err := fmt.Sscanf(parts[1], "%4d", &y2); err != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("fy: second year invalid")
	}
	if y2 != y1+1 {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("fy second year must be first year + 1")
	}
	start := time.Date(y1, time.July, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(y2, time.July, 1, 0, 0, 0, 0, time.UTC)
	return start, end, nil
}

// splitRangePeriod — parses 'YYYY-MM-DD..YYYY-MM-DD' for the email path.
func splitRangePeriod(period string) (time.Time, time.Time, error) {
	parts := strings.Split(period, "..")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("period must be 'YYYY-MM-DD..YYYY-MM-DD'")
	}
	from, err := time.Parse("2006-01-02", parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("period from invalid")
	}
	to, err := time.Parse("2006-01-02", parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, httpx.ErrBadRequest("period to invalid")
	}
	return from, to.AddDate(0, 0, 1), nil
}

func docTypeForKind(k string) string {
	switch k {
	case "deposits":
		return "deposit_statement"
	case "shares":
		return "share_statement"
	case "interest":
		return "interest_statement"
	case "dividend":
		return "dividend_statement"
	}
	return ""
}

func humanStatementSubject(k string) string {
	switch k {
	case "deposits":
		return "Deposit statement"
	case "shares":
		return "Share statement"
	case "interest":
		return "Interest statement"
	case "dividend":
		return "Dividend statement"
	}
	return "Statement"
}

// silence import lint when json isn't used elsewhere in this file.
var _ = json.Marshal
