// Member store — tenant-scoped writes/reads inside a transaction so RLS
// is enforced. All methods assume an *outer* WithTenantTx has bound the
// tenant for the connection.

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/domain"
)

type MemberStore struct {
	pool *pgxpool.Pool
}

func NewMemberStore(pool *pgxpool.Pool) *MemberStore {
	return &MemberStore{pool: pool}
}

type CreateMemberInput struct {
	TenantID         uuid.UUID
	FullName         string
	IDDocKind        domain.IDDocKind
	IDDocNumber      string
	KraPIN           string
	Gender           domain.Gender
	DateOfBirth      *time.Time
	Phone            string
	Email            string
	County           string
	SubCounty        string
	PhysicalAddress  string
	EmploymentStatus string
	Employer         string
	PayrollNo        string
	JobTitle         string
	CreatedBy        *uuid.UUID
}

// NextMemberNo atomically bumps the per-tenant counter and renders an
// "M-YYYY-NNNNN" identifier. Runs inside the caller's txn (so it rolls
// back if the parent insert fails).
func (s *MemberStore) NextMemberNoTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (string, error) {
	year := time.Now().Year()
	var next int
	err := tx.QueryRow(ctx, `
		INSERT INTO member_number_seq (tenant_id, year, last_value)
		VALUES ($1, $2, 1)
		ON CONFLICT (tenant_id, year) DO UPDATE SET last_value = member_number_seq.last_value + 1
		RETURNING last_value
	`, tenantID, year).Scan(&next)
	if err != nil {
		return "", fmt.Errorf("bump member_number_seq: %w", err)
	}
	return fmt.Sprintf("M-%d-%05d", year, next), nil
}

func (s *MemberStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateMemberInput, memberNo string) (*domain.Member, error) {
	var m domain.Member
	err := tx.QueryRow(ctx, `
		INSERT INTO members (
		  tenant_id, member_no, status,
		  full_name, id_doc_kind, id_doc_number, kra_pin, gender, date_of_birth,
		  phone, email, county, sub_county, physical_address,
		  employment_status, employer, payroll_no, job_title,
		  created_by
		) VALUES (
		  $1, $2, 'pending',
		  $3, $4, $5, NULLIF($6,''), $7, $8,
		  NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), NULLIF($12,''), NULLIF($13,''),
		  NULLIF($14,''), NULLIF($15,''), NULLIF($16,''), NULLIF($17,''),
		  $18
		)
		RETURNING id, tenant_id, member_no, status,
		          full_name, id_doc_kind, id_doc_number, COALESCE(kra_pin,''), gender, date_of_birth,
		          COALESCE(phone,''), COALESCE(email,''),
		          COALESCE(county,''), COALESCE(sub_county,''), COALESCE(physical_address,''),
		          COALESCE(employment_status,''), COALESCE(employer,''),
		          COALESCE(payroll_no,''), COALESCE(job_title,''),
		          approved_at, approved_by, COALESCE(rejection_reason,''),
		          created_at, updated_at, created_by
	`,
		in.TenantID, memberNo,
		in.FullName, in.IDDocKind, in.IDDocNumber, in.KraPIN, in.Gender, in.DateOfBirth,
		in.Phone, in.Email, in.County, in.SubCounty, in.PhysicalAddress,
		in.EmploymentStatus, in.Employer, in.PayrollNo, in.JobTitle,
		in.CreatedBy,
	).Scan(
		&m.ID, &m.TenantID, &m.MemberNo, &m.Status,
		&m.FullName, &m.IDDocKind, &m.IDDocNumber, &m.KraPIN, &m.Gender, &m.DateOfBirth,
		&m.Phone, &m.Email,
		&m.County, &m.SubCounty, &m.PhysicalAddress,
		&m.EmploymentStatus, &m.Employer,
		&m.PayrollNo, &m.JobTitle,
		&m.ApprovedAt, &m.ApprovedBy, &m.RejectionReason,
		&m.CreatedAt, &m.UpdatedAt, &m.CreatedBy,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *MemberStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Member, error) {
	return scanMember(tx.QueryRow(ctx, memberSelect+` WHERE id = $1`, id))
}

type ListInput struct {
	Status domain.MemberStatus // "" for any
	Query  string              // optional ILIKE on full_name + member_no
	Limit  int
	Offset int
}

type ListResult struct {
	Members []*domain.Member
	Total   int
}

func (s *MemberStore) ListTx(ctx context.Context, tx pgx.Tx, in ListInput) (*ListResult, error) {
	args := []any{}
	where := []string{}
	if in.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, in.Status)
	}
	if in.Query != "" {
		where = append(where, fmt.Sprintf("(full_name ILIKE $%d OR member_no ILIKE $%d)", len(args)+1, len(args)+1))
		args = append(args, "%"+in.Query+"%")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + join(where, " AND ")
	}

	var total int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM members"+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}

	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	args2 := append(args, in.Limit, in.Offset)
	rows, err := tx.Query(ctx, memberSelect+whereSQL+
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), args2...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &ListResult{Total: total}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out.Members = append(out.Members, m)
	}
	return out, rows.Err()
}

func (s *MemberStore) ApproveTx(ctx context.Context, tx pgx.Tx, id, approver uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE members
		SET status = 'active', approved_at = now(), approved_by = $2, rejection_reason = NULL
		WHERE id = $1 AND status = 'pending'
	`, id, approver)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MemberStore) RejectTx(ctx context.Context, tx pgx.Tx, id, approver uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE members
		SET status = 'rejected', approved_by = $2, rejection_reason = $3
		WHERE id = $1 AND status = 'pending'
	`, id, approver, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MemberStore) SetStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.MemberStatus) error {
	tag, err := tx.Exec(ctx, `UPDATE members SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─────────── Helpers ───────────

const memberSelect = `
SELECT id, tenant_id, member_no, status,
       full_name, id_doc_kind, id_doc_number, COALESCE(kra_pin,''), gender, date_of_birth,
       COALESCE(phone,''), COALESCE(email,''),
       COALESCE(county,''), COALESCE(sub_county,''), COALESCE(physical_address,''),
       COALESCE(employment_status,''), COALESCE(employer,''),
       COALESCE(payroll_no,''), COALESCE(job_title,''),
       approved_at, approved_by, COALESCE(rejection_reason,''),
       created_at, updated_at, created_by
FROM members`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMember(row rowScanner) (*domain.Member, error) {
	var m domain.Member
	err := row.Scan(
		&m.ID, &m.TenantID, &m.MemberNo, &m.Status,
		&m.FullName, &m.IDDocKind, &m.IDDocNumber, &m.KraPIN, &m.Gender, &m.DateOfBirth,
		&m.Phone, &m.Email,
		&m.County, &m.SubCounty, &m.PhysicalAddress,
		&m.EmploymentStatus, &m.Employer,
		&m.PayrollNo, &m.JobTitle,
		&m.ApprovedAt, &m.ApprovedBy, &m.RejectionReason,
		&m.CreatedAt, &m.UpdatedAt, &m.CreatedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
