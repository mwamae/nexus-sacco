// Centralised loan_documents access — one seam for every write so the
// supersede + expiry logic stays consistent.
//
// Callers previously did `INSERT INTO loan_documents` inline from at
// least two handlers (guarantor consent proof + collections letter PDF).
// Migration 0050 plus this store route both through Insert below.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/savings/internal/domain"
)

type LoanDocumentStore struct {
	pool *pgxpool.Pool
}

func NewLoanDocumentStore(pool *pgxpool.Pool) *LoanDocumentStore {
	return &LoanDocumentStore{pool: pool}
}

// Errors callers may check.
var (
	ErrDocumentNotFound      = errors.New("loan document not found")
	ErrDocumentDeleteBlocked = errors.New("loan document cannot be deleted while it satisfies a required-kind slot")
)

// ─────────── helpers ───────────

const loanDocCols = `
	id, tenant_id, application_id, loan_id, kind, description,
	storage_path, mime, size_bytes, uploaded_at, uploaded_by,
	expires_at, review_status, reviewed_by, reviewed_at, review_notes,
	is_current, superseded_by_id
`

func scanLoanDocument(row pgx.Row) (*domain.LoanDocument, error) {
	var d domain.LoanDocument
	err := row.Scan(
		&d.ID, &d.TenantID, &d.ApplicationID, &d.LoanID, &d.Kind, &d.Description,
		&d.StoragePath, &d.Mime, &d.SizeBytes, &d.UploadedAt, &d.UploadedBy,
		&d.ExpiresAt, &d.ReviewStatus, &d.ReviewedBy, &d.ReviewedAt, &d.ReviewNotes,
		&d.IsCurrent, &d.SupersededByID,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ─────────── Insert + supersede ───────────

// InsertInput captures every field the caller can set up-front. Supersede
// runs automatically: when a current row exists for the same target
// (application_id OR loan_id) and the same kind, it's flipped to
// is_current=false + superseded_by_id=new.id atomically with the insert.
type InsertInput struct {
	ApplicationID *uuid.UUID                // exactly one of ApplicationID / LoanID
	LoanID        *uuid.UUID
	Kind          domain.LoanDocKind
	Description   *string
	StoragePath   string
	Mime          string
	SizeBytes     int64
	UploadedBy    uuid.UUID
	ExpiresAt     *time.Time
}

// InsertTx — the single write seam. Returns the new row.
func (s *LoanDocumentStore) InsertTx(ctx context.Context, tx pgx.Tx, in InsertInput) (*domain.LoanDocument, error) {
	if (in.ApplicationID == nil) == (in.LoanID == nil) {
		return nil, errors.New("InsertTx requires exactly one of ApplicationID, LoanID")
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO loan_documents (
		  tenant_id, application_id, loan_id, kind, description,
		  storage_path, mime, size_bytes, uploaded_by, expires_at
		) VALUES (
		  current_tenant_id(), $1, $2, $3::loan_doc_kind, $4,
		  $5, $6, $7, $8, $9
		)
		RETURNING `+loanDocCols,
		in.ApplicationID, in.LoanID, string(in.Kind), in.Description,
		in.StoragePath, in.Mime, in.SizeBytes, in.UploadedBy, in.ExpiresAt,
	)
	doc, err := scanLoanDocument(row)
	if err != nil {
		return nil, fmt.Errorf("insert loan document: %w", err)
	}
	// Supersede any prior current row for this (target, kind).
	if in.ApplicationID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE loan_documents SET
			  is_current      = false,
			  superseded_by_id = $3
			 WHERE application_id = $1 AND kind = $2::loan_doc_kind AND id <> $3 AND is_current = true
		`, *in.ApplicationID, string(in.Kind), doc.ID); err != nil {
			return nil, fmt.Errorf("supersede prior application doc: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE loan_documents SET
			  is_current      = false,
			  superseded_by_id = $3
			 WHERE loan_id = $1 AND kind = $2::loan_doc_kind AND id <> $3 AND is_current = true
		`, *in.LoanID, string(in.Kind), doc.ID); err != nil {
			return nil, fmt.Errorf("supersede prior loan doc: %w", err)
		}
	}
	return doc, nil
}

// ─────────── Read ───────────

func (s *LoanDocumentStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.LoanDocument, error) {
	row := tx.QueryRow(ctx, `SELECT `+loanDocCols+` FROM loan_documents WHERE id = $1`, id)
	d, err := scanLoanDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDocumentNotFound
	}
	return d, err
}

// ListByApplicationTx returns rows ordered by kind then uploaded_at DESC.
// When includeHistory is false, only is_current=true rows are returned.
func (s *LoanDocumentStore) ListByApplicationTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID, includeHistory bool) ([]domain.LoanDocument, error) {
	return s.listWhereTx(ctx, tx, `application_id = $1`, appID, includeHistory)
}

func (s *LoanDocumentStore) ListByLoanTx(ctx context.Context, tx pgx.Tx, loanID uuid.UUID, includeHistory bool) ([]domain.LoanDocument, error) {
	return s.listWhereTx(ctx, tx, `loan_id = $1`, loanID, includeHistory)
}

func (s *LoanDocumentStore) listWhereTx(ctx context.Context, tx pgx.Tx, where string, arg uuid.UUID, includeHistory bool) ([]domain.LoanDocument, error) {
	q := `SELECT ` + loanDocCols + ` FROM loan_documents WHERE ` + where
	if !includeHistory {
		q += ` AND is_current = true`
	}
	q += ` ORDER BY kind, uploaded_at DESC`
	rows, err := tx.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LoanDocument
	for rows.Next() {
		d, err := scanLoanDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// HasCurrentNonExpiredTx — the workhorse for the approval-gate check.
// Returns true when there's at least one is_current row for the
// (target, kind) pair with expires_at NULL or > today.
func (s *LoanDocumentStore) HasCurrentNonExpiredTx(ctx context.Context, tx pgx.Tx, target uuid.UUID, kind string, targetIsLoan bool) (bool, error) {
	col := "application_id"
	if targetIsLoan {
		col = "loan_id"
	}
	var exists bool
	err := tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT EXISTS (
		  SELECT 1 FROM loan_documents
		   WHERE %s = $1 AND kind = $2::loan_doc_kind
		     AND is_current = true
		     AND (expires_at IS NULL OR expires_at > CURRENT_DATE)
		)
	`, col), target, kind).Scan(&exists)
	return exists, err
}

// RequiredDocsStatusTx returns the structured payload the UI checklist
// + the approval-gate 409 body use. statusByKind[kind] is:
//
//   { "satisfied": bool, "current_doc_id": uuid|nil, "expires_at": date|nil,
//     "warning": "expires_in_Nd" | "" , "reason": "no_document" | "expired" | "" }
type RequiredDocStatus struct {
	Satisfied    bool       `json:"satisfied"`
	CurrentDocID *uuid.UUID `json:"current_doc_id,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Warning      string     `json:"warning,omitempty"`
	Reason       string     `json:"reason,omitempty"`
}

type RequiredDocsStatus struct {
	Required     []string                     `json:"required"`
	Status       map[string]RequiredDocStatus `json:"status"`
	AllSatisfied bool                         `json:"all_satisfied"`
	Summary      string                       `json:"summary"`
}

// RequiredDocsStatusTx — combines a product's required_document_kinds
// with the current document state for the application. warningDays
// drives the "expires_in_Nd" UI banner; pass 0 to skip the warning.
func (s *LoanDocumentStore) RequiredDocsStatusTx(
	ctx context.Context, tx pgx.Tx,
	appID uuid.UUID, requiredKinds []string, warningDays int,
) (*RequiredDocsStatus, error) {
	// Initialise as empty slice (not nil) so JSON encodes as [] rather
	// than null — the frontend reads .length immediately so a null
	// would crash the React tab.
	required := []string{}
	required = append(required, requiredKinds...)
	out := &RequiredDocsStatus{
		Required: required,
		Status:   map[string]RequiredDocStatus{},
	}
	if len(requiredKinds) == 0 {
		out.AllSatisfied = true
		out.Summary = "No required documents for this product."
		return out, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT kind::text, id, expires_at
		  FROM loan_documents
		 WHERE application_id = $1 AND is_current = true AND kind::text = ANY($2)
	`, appID, requiredKinds)
	if err != nil {
		return nil, err
	}
	byKind := make(map[string]struct {
		ID        uuid.UUID
		ExpiresAt *time.Time
	})
	for rows.Next() {
		var kind string
		var id uuid.UUID
		var exp *time.Time
		if err := rows.Scan(&kind, &id, &exp); err != nil {
			rows.Close()
			return nil, err
		}
		byKind[kind] = struct {
			ID        uuid.UUID
			ExpiresAt *time.Time
		}{id, exp}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	satisfied := 0
	for _, k := range requiredKinds {
		row, ok := byKind[k]
		if !ok {
			out.Status[k] = RequiredDocStatus{Satisfied: false, Reason: "no_document"}
			continue
		}
		st := RequiredDocStatus{
			Satisfied:    true,
			CurrentDocID: &row.ID,
			ExpiresAt:    row.ExpiresAt,
		}
		if row.ExpiresAt != nil {
			if !row.ExpiresAt.After(today) {
				st.Satisfied = false
				st.Reason = "expired"
			} else if warningDays > 0 {
				daysLeft := int(row.ExpiresAt.Sub(today).Hours() / 24)
				if daysLeft <= warningDays {
					st.Warning = fmt.Sprintf("expires_in_%dd", daysLeft)
				}
			}
		}
		if st.Satisfied {
			satisfied++
		}
		out.Status[k] = st
	}
	out.AllSatisfied = satisfied == len(requiredKinds)
	out.Summary = fmt.Sprintf("%d of %d required documents satisfied", satisfied, len(requiredKinds))
	return out, nil
}

// ─────────── Review state ───────────

type ReviewInput struct {
	ID         uuid.UUID
	Status     string // 'reviewed' | 'needs_replacement' | 'flagged'
	ReviewedBy uuid.UUID
	Notes      *string
}

func (s *LoanDocumentStore) ReviewTx(ctx context.Context, tx pgx.Tx, in ReviewInput) (*domain.LoanDocument, error) {
	switch in.Status {
	case "reviewed", "needs_replacement", "flagged":
	default:
		return nil, errors.New("invalid review status")
	}
	row := tx.QueryRow(ctx, `
		UPDATE loan_documents SET
		  review_status = $2,
		  reviewed_by   = $3,
		  reviewed_at   = now(),
		  review_notes  = $4
		 WHERE id = $1
		 RETURNING `+loanDocCols,
		in.ID, in.Status, in.ReviewedBy, in.Notes,
	)
	d, err := scanLoanDocument(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDocumentNotFound
	}
	return d, err
}

// DeleteTx — removes a row only when it's NOT the current row satisfying
// a required-kind slot on its target. The caller passes the required
// kinds it already knows.
func (s *LoanDocumentStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, requiredKinds []string) error {
	doc, err := s.GetTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if doc.IsCurrent {
		for _, rk := range requiredKinds {
			if rk == string(doc.Kind) {
				return ErrDocumentDeleteBlocked
			}
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM loan_documents WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDocumentNotFound
	}
	return nil
}

// ─────────── Loan backfill (Phase 6c) ───────────

// AttachToLoanTx — when an application becomes a loan, point every doc
// row at the loan_id. Mirrors the existing inline UPDATE that
// loan_guarantee_store.go ran at line 226.
func (s *LoanDocumentStore) AttachToLoanTx(ctx context.Context, tx pgx.Tx, appID, loanID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE loan_documents SET loan_id = $2 WHERE application_id = $1
	`, appID, loanID)
	return err
}

// ─────────── Tenant config helpers ───────────

// LoadTenantDocConfigTx returns the per-tenant defaults used to compute
// expires_at on upload + the warning window for the UI.
type TenantDocConfig struct {
	DefaultRequiredKinds []string
	ExpiryWindowsDays    map[string]int // kind → days
	WarningDays          int
}

func (s *LoanDocumentStore) LoadTenantDocConfigTx(ctx context.Context, tx pgx.Tx) (*TenantDocConfig, error) {
	var cfg TenantDocConfig
	var windowsJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(default_required_document_kinds, ARRAY[]::text[]),
		  COALESCE(document_expiry_windows, '{}'::jsonb),
		  COALESCE(document_expiry_warning_days, 14)
		 FROM tenant_operations
		 LIMIT 1
	`).Scan(&cfg.DefaultRequiredKinds, &windowsJSON, &cfg.WarningDays)
	if err != nil {
		return nil, err
	}
	cfg.ExpiryWindowsDays = map[string]int{}
	if len(windowsJSON) > 0 {
		raw := map[string]json.Number{}
		if jerr := json.Unmarshal(windowsJSON, &raw); jerr == nil {
			for k, v := range raw {
				if n, perr := v.Int64(); perr == nil {
					cfg.ExpiryWindowsDays[k] = int(n)
				}
			}
		}
	}
	return &cfg, nil
}
