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
	CounterpartyID    uuid.UUID
	TenantID    uuid.UUID
	Kind        domain.DocumentKind
	StoragePath string
	MIME        string
	SizeBytes   int64
	UploadedBy  *uuid.UUID
}

// UpsertTx writes/replaces the document of (counterparty, kind).
// Phase E A: in.CounterpartyID is a counterparty.id directly; the
// URL contract for /counterparties/{id}/documents already carries it.
// Callers that still hold a real members.id (member-onboarding) must
// resolve at the call site via ResolveCounterpartyID.
func (s *DocumentStore) UpsertTx(ctx context.Context, tx pgx.Tx, in CreateDocumentInput) (*domain.Document, error) {
	var d domain.Document
	err := tx.QueryRow(ctx, `
		INSERT INTO member_documents (counterparty_id, tenant_id, kind, storage_path, mime, size_bytes, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (counterparty_id, kind) DO UPDATE
		  SET storage_path = EXCLUDED.storage_path,
		      mime = EXCLUDED.mime,
		      size_bytes = EXCLUDED.size_bytes,
		      uploaded_at = now(),
		      uploaded_by = EXCLUDED.uploaded_by
		RETURNING id, counterparty_id, kind, storage_path, mime, size_bytes, uploaded_at
	`, in.CounterpartyID, in.TenantID, in.Kind, in.StoragePath, in.MIME, in.SizeBytes, in.UploadedBy).
		Scan(&d.ID, &d.CounterpartyID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ListForCounterpartyTx — Phase E A: parameter is a counterparty.id
// directly. Renamed from ListForMemberTx to make the input semantics
// match the URL contract.
func (s *DocumentStore) ListForCounterpartyTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) ([]*domain.Document, error) {
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

// ByKindTx — Phase E A: cpID parameter is a counterparty.id directly.
func (s *DocumentStore) ByKindTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID, kind domain.DocumentKind) (*domain.Document, error) {
	var d domain.Document
	err := tx.QueryRow(ctx, `
		SELECT id, counterparty_id, kind, storage_path, mime, size_bytes, uploaded_at
		FROM member_documents WHERE counterparty_id = $1 AND kind = $2
	`, cpID, kind).Scan(&d.ID, &d.CounterpartyID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes, &d.UploadedAt)
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
