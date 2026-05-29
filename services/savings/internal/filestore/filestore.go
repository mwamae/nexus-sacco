// Package filestore — minimal local-disk storage for documents
// uploaded through the savings service (today: guarantor consent
// proofs; future: any document attached to an application or loan).
//
// Files are stored under `BaseDir`/<tenant>/<subdir>/<uuid><ext>.
// The returned path is the relative-from-base form (so it stays
// portable when the base moves, e.g. when we switch to S3 later).
//
// This is intentionally narrow: no MIME sniffing, no AV scanning, no
// thumbnailing. A 5 MB cap is enforced at the multipart-parse layer
// in the handler.

package filestore

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type Store struct {
	BaseDir string
}

// New returns a Store rooted at baseDir. Creates the dir if missing.
func New(baseDir string) (*Store, error) {
	if baseDir == "" {
		baseDir = "./uploads"
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("filestore: ensure base dir: %w", err)
	}
	return &Store{BaseDir: baseDir}, nil
}

// SaveResult is what callers persist on the originating row.
type SaveResult struct {
	StoragePath string // base-relative path, suitable for loan_documents.storage_path
	Size        int64
	MimeType    string
	Filename    string // sanitised original filename
}

// Save streams the contents to disk under <tenant>/<subdir>/<uuid><ext>.
// `subdir` distinguishes document classes ("guarantor_consent_proof"
// is the first; collateral / KYC could land in their own subdirs).
// `originalName` is the multipart filename (used only for the extension);
// the on-disk name is a UUID to prevent collisions + traversal.
func (s *Store) Save(
	tenantID uuid.UUID, subdir, originalName, contentType string,
	body io.Reader,
) (*SaveResult, error) {
	if s == nil || s.BaseDir == "" {
		return nil, errors.New("filestore: not initialised")
	}
	ext := filepath.Ext(originalName)
	if ext == "" && contentType != "" {
		// Best-effort from MIME type.
		exts, _ := mime.ExtensionsByType(contentType)
		if len(exts) > 0 {
			ext = exts[0]
		}
	}
	ext = sanitiseExt(ext)
	id := uuid.New().String()
	relPath := filepath.Join(tenantID.String(), subdir, id+ext)
	absPath := filepath.Join(s.BaseDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, fmt.Errorf("filestore: mkdir: %w", err)
	}
	f, err := os.Create(absPath)
	if err != nil {
		return nil, fmt.Errorf("filestore: create %s: %w", absPath, err)
	}
	defer f.Close()
	n, err := io.Copy(f, body)
	if err != nil {
		_ = os.Remove(absPath)
		return nil, fmt.Errorf("filestore: write: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &SaveResult{
		StoragePath: relPath,
		Size:        n,
		MimeType:    contentType,
		Filename:    sanitiseFilename(originalName),
	}, nil
}

// sanitiseExt strips anything weird from a file extension. Keeps the
// dot prefix. Empty input → empty output.
func sanitiseExt(ext string) string {
	if ext == "" {
		return ""
	}
	out := strings.ToLower(ext)
	// Allow only a..z 0..9 . in the extension.
	clean := make([]byte, 0, len(out))
	for _, c := range out {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' {
			clean = append(clean, byte(c))
		}
	}
	if len(clean) == 0 {
		return ""
	}
	if clean[0] != '.' {
		return "." + string(clean)
	}
	return string(clean)
}

// sanitiseFilename removes path separators + control chars from a
// user-provided filename. Used only for display / loan_documents
// description; the on-disk name is a UUID.
func sanitiseFilename(s string) string {
	s = filepath.Base(s)
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 32 && r != '/' && r != '\\' {
			out = append(out, r)
		}
	}
	return string(out)
}
