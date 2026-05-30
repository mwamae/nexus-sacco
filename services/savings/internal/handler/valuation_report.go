// Phase-1 follow-up — upload + download for valuation report PDFs that
// live on collateral_valuations.valuation_report_path.
//
//   POST /v1/collateral/{id}/valuation-report   — multipart, returns {storage_path}
//   GET  /v1/collateral-valuations/{id}/report  — streams the file
//
// Valuation reports are a special class of file: they're attached to a
// single collateral valuation row, not to the application / loan as a
// whole, so they live outside the loan_documents table. The upload
// endpoint just writes bytes to filestore + returns the path; the
// caller then includes the path on the valuation-create body.

package handler

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/filestore"
	"github.com/nexussacco/savings/internal/httpx"
	"github.com/nexussacco/savings/internal/middleware"
)

type ValuationReportHandler struct {
	DB     *db.Pool
	Files  *filestore.Store
	Logger *slog.Logger

	// MaxUploadBytes — defaults to 10 MiB if zero.
	MaxUploadBytes int64
}

func (h *ValuationReportHandler) maxUpload() int64 {
	if h.MaxUploadBytes > 0 {
		return h.MaxUploadBytes
	}
	return 10 << 20
}

// Pragmatic MIME allowlist — reports are almost always PDFs; we also
// accept images so a phone-snap of a printed report can be attached.
var valuationReportMIMEs = map[string]struct{}{
	"application/pdf": {},
	"image/png":       {},
	"image/jpeg":      {},
	"image/jpg":       {},
}

// ─────────── Upload ───────────

func (h *ValuationReportHandler) Upload(w http.ResponseWriter, r *http.Request) {
	collateralID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid collateral id"))
		return
	}
	if h.Files == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("file uploads not configured"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload())
	if err := r.ParseMultipartForm(h.maxUpload()); err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("multipart parse failed: "+err.Error()))
		return
	}
	file, header, ferr := r.FormFile("file")
	if ferr != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("file is required"))
		return
	}
	defer file.Close()
	if header.Size > h.maxUpload() {
		httpx.WriteErr(w, r, httpx.ErrBadRequest(fmt.Sprintf("file too large; %d bytes max", h.maxUpload())))
		return
	}
	ct := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	if i := strings.Index(ct, ";"); i > 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if _, ok := valuationReportMIMEs[ct]; !ok {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("unsupported content type: "+ct+" (PDF or image required)"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)

	// Confirm the collateral row exists + belongs to the tenant before
	// we burn filestore space. The savings filestore is per-tenant
	// scoped so RLS doesn't apply here; the lookup is the gate.
	if err := h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `
			SELECT EXISTS(SELECT 1 FROM loan_collateral WHERE id = $1)
		`, collateralID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.ErrNotFound("collateral not found")
		}
		return nil
	}); err != nil {
		httpx.WriteErr(w, r, err)
		return
	}

	subdir := filepath.Join("valuation_reports", collateralID.String())
	saved, err := h.Files.Save(tid, subdir, header.Filename, ct, file)
	if err != nil {
		h.Logger.Error("save valuation report", "err", err)
		httpx.WriteErr(w, r, httpx.ErrInternal())
		return
	}
	httpx.OK(w, map[string]any{
		"storage_path": saved.StoragePath,
		"mime":         saved.MimeType,
		"size":         saved.Size,
		"filename":     header.Filename,
	})
}

// ─────────── Other collateral file downloads ───────────
//
// The drawer's Docs sub-tab also surfaces the ownership document + the
// verification photos taken during the verify step. Both are stored as
// plain paths on loan_collateral, and the corresponding files live in
// filestore under the tenant subdir. These two endpoints stream the
// bytes after re-fetching the row to authorise the access (RLS scopes
// the SELECT to the caller's tenant).

func (h *ValuationReportHandler) OwnershipDocDownload(w http.ResponseWriter, r *http.Request) {
	collateralID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid collateral id"))
		return
	}
	if h.Files == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("file uploads not configured"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var path *string
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT ownership_path FROM loan_collateral WHERE id = $1
		`, collateralID).Scan(&path)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("collateral not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	if path == nil || *path == "" {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no ownership document on this collateral"))
		return
	}
	streamFile(w, r, h.Files.BaseDir, *path, h.Logger)
}

func (h *ValuationReportHandler) VerificationPhotoDownload(w http.ResponseWriter, r *http.Request) {
	collateralID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid collateral id"))
		return
	}
	idxStr := chi.URLParam(r, "idx")
	if h.Files == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("file uploads not configured"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var photosJSON []byte
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT verification_photos::text::bytea
			  FROM loan_collateral WHERE id = $1
		`, collateralID).Scan(&photosJSON)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("collateral not found"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	// Parse the JSONB array. Empty → 404.
	var paths []string
	if len(photosJSON) > 0 {
		_ = jsonUnmarshalLoose(photosJSON, &paths)
	}
	if len(paths) == 0 {
		httpx.WriteErr(w, r, httpx.ErrNotFound("no verification photos on this collateral"))
		return
	}
	var idx int
	if _, perr := fmt.Sscanf(idxStr, "%d", &idx); perr != nil || idx < 0 || idx >= len(paths) {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid photo index"))
		return
	}
	streamFile(w, r, h.Files.BaseDir, paths[idx], h.Logger)
}

// streamFile — shared helper. Sniffs content type from extension.
func streamFile(w http.ResponseWriter, r *http.Request, baseDir, relPath string, logger *slog.Logger) {
	abs := filepath.Join(baseDir, relPath)
	f, ferr := os.Open(abs)
	if ferr != nil {
		logger.Error("open collateral file", "path", relPath, "err", ferr)
		httpx.WriteErr(w, r, httpx.ErrNotFound("file missing on disk"))
		return
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(relPath))
	ct := "application/octet-stream"
	switch ext {
	case ".pdf":
		ct = "application/pdf"
	case ".png":
		ct = "image/png"
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	case ".gif":
		ct = "image/gif"
	case ".webp":
		ct = "image/webp"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `inline; filename="`+filepath.Base(relPath)+`"`)
	_, _ = io.Copy(w, f)
}

// jsonUnmarshalLoose — local helper that won't pull in encoding/json
// into this file's import list when callers want to keep the file
// minimal. We accept the verification_photos JSONB shape (array of
// strings).
func jsonUnmarshalLoose(b []byte, dst *[]string) error {
	// Strip ::text::bytea round-trip artefacts: postgres returns the
	// JSON serialised as text, then casts to bytea. The body is just
	// `["path1","path2"]` text. Parse it directly.
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	// Minimal hand-parser tuned for ["a","b","c"] — avoids importing
	// encoding/json (already imported in other files, but the type
	// here is straightforward enough).
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return fmt.Errorf("not a JSON array")
	}
	body := s[1 : len(s)-1]
	if body = strings.TrimSpace(body); body == "" {
		return nil
	}
	for _, part := range strings.Split(body, ",") {
		part = strings.TrimSpace(part)
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			*dst = append(*dst, part[1:len(part)-1])
		}
	}
	return nil
}

// ─────────── Download ───────────

func (h *ValuationReportHandler) Download(w http.ResponseWriter, r *http.Request) {
	valuationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteErr(w, r, httpx.ErrBadRequest("invalid valuation id"))
		return
	}
	if h.Files == nil {
		httpx.WriteErr(w, r, httpx.ErrConflict("file uploads not configured"))
		return
	}
	tid, _ := middleware.TenantIDFrom(r)
	var (
		path string
		mime *string
	)
	err = h.DB.WithTenantTx(r.Context(), tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT valuation_report_path, NULL::text
			  FROM collateral_valuations
			 WHERE id = $1 AND valuation_report_path IS NOT NULL
		`, valuationID).Scan(&path, &mime)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteErr(w, r, httpx.ErrNotFound("no report on this valuation"))
			return
		}
		httpx.WriteErr(w, r, err)
		return
	}
	abs := filepath.Join(h.Files.BaseDir, path)
	f, ferr := os.Open(abs)
	if ferr != nil {
		h.Logger.Error("open valuation report", "valuation_id", valuationID, "path", path, "err", ferr)
		httpx.WriteErr(w, r, httpx.ErrNotFound("report file missing on disk"))
		return
	}
	defer f.Close()
	// Sniff content type from filename — the row doesn't store mime.
	ext := strings.ToLower(filepath.Ext(path))
	ct := "application/octet-stream"
	switch ext {
	case ".pdf":
		ct = "application/pdf"
	case ".png":
		ct = "image/png"
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `inline; filename="`+filepath.Base(path)+`"`)
	_, _ = io.Copy(w, f)
}
