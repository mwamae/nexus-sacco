// pdf_templates + pdf_documents persistence.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/notification/internal/domain"
)

type PDFStore struct {
	pool *pgxpool.Pool
}

func NewPDFStore(pool *pgxpool.Pool) *PDFStore {
	return &PDFStore{pool: pool}
}

// ─────────── Templates ───────────

const pdfTemplateCols = `
	id, tenant_id, document_type, version_no, label,
	html_body, page_size, is_active, created_at
`

func scanPDFTemplate(row pgx.Row) (*domain.PDFTemplate, error) {
	var t domain.PDFTemplate
	err := row.Scan(
		&t.ID, &t.TenantID, &t.DocumentType, &t.VersionNo, &t.Label,
		&t.HTMLBody, &t.PageSize, &t.IsActive, &t.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ActiveTemplateTx returns the highest active version for the document
// type in the current tenant context. Returns (nil, nil) if none.
func (s *PDFStore) ActiveTemplateTx(ctx context.Context, tx pgx.Tx, docType string) (*domain.PDFTemplate, error) {
	row := tx.QueryRow(ctx, `
		SELECT `+pdfTemplateCols+` FROM pdf_templates
		WHERE document_type = $1 AND is_active = true
		ORDER BY version_no DESC LIMIT 1
	`, docType)
	t, err := scanPDFTemplate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// ─────────── Documents ───────────

const pdfDocumentCols = `
	id, tenant_id, document_type, template_id, template_version,
	subject_member_id, subject_loan_id, subject_account_id, subject_label,
	payload, storage_path, file_size_bytes,
	download_token, token_expires_at,
	download_count, last_downloaded_at, generated_at, generated_by
`

func scanPDFDocument(row pgx.Row) (*domain.PDFDocument, error) {
	var d domain.PDFDocument
	var payload []byte
	err := row.Scan(
		&d.ID, &d.TenantID, &d.DocumentType, &d.TemplateID, &d.TemplateVersion,
		&d.SubjectMemberID, &d.SubjectLoanID, &d.SubjectAccountID, &d.SubjectLabel,
		&payload, &d.StoragePath, &d.FileSizeBytes,
		&d.DownloadToken, &d.TokenExpiresAt,
		&d.DownloadCount, &d.LastDownloadedAt, &d.GeneratedAt, &d.GeneratedBy,
	)
	if err != nil {
		return nil, err
	}
	d.Payload = payload
	return &d, nil
}

type CreatePDFInput struct {
	DocumentType     string
	TemplateID       *uuid.UUID
	TemplateVersion  *int
	SubjectMemberID  *uuid.UUID
	SubjectLoanID    *uuid.UUID
	SubjectAccountID *uuid.UUID
	SubjectLabel     string
	Payload          map[string]any
	StoragePath      string
	FileSizeBytes    int
	DownloadToken    string
	TokenExpiresAt   time.Time
	GeneratedBy      *uuid.UUID
}

func (s *PDFStore) CreateDocumentTx(ctx context.Context, tx pgx.Tx, in CreatePDFInput) (*domain.PDFDocument, error) {
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 || string(payload) == "null" {
		payload = []byte("{}")
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO pdf_documents (
			tenant_id, document_type, template_id, template_version,
			subject_member_id, subject_loan_id, subject_account_id, subject_label,
			payload, storage_path, file_size_bytes,
			download_token, token_expires_at, generated_by
		) VALUES (
			current_tenant_id(), $1, $2, $3,
			$4, $5, $6, $7,
			$8::jsonb, $9, $10,
			$11, $12, $13
		)
		RETURNING `+pdfDocumentCols,
		in.DocumentType, in.TemplateID, in.TemplateVersion,
		in.SubjectMemberID, in.SubjectLoanID, in.SubjectAccountID, in.SubjectLabel,
		payload, in.StoragePath, in.FileSizeBytes,
		nullIfEmpty(in.DownloadToken), in.TokenExpiresAt, in.GeneratedBy,
	)
	return scanPDFDocument(row)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *PDFStore) GetDocumentTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.PDFDocument, error) {
	row := tx.QueryRow(ctx, `SELECT `+pdfDocumentCols+` FROM pdf_documents WHERE id = $1`, id)
	d, err := scanPDFDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// GetDocumentByTokenTx — used by the public /d/<token>.pdf endpoint.
// Lookup happens across tenants (no current_tenant_id() set), so the
// caller is expected to apply RLS-bypass via a separate connection if
// needed. For Stage 5 we use a regular tx in the no-tenant context —
// but for that to work we need a tenant-agnostic lookup. To keep RLS
// honest, the token-lookup endpoint can iterate tenants. For now,
// callers should NOT use this without setting tenant context first.
func (s *PDFStore) GetDocumentByTokenAnyTenantTx(ctx context.Context, tx pgx.Tx, token string) (*domain.PDFDocument, error) {
	row := tx.QueryRow(ctx,
		`SELECT `+pdfDocumentCols+` FROM pdf_documents WHERE download_token = $1`,
		token,
	)
	d, err := scanPDFDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// ListBySubjectTx — used by member profile / loan detail panels.
type PDFListFilter struct {
	MemberID  *uuid.UUID
	LoanID    *uuid.UUID
	AccountID *uuid.UUID
	DocType   string
	Limit     int
	Offset    int
}

func (s *PDFStore) ListTx(ctx context.Context, tx pgx.Tx, f PDFListFilter) ([]domain.PDFDocument, error) {
	where := "WHERE 1=1"
	args := []any{}
	idx := 1
	if f.MemberID != nil {
		where += " AND subject_member_id = $" + strconv.Itoa(idx)
		args = append(args, *f.MemberID)
		idx++
	}
	if f.LoanID != nil {
		where += " AND subject_loan_id = $" + strconv.Itoa(idx)
		args = append(args, *f.LoanID)
		idx++
	}
	if f.AccountID != nil {
		where += " AND subject_account_id = $" + strconv.Itoa(idx)
		args = append(args, *f.AccountID)
		idx++
	}
	if f.DocType != "" {
		where += " AND document_type = $" + strconv.Itoa(idx)
		args = append(args, f.DocType)
		idx++
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := tx.Query(ctx, `
		SELECT `+pdfDocumentCols+` FROM pdf_documents `+where+`
		ORDER BY generated_at DESC LIMIT $`+strconv.Itoa(idx)+` OFFSET $`+strconv.Itoa(idx+1),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PDFDocument{}
	for rows.Next() {
		d, err := scanPDFDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *PDFStore) RecordDownloadTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE pdf_documents
		SET download_count = download_count + 1, last_downloaded_at = now()
		WHERE id = $1
	`, id)
	return err
}
