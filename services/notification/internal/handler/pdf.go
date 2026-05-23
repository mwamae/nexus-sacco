// PDF endpoints:
//   POST /internal/v1/pdf/generate                   (service-to-service)
//   GET  /v1/pdf-documents                           list (filtered)
//   GET  /v1/pdf-documents/{id}                      detail
//   GET  /v1/pdf-documents/{id}/download             authenticated download
//   GET  /d/{token}.pdf                              public time-limited link

package handler

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/notification/internal/db"
	"github.com/nexussacco/notification/internal/domain"
	"github.com/nexussacco/notification/internal/httpx"
	"github.com/nexussacco/notification/internal/middleware"
	"github.com/nexussacco/notification/internal/pdf"
	"github.com/nexussacco/notification/internal/store"
)

type PDFHandler struct {
	DB            *db.Pool
	PDFs          *store.PDFStore
	Generator     *pdf.Generator
	Storage       *pdf.Storage
	InternalToken string
	Logger        *slog.Logger
}

// ─────────── Internal generate ───────────

type generateReq struct {
	TenantID         uuid.UUID      `json:"tenant_id"`
	DocumentType     string         `json:"document_type"`
	SubjectMemberID  *uuid.UUID     `json:"subject_member_id,omitempty"`
	SubjectLoanID    *uuid.UUID     `json:"subject_loan_id,omitempty"`
	SubjectAccountID *uuid.UUID     `json:"subject_account_id,omitempty"`
	SubjectLabel     string         `json:"subject_label,omitempty"`
	Payload          map[string]any `json:"payload,omitempty"`
	GeneratedBy      *uuid.UUID     `json:"generated_by,omitempty"`
}

func (h *PDFHandler) GenerateInternal(w http.ResponseWriter, r *http.Request) {
	if h.InternalToken != "" {
		if r.Header.Get("X-Internal-Token") != h.InternalToken {
			httpx.WriteErr(w, r, httpx.ErrUnauthorized("invalid internal token"))
			return
		}
	}
	var in generateReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if in.TenantID == uuid.Nil || in.DocumentType == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("tenant_id and document_type are required"))
		return
	}
	doc, _, err := h.Generator.Generate(r.Context(), pdf.GenerateInput{
		TenantID:         in.TenantID,
		DocumentType:     in.DocumentType,
		SubjectMemberID:  in.SubjectMemberID,
		SubjectLoanID:    in.SubjectLoanID,
		SubjectAccountID: in.SubjectAccountID,
		SubjectLabel:     in.SubjectLabel,
		Payload:          in.Payload,
		GeneratedBy:      in.GeneratedBy,
	})
	if err != nil {
		httpx.WriteErr(w, r, fmt.Errorf("generate: %w", err))
		return
	}
	httpx.Created(w, doc)
}

// ─────────── Admin: list + detail + authenticated download ───────────

func (h *PDFHandler) List(w http.ResponseWriter, r *http.Request) {
	tid, _ := middleware.TenantIDFrom(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := store.PDFListFilter{
		DocType: q.Get("document_type"),
		Limit:   limit,
		Offset:  offset,
	}
	if v := q.Get("counterparty_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.CounterpartyID = &id
		}
	}
	if v := q.Get("loan_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.LoanID = &id
		}
	}
	if v := q.Get("account_id"); v != "" {
		id, err := uuid.Parse(v)
		if err == nil {
			f.AccountID = &id
		}
	}
	var items []domain.PDFDocument
	err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		items, err = h.PDFs.ListTx(r.Context(), tx, f)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items})
}

func (h *PDFHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var d *domain.PDFDocument
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		d, err = h.PDFs.GetDocumentTx(r.Context(), tx, id)
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, d)
}

// Download — authenticated via the standard JWT middleware. Streams
// the file from disk; does not expose the storage path.
func (h *PDFHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var d *domain.PDFDocument
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		d, err = h.PDFs.GetDocumentTx(r.Context(), tx, id)
		if err != nil {
			return err
		}
		return h.PDFs.RecordDownloadTx(r.Context(), tx, id)
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	h.streamPDF(w, r, d)
}

// PublicDownload — token-based, no JWT. Token lookup spans tenants
// (RLS would normally block a no-tenant SELECT, but pdf_documents.
// download_token is unique across the table so we can match without
// knowing the tenant ahead of time). The match's tenant_id then
// scopes any further reads/writes.
func (h *PDFHandler) PublicDownload(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	// Strip optional .pdf suffix so a browser request for "foo.pdf"
	// just works.
	if l := len(token); l > 4 && token[l-4:] == ".pdf" {
		token = token[:l-4]
	}
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}
	// Token is high-entropy; the unique index guarantees at most one
	// row across the platform. Iterate active tenants and search.
	var tenantIDs []uuid.UUID
	if err := h.DB.WithTenantTx(r.Context(), uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id FROM tenants WHERE status = 'active'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			tenantIDs = append(tenantIDs, id)
		}
		return rows.Err()
	}); err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	var doc *domain.PDFDocument
	for _, tid := range tenantIDs {
		_ = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
			d, err := h.PDFs.GetDocumentByTokenAnyTenantTx(r.Context(), tx, token)
			if err == nil && d != nil {
				doc = d
			}
			return nil
		})
		if doc != nil {
			break
		}
	}
	if doc == nil {
		http.Error(w, "not found or expired", http.StatusNotFound)
		return
	}
	if doc.TokenExpiresAt == nil || doc.TokenExpiresAt.Before(time.Now()) {
		http.Error(w, "link expired", http.StatusGone)
		return
	}
	// Record the download under the doc's tenant.
	_ = h.DB.WithTenantTx(r.Context(), doc.TenantID, func(tx pgx.Tx) error {
		return h.PDFs.RecordDownloadTx(r.Context(), tx, doc.ID)
	})
	h.streamPDF(w, r, doc)
}

func (h *PDFHandler) streamPDF(w http.ResponseWriter, _ *http.Request, doc *domain.PDFDocument) {
	f, err := h.Storage.Open(doc.StoragePath)
	if err != nil {
		h.Logger.Error("pdf: open storage failed", "path", doc.StoragePath, "err", err)
		http.Error(w, "file unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	w.Header().Set("Content-Type", "application/pdf")
	if stat != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	}
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filenameFor(doc)+`"`)
	if _, err := io.Copy(w, f); err != nil {
		h.Logger.Warn("pdf: stream failed", "err", err)
	}
}

func filenameFor(d *domain.PDFDocument) string {
	if d.SubjectLabel != "" {
		return safeFilename(d.DocumentType+" - "+d.SubjectLabel) + ".pdf"
	}
	return safeFilename(d.DocumentType+" - "+d.ID.String()) + ".pdf"
}

func safeFilename(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == ' ', r == '.':
			out = append(out, byte(r))
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

