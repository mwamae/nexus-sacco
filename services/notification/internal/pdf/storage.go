// Filesystem storage for generated PDFs.
//
// Path layout: <root>/<tenant_id>/<yyyy>/<mm>/<uuid>.pdf
// The file is never exposed in a URL; the download endpoint reads
// from disk and streams to the authenticated caller.

package pdf

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

type Storage struct {
	Root string
}

func NewStorage(root string) (*Storage, error) {
	if root == "" {
		return nil, fmt.Errorf("pdf storage: root path is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	return &Storage{Root: root}, nil
}

// Write persists bytes for the given tenant and returns the path
// relative to the storage root (what we store in pdf_documents.storage_path).
func (s *Storage) Write(tenantID uuid.UUID, bytes []byte) (relPath string, err error) {
	now := time.Now().UTC()
	id := uuid.New()
	rel := filepath.Join(
		tenantID.String(),
		fmt.Sprintf("%04d", now.Year()),
		fmt.Sprintf("%02d", now.Month()),
		id.String()+".pdf",
	)
	abs := filepath.Join(s.Root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(abs, bytes, 0o640); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return rel, nil
}

// Read returns the absolute path on disk for a stored doc.
func (s *Storage) AbsPath(relPath string) string {
	return filepath.Join(s.Root, relPath)
}

// Open opens a stored doc for reading.
func (s *Storage) Open(relPath string) (*os.File, error) {
	return os.Open(s.AbsPath(relPath))
}
