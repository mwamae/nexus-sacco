// Phase-1 follow-up — Documents tab on the loan application + loan
// detail pages.
//
// Mirrors the member-documents pattern (services/member/.../member.go::
// UploadDocument): multipart parse → size cap → MIME allowlist →
// filestore.Save → store.Insert (with supersede + auto expires_at). The
// new store also drives:
//
//   • the required-documents checklist endpoint that the UI renders, and
//   • the approval-gate helper consumed by the workflow callback.
//
// Bundle endpoint produces a single merged PDF for download (pdfcpu).

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/filestore"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/pdfbundle"
	"github.com/nexussacco/savings/internal/store"
)

type LoanDocumentsHandler struct {
	DB     *db.Pool
	Docs   *store.LoanDocumentStore
	Files  *filestore.Store
	Logger *slog.Logger

	// MaxUploadBytes — defaults to 10 MiB if zero.
	MaxUploadBytes int64

	// RescoreInTx — when set, an upload to an application whose product
	// has the kind in required_document_kinds triggers an in-tx rescore
	// with trigger_reason='document_added'. Wired from main.go to
	// LoanApplicationHandler.RescoreApplicationTx.
	RescoreInTx RescoreInTxHook
}

// RescoreInTxHook runs inside the caller's tx so the rescore commits
// or rolls back atomically with the upstream change.
type RescoreInTxHook func(ctx context.Context, tx pgx.Tx, appID uuid.UUID, trigger string) error

const defaultMaxDocUpload = 10 << 20 // 10 MiB

func (h *LoanDocumentsHandler) maxUpload() int64 {
	if h.MaxUploadBytes > 0 {
		return h.MaxUploadBytes
	}
	return defaultMaxDocUpload
}

// allowedDocMIMEs — pragmatic allowlist matching the member-docs handler.
var allowedDocMIMEs = map[string]struct{}{
	"application/pdf":         {},
	"image/png":               {},
	"image/jpeg":              {},
	"image/jpg":               {},
	"image/gif":               {},
	"image/webp":              {},
	"application/msword":      {},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": {},
	"application/vnd.ms-excel": {},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       {},
}

func mimeAllowed(m string) bool {
	if m == "" {
		return false
	}
	if i := strings.Index(m, ";"); i > 0 {
		m = strings.TrimSpace(m[:i])
	}
	_, ok := allowedDocMIMEs[strings.ToLower(m)]
	return ok
}

// ─────────── Upload ───────────

func (h *LoanDocumentsHandler) UploadForApplication(w http.ResponseWriter, r *http.Request) {
	h.upload(w, r, true)
}

func (h *LoanDocumentsHandler) UploadForLoan(w http.ResponseWriter, r *http.Request) {
	h.upload(w, r, false)
}

func (h *LoanDocumentsHandler) upload(w http.ResponseWriter, r *http.Request, forApp bool) {
	targetIDParam := "loan_id"
	if forApp {
		targetIDParam = "app_id"
	}
	targetID, err := uuid.Parse(chi.URLParam(r, targetIDParam))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid "+targetIDParam))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload())
	if err := r.ParseMultipartForm(h.maxUpload()); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("multipart parse failed: "+err.Error()))
		return
	}
	file, fileHeader, ferr := r.FormFile("file")
	if ferr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file is required"))
		return
	}
	defer file.Close()
	if fileHeader.Size > h.maxUpload() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(fmt.Sprintf("file too large; %d bytes max", h.maxUpload())))
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind == "" {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("kind is required"))
		return
	}
	contentType := fileHeader.Header.Get("Content-Type")
	if !mimeAllowed(contentType) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("unsupported file type: "+contentType))
		return
	}
	desc := strPtrOrNil(r.FormValue("description"))

	// Optional override; otherwise computed from tenant_operations.
	var explicitExpiry *time.Time
	if v := strings.TrimSpace(r.FormValue("expires_at")); v != "" {
		t, perr := time.Parse("2006-01-02", v)
		if perr != nil {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("expires_at must be YYYY-MM-DD"))
			return
		}
		explicitExpiry = &t
	}

	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	if h.Files == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("file uploads not configured"))
		return
	}
	saved, err := h.Files.Save(tid, "loan_documents", fileHeader.Filename, contentType, file)
	if err != nil {
		h.Logger.Error("save loan doc", "err", err)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}

	var doc *domain.LoanDocument
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		expiry := explicitExpiry
		if expiry == nil {
			cfg, cerr := h.Docs.LoadTenantDocConfigTx(r.Context(), tx)
			if cerr != nil {
				return cerr
			}
			if days, ok := cfg.ExpiryWindowsDays[kind]; ok && days > 0 {
				t := time.Now().UTC().AddDate(0, 0, days)
				expiry = &t
			}
		}
		in := store.InsertInput{
			Kind:        domain.LoanDocKind(kind),
			Description: desc,
			StoragePath: saved.StoragePath,
			Mime:        saved.MimeType,
			SizeBytes:   saved.Size,
			UploadedBy:  uid,
			ExpiresAt:   expiry,
		}
		if forApp {
			in.ApplicationID = &targetID
		} else {
			in.LoanID = &targetID
		}
		d, ierr := h.Docs.InsertTx(r.Context(), tx, in)
		if ierr != nil {
			return ierr
		}
		doc = d

		// Phase-1 follow-up — auto-rescore when the uploaded kind is in
		// product.required_document_kinds. Runs in the same tx so the
		// score stays in sync with the document set.
		if forApp && h.RescoreInTx != nil {
			var required []string
			if err := tx.QueryRow(r.Context(), `
				SELECT COALESCE(p.required_document_kinds, ARRAY[]::text[])
				  FROM loan_applications a
				  JOIN loan_products p ON p.id = a.product_id
				 WHERE a.id = $1
			`, targetID).Scan(&required); err != nil {
				return err
			}
			for _, k := range required {
				if k == kind {
					if rerr := h.RescoreInTx(r.Context(), tx, targetID, "document_added"); rerr != nil {
						return rerr
					}
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		// Best-effort cleanup of orphaned file if the DB write failed.
		_ = os.Remove(filepath.Join(h.Files.BaseDir, saved.StoragePath))
		httpx.WriteErr(w, r, err)
		return
	}

	httpx.OK(w, doc)
}

// ─────────── List ───────────

func (h *LoanDocumentsHandler) ListForApplication(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, true)
}

func (h *LoanDocumentsHandler) ListForLoan(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, false)
}

func (h *LoanDocumentsHandler) list(w http.ResponseWriter, r *http.Request, forApp bool) {
	idParam := "loan_id"
	if forApp {
		idParam = "app_id"
	}
	targetID, err := uuid.Parse(chi.URLParam(r, idParam))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	includeHistory := r.URL.Query().Get("include_history") == "true"
	tid, _ := middleware.TenantIDFrom(r)
	var items []domain.LoanDocument
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var err error
		if forApp {
			items, err = h.Docs.ListByApplicationTx(r.Context(), tx, targetID, includeHistory)
		} else {
			items, err = h.Docs.ListByLoanTx(r.Context(), tx, targetID, includeHistory)
		}
		return err
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": len(items), "include_history": includeHistory})
}

// ─────────── Download single ───────────

func (h *LoanDocumentsHandler) Download(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var doc *domain.LoanDocument
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		d, gerr := h.Docs.GetTx(r.Context(), tx, id)
		if gerr != nil {
			return gerr
		}
		doc = d
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrDocumentNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("document not found"))
		} else {
			httpx.WriteErr(w, r, err)
		}
		return
	}
	abs := filepath.Join(h.Files.BaseDir, doc.StoragePath)
	f, ferr := os.Open(abs)
	if ferr != nil {
		h.Logger.Error("open doc", "id", id, "err", ferr)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", doc.Mime)
	if doc.SizeBytes > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", doc.SizeBytes))
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(doc.StoragePath)+`"`)
	_, _ = io.Copy(w, f)
}

// ─────────── Review ───────────

type reviewDocReq struct {
	Status string  `json:"status"`
	Notes  *string `json:"notes,omitempty"`
}

func (h *LoanDocumentsHandler) Review(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	var in reviewDocReq
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	switch in.Status {
	case "reviewed":
	case "needs_replacement", "flagged":
		if in.Notes == nil || strings.TrimSpace(*in.Notes) == "" {
			httpx.WriteErr(w, r, httpx.ErrBadRequest("notes required for "+in.Status))
			return
		}
	default:
		httpx.WriteErr(w, r, httpx.ErrBadRequest("status must be reviewed | needs_replacement | flagged"))
		return
	}
	uid, _ := middleware.UserIDFrom(r)
	tid, _ := middleware.TenantIDFrom(r)
	var doc *domain.LoanDocument
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		d, rerr := h.Docs.ReviewTx(r.Context(), tx, store.ReviewInput{
			ID: id, Status: in.Status, ReviewedBy: uid, Notes: in.Notes,
		})
		if rerr != nil {
			return rerr
		}
		doc = d
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, doc)
}

// ─────────── Delete ───────────

func (h *LoanDocumentsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		// Look up the product's required kinds so the store can refuse
		// to delete a row satisfying a required slot.
		doc, gerr := h.Docs.GetTx(r.Context(), tx, id)
		if gerr != nil {
			return gerr
		}
		var requiredKinds []string
		if doc.ApplicationID != nil {
			if err := tx.QueryRow(r.Context(), `
				SELECT COALESCE(p.required_document_kinds, ARRAY[]::text[])
				  FROM loan_applications a
				  JOIN loan_products p ON p.id = a.product_id
				 WHERE a.id = $1
			`, *doc.ApplicationID).Scan(&requiredKinds); err != nil {
				return err
			}
		}
		return h.Docs.DeleteTx(r.Context(), tx, id, requiredKinds)
	})
	if err != nil {
		if errors.Is(err, store.ErrDocumentDeleteBlocked) {
			httpx.WriteErr(w, r, httpx.ErrConflict("cannot delete: this document satisfies a required-kind slot. Upload a replacement first."))
			return
		}
		if errors.Is(err, store.ErrDocumentNotFound) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("document not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────── Required-documents status ───────────

func (h *LoanDocumentsHandler) RequiredStatus(w http.ResponseWriter, r *http.Request) {
	appID, err := uuid.Parse(chi.URLParam(r, "app_id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid app_id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var out *store.RequiredDocsStatus
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var required []string
		if err := tx.QueryRow(r.Context(), `
			SELECT COALESCE(p.required_document_kinds, ARRAY[]::text[])
			  FROM loan_applications a
			  JOIN loan_products p ON p.id = a.product_id
			 WHERE a.id = $1
		`, appID).Scan(&required); err != nil {
			return err
		}
		cfg, cerr := h.Docs.LoadTenantDocConfigTx(r.Context(), tx)
		if cerr != nil {
			return cerr
		}
		s, serr := h.Docs.RequiredDocsStatusTx(r.Context(), tx, appID, required, cfg.WarningDays)
		if serr != nil {
			return serr
		}
		out = s
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// ─────────── Bundle ───────────

func (h *LoanDocumentsHandler) BundleApplication(w http.ResponseWriter, r *http.Request) {
	h.bundle(w, r, true)
}

func (h *LoanDocumentsHandler) BundleLoan(w http.ResponseWriter, r *http.Request) {
	h.bundle(w, r, false)
}

func (h *LoanDocumentsHandler) bundle(w http.ResponseWriter, r *http.Request, forApp bool) {
	idParam := "loan_id"
	if forApp {
		idParam = "app_id"
	}
	targetID, err := uuid.Parse(chi.URLParam(r, idParam))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid id"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	// Load all is_current docs + the surrounding loan / app reference
	// (used as the cover page header).
	var (
		docs    []domain.LoanDocument
		loanRef string
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var ferr error
		if forApp {
			docs, ferr = h.Docs.ListByApplicationTx(r.Context(), tx, targetID, false)
			if ferr != nil {
				return ferr
			}
			_ = tx.QueryRow(r.Context(), `SELECT application_no FROM loan_applications WHERE id = $1`, targetID).Scan(&loanRef)
		} else {
			docs, ferr = h.Docs.ListByLoanTx(r.Context(), tx, targetID, false)
			if ferr != nil {
				return ferr
			}
			_ = tx.QueryRow(r.Context(), `SELECT loan_no FROM loans WHERE id = $1`, targetID).Scan(&loanRef)
		}
		return nil
	})
	if err != nil {
		httpx.WriteErr(w, r, err)
		return
	}
	if len(docs) == 0 {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no documents to bundle"))
		return
	}

	sources := make([]pdfbundle.DocSource, 0, len(docs))
	for _, d := range docs {
		desc := ""
		if d.Description != nil {
			desc = *d.Description
		}
		sources = append(sources, pdfbundle.DocSource{
			Kind:         string(d.Kind),
			Description:  desc,
			UploadedAt:   d.UploadedAt,
			ReviewStatus: d.ReviewStatus,
			Filename:     filepath.Base(d.StoragePath),
			MimeType:     d.Mime,
			AbsolutePath: filepath.Join(h.Files.BaseDir, d.StoragePath),
		})
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="Loan-%s-Documents.pdf"`, sanitizeFilenameComponent(loanRef)))
	if err := pdfbundle.Build(w, loanRef, sources); err != nil {
		h.Logger.Error("bundle build", "err", err)
		// Headers may already be sent; surface a short error in the body.
		_, _ = w.Write([]byte("\n\nbundle build failed: " + err.Error()))
	}
}

// ─────────── helpers ───────────

func sanitizeFilenameComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "loan"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

func strPtrOrNil(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
