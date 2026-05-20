// member_documents records the metadata of uploaded files. The bytes
// themselves live in the storage backend (LocalDisk for now).

package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/domain"
)

type DocumentStore struct {
	pool *pgxpool.Pool
}

func NewDocumentStore(pool *pgxpool.Pool) *DocumentStore {
	return &DocumentStore{pool: pool}
}

type CreateDocumentInput struct {
	MemberID    uuid.UUID
	TenantID    uuid.UUID
	Kind        domain.DocumentKind
	StoragePath string
	MIME        string
	SizeBytes   int64
	UploadedBy  *uuid.UUID
}

// UpsertTx writes/replaces the document of (member, kind). Returns the row.
func (s *DocumentStore) UpsertTx(ctx context.Context, tx pgx.Tx, in CreateDocumentInput) (*domain.Document, error) {
	var d domain.Document
	err := tx.QueryRow(ctx, `
		INSERT INTO member_documents (member_id, tenant_id, kind, storage_path, mime, size_bytes, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (member_id, kind) DO UPDATE
		  SET storage_path = EXCLUDED.storage_path,
		      mime = EXCLUDED.mime,
		      size_bytes = EXCLUDED.size_bytes,
		      uploaded_at = now(),
		      uploaded_by = EXCLUDED.uploaded_by
		RETURNING id, member_id, kind, storage_path, mime, size_bytes, uploaded_at
	`, in.MemberID, in.TenantID, in.Kind, in.StoragePath, in.MIME, in.SizeBytes, in.UploadedBy).
		Scan(&d.ID, &d.MemberID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DocumentStore) ListForMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) ([]*domain.Document, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, member_id, kind, storage_path, mime, size_bytes, uploaded_at
		FROM member_documents WHERE member_id = $1
		ORDER BY kind
	`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Document
	for rows.Next() {
		var d domain.Document
		if err := rows.Scan(&d.ID, &d.MemberID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *DocumentStore) ByKindTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID, kind domain.DocumentKind) (*domain.Document, error) {
	var d domain.Document
	err := tx.QueryRow(ctx, `
		SELECT id, member_id, kind, storage_path, mime, size_bytes, uploaded_at
		FROM member_documents WHERE member_id = $1 AND kind = $2
	`, memberID, kind).Scan(&d.ID, &d.MemberID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
