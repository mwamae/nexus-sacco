// Document storage abstraction. v1 ships a LocalDisk implementation;
// swap to S3 / GCS later by implementing the same interface.

package storage

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

type Storage interface {
	// Save reads from src and writes a file under tenant/member/kind.
	// Returns an opaque storage path that Open can later resolve.
	Save(tenantID, memberID uuid.UUID, kind, mimeType string, src io.Reader, sizeHint int64) (path string, size int64, err error)
	// Open returns a readable handle for the file at path.
	Open(path string) (io.ReadCloser, error)
	// Delete removes the file (best-effort).
	Delete(path string) error
}

type LocalDisk struct {
	Root string
}

func NewLocalDisk(root string) (*LocalDisk, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage root abs: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("storage root mkdir: %w", err)
	}
	return &LocalDisk{Root: abs}, nil
}

// extFromMIME picks a sensible file extension. Falls back to .bin.
func extFromMIME(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	switch m {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	}
	if exts, _ := mime.ExtensionsByType(m); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func (d *LocalDisk) Save(tenantID, memberID uuid.UUID, kind, mimeType string, src io.Reader, _ int64) (string, int64, error) {
	rel := filepath.Join(tenantID.String(), memberID.String(), kind+extFromMIME(mimeType))
	abs := filepath.Join(d.Root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", 0, fmt.Errorf("mkdir: %w", err)
	}
	// Overwrite any existing file for this (member, kind).
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", 0, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, src)
	if err != nil {
		return "", 0, fmt.Errorf("copy: %w", err)
	}
	return rel, n, nil
}

func (d *LocalDisk) Open(rel string) (io.ReadCloser, error) {
	abs := filepath.Join(d.Root, rel)
	// Defence-in-depth: refuse paths that escape the root.
	clean, err := filepath.Abs(abs)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(clean, d.Root+string(os.PathSeparator)) && clean != d.Root {
		return nil, errors.New("invalid storage path")
	}
	return os.Open(clean)
}

func (d *LocalDisk) Delete(rel string) error {
	abs := filepath.Join(d.Root, rel)
	clean, err := filepath.Abs(abs)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(clean, d.Root+string(os.PathSeparator)) {
		return errors.New("invalid storage path")
	}
	return os.Remove(clean)
}
