// member_documents records the metadata of uploaded files. The bytes
// themselves live in the storage backend (LocalDisk for now).

package store

import (
	"context"
	"fmt"

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
	CounterpartyID    uuid.UUID
	TenantID    uuid.UUID
	Kind        domain.DocumentKind
	StoragePath string
	MIME        string
	SizeBytes   int64
	UploadedBy  *uuid.UUID
}

// UpsertTx writes/replaces the document of (member, kind). Returns the row.
func (s *DocumentStore) UpsertTx(ctx context.Context, tx pgx.Tx, in CreateDocumentInput) (*domain.Document, error) {
	cpID, err := ResolveCounterpartyID(ctx, tx, in.CounterpartyID)
	if err != nil {
		return nil, fmt.Errorf("resolve counterparty for document upsert: %w", err)
	}
	var d domain.Document
	err = tx.QueryRow(ctx, `
		INSERT INTO member_documents (counterparty_id, tenant_id, kind, storage_path, mime, size_bytes, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (counterparty_id, kind) DO UPDATE
		  SET storage_path = EXCLUDED.storage_path,
		      mime = EXCLUDED.mime,
		      size_bytes = EXCLUDED.size_bytes,
		      uploaded_at = now(),
		      uploaded_by = EXCLUDED.uploaded_by
		RETURNING id, counterparty_id, kind, storage_path, mime, size_bytes, uploaded_at
	`, cpID, in.TenantID, in.Kind, in.StoragePath, in.MIME, in.SizeBytes, in.UploadedBy).
		Scan(&d.ID, &d.CounterpartyID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DocumentStore) ListForMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) ([]*domain.Document, error) {
	cpID, err := ResolveCounterpartyID(ctx, tx, memberID)
	if err != nil {
		return nil, fmt.Errorf("resolve counterparty for document list: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id, counterparty_id, kind, storage_path, mime, size_bytes, uploaded_at
		FROM member_documents WHERE counterparty_id = $1
		ORDER BY kind
	`, cpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.Document{}
	for rows.Next() {
		var d domain.Document
		if err := rows.Scan(&d.ID, &d.CounterpartyID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *DocumentStore) ByKindTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID, kind domain.DocumentKind) (*domain.Document, error) {
	var d domain.Document
	err := tx.QueryRow(ctx, `
		SELECT id, counterparty_id, kind, storage_path, mime, size_bytes, uploaded_at
		FROM member_documents WHERE counterparty_id = $1 AND kind = $2
	`, memberID, kind).Scan(&d.ID, &d.CounterpartyID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
