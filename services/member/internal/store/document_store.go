// member_documents records the metadata of uploaded files. The bytes
// themselves live in the storage backend (LocalDisk for now).

package store

import (
	"context"
	"errors"
	"time"

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
	CounterpartyID uuid.UUID
	TenantID       uuid.UUID
	Kind           domain.DocumentKind
	StoragePath    string
	MIME           string
	SizeBytes      int64
	IssueDate      *time.Time
	ExpiryDate     *time.Time
	UploadedBy     *uuid.UUID
}

// UpsertTx writes a document and returns the resulting row. The 'other'
// kind is allowed to repeat per counterparty (see the partial unique
// index from migration 0020) so it always INSERTs a new row; all other
// kinds upsert in place, resetting verification back to pending because
// a new file replaced the previous one.
func (s *DocumentStore) UpsertTx(ctx context.Context, tx pgx.Tx, in CreateDocumentInput) (*domain.Document, error) {
	if in.Kind == domain.DocOther {
		return s.insertTx(ctx, tx, in)
	}
	return s.upsertSingularTx(ctx, tx, in)
}

const docReturning = `RETURNING id, counterparty_id, kind, storage_path, mime, size_bytes,
		issue_date, expiry_date, verification, verified_by, verified_at,
		COALESCE(verification_note,''), uploaded_at, uploaded_by`

func scanDoc(row pgx.Row, d *domain.Document) error {
	return row.Scan(&d.ID, &d.CounterpartyID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes,
		&d.IssueDate, &d.ExpiryDate, &d.Verification, &d.VerifiedBy, &d.VerifiedAt,
		&d.VerificationNote, &d.UploadedAt, &d.UploadedBy)
}

func (s *DocumentStore) upsertSingularTx(ctx context.Context, tx pgx.Tx, in CreateDocumentInput) (*domain.Document, error) {
	var d domain.Document
	err := scanDoc(tx.QueryRow(ctx, `
		INSERT INTO member_documents (counterparty_id, tenant_id, kind, storage_path, mime, size_bytes,
		                              issue_date, expiry_date, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (counterparty_id, kind) WHERE kind <> 'other'::document_kind DO UPDATE
		  SET storage_path = EXCLUDED.storage_path,
		      mime         = EXCLUDED.mime,
		      size_bytes   = EXCLUDED.size_bytes,
		      issue_date   = EXCLUDED.issue_date,
		      expiry_date  = EXCLUDED.expiry_date,
		      uploaded_at  = now(),
		      uploaded_by  = EXCLUDED.uploaded_by,
		      verification = 'pending',
		      verified_by  = NULL,
		      verified_at  = NULL,
		      verification_note = NULL
		`+docReturning,
		in.CounterpartyID, in.TenantID, in.Kind, in.StoragePath, in.MIME, in.SizeBytes,
		in.IssueDate, in.ExpiryDate, in.UploadedBy), &d)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DocumentStore) insertTx(ctx context.Context, tx pgx.Tx, in CreateDocumentInput) (*domain.Document, error) {
	var d domain.Document
	err := scanDoc(tx.QueryRow(ctx, `
		INSERT INTO member_documents (counterparty_id, tenant_id, kind, storage_path, mime, size_bytes,
		                              issue_date, expiry_date, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`+docReturning,
		in.CounterpartyID, in.TenantID, in.Kind, in.StoragePath, in.MIME, in.SizeBytes,
		in.IssueDate, in.ExpiryDate, in.UploadedBy), &d)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DocumentStore) SetVerificationTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.DocVerification, by uuid.UUID, note string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE member_documents
		SET verification = $2, verified_by = $3, verified_at = now(), verification_note = NULLIF($4,'')
		WHERE id = $1
	`, id, status, by, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTx removes the row by id, returning the storage_path of the
// removed row so the caller can delete the underlying blob after the
// DB transaction commits. Returns ErrNotFound when nothing matched.
func (s *DocumentStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (string, error) {
	var path string
	err := tx.QueryRow(ctx, `
		DELETE FROM member_documents WHERE id = $1
		RETURNING storage_path
	`, id).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return path, err
}

// ListForCounterpartyTx — parameter is a counterparty.id directly.
func (s *DocumentStore) ListForCounterpartyTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) ([]*domain.Document, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, counterparty_id, kind, storage_path, mime, size_bytes,
		       issue_date, expiry_date, verification, verified_by, verified_at,
		       COALESCE(verification_note,''), uploaded_at, uploaded_by
		FROM member_documents WHERE counterparty_id = $1
		ORDER BY kind, uploaded_at DESC
	`, cpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*domain.Document{}
	for rows.Next() {
		var d domain.Document
		if err := scanDoc(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// ByKindTx returns the singular row for a fixed-kind document. For
// the 'other' kind this returns an arbitrary row (most recent), which
// is what the verify/replace endpoints want when a path-based lookup
// is unambiguous. Callers that need a specific 'other' row must use
// ByIDTx instead.
func (s *DocumentStore) ByKindTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID, kind domain.DocumentKind) (*domain.Document, error) {
	var d domain.Document
	err := scanDoc(tx.QueryRow(ctx, `
		SELECT id, counterparty_id, kind, storage_path, mime, size_bytes,
		       issue_date, expiry_date, verification, verified_by, verified_at,
		       COALESCE(verification_note,''), uploaded_at, uploaded_by
		FROM member_documents WHERE counterparty_id = $1 AND kind = $2
		ORDER BY uploaded_at DESC
		LIMIT 1
	`, cpID, kind), &d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ByIDTx fetches a single document by id (within the current RLS
// tenant scope). Used by the delete/verify-by-id endpoints when the
// caller already holds an opaque doc id.
func (s *DocumentStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Document, error) {
	var d domain.Document
	err := scanDoc(tx.QueryRow(ctx, `
		SELECT id, counterparty_id, kind, storage_path, mime, size_bytes,
		       issue_date, expiry_date, verification, verified_by, verified_at,
		       COALESCE(verification_note,''), uploaded_at, uploaded_by
		FROM member_documents WHERE id = $1
	`, id), &d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}
