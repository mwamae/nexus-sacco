// Organisation onboarding stores — five small stores grouped in one
// file so callers can wire them in main.go without ceremony. Every
// method runs inside an outer WithTenantTx so RLS is enforced.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/domain"
)

// ─────────── Org members ───────────

type OrgMemberStore struct {
	pool *pgxpool.Pool
}

func NewOrgMemberStore(pool *pgxpool.Pool) *OrgMemberStore {
	return &OrgMemberStore{pool: pool}
}

type CreateOrgInput struct {
	TenantID           uuid.UUID
	RegisteredName     string
	TradingName        string
	Kind               domain.OrgKind
	RegistrationNo     string
	DateOfRegistration *time.Time
	DateOfOperation    *time.Time
	Industry           string
	NatureOfBusiness   string
	MemberCount        *int
	EmployeeCount      *int

	PhysicalAddress string
	PostalAddress   string
	County          string
	SubCounty       string
	Ward            string
	GPSLat          *float64
	GPSLng          *float64
	BranchID        *uuid.UUID

	RiskCategory domain.RiskCategory
	CreatedBy    *uuid.UUID
}

// NextOrgNoTx atomically bumps the per-tenant year-keyed counter and
// renders ORG-YYYY-NNNNN.
func (s *OrgMemberStore) NextOrgNoTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (string, error) {
	year := time.Now().Year()
	var n int
	err := tx.QueryRow(ctx, `
		INSERT INTO org_number_seq (tenant_id, year, last_value)
		VALUES ($1, $2, 1)
		ON CONFLICT (tenant_id, year) DO UPDATE SET last_value = org_number_seq.last_value + 1
		RETURNING last_value
	`, tenantID, year).Scan(&n)
	if err != nil {
		return "", fmt.Errorf("bump org_number_seq: %w", err)
	}
	return fmt.Sprintf("ORG-%d-%05d", year, n), nil
}

func (s *OrgMemberStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateOrgInput, orgNo string) (*domain.Org, error) {
	risk := in.RiskCategory
	if risk == "" {
		risk = domain.RiskMedium
	}
	var o domain.Org
	err := tx.QueryRow(ctx, `
		INSERT INTO org_members (
		  tenant_id, org_no, status,
		  registered_name, trading_name, kind, registration_no,
		  date_of_registration, date_of_operation, industry, nature_of_business,
		  member_count, employee_count,
		  physical_address, postal_address, county, sub_county, ward,
		  gps_lat, gps_lng, branch_id,
		  risk_category, created_by
		) VALUES (
		  $1, $2, 'pending',
		  $3, NULLIF($4,''), $5, NULLIF($6,''),
		  $7, $8, NULLIF($9,''), NULLIF($10,''),
		  $11, $12,
		  NULLIF($13,''), NULLIF($14,''), NULLIF($15,''), NULLIF($16,''), NULLIF($17,''),
		  $18, $19, $20,
		  $21, $22
		)
		RETURNING `+orgSelectCols+`
	`,
		in.TenantID, orgNo,
		in.RegisteredName, in.TradingName, in.Kind, in.RegistrationNo,
		in.DateOfRegistration, in.DateOfOperation, in.Industry, in.NatureOfBusiness,
		in.MemberCount, in.EmployeeCount,
		in.PhysicalAddress, in.PostalAddress, in.County, in.SubCounty, in.Ward,
		in.GPSLat, in.GPSLng, in.BranchID,
		risk, in.CreatedBy,
	).Scan(orgScanDests(&o)...)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *OrgMemberStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Org, error) {
	var o domain.Org
	err := tx.QueryRow(ctx, `SELECT `+orgSelectCols+` FROM org_members WHERE id = $1`, id).
		Scan(orgScanDests(&o)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

type ListOrgsInput struct {
	Status domain.OrgStatus
	Kind   domain.OrgKind
	Query  string
	Limit  int
	Offset int
}

type ListOrgsResult struct {
	Orgs  []*domain.Org `json:"orgs"`
	Total int           `json:"total"`
}

func (s *OrgMemberStore) ListTx(ctx context.Context, tx pgx.Tx, in ListOrgsInput) (*ListOrgsResult, error) {
	args := []any{}
	where := []string{}
	if in.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, in.Status)
	}
	if in.Kind != "" {
		where = append(where, fmt.Sprintf("kind = $%d", len(args)+1))
		args = append(args, in.Kind)
	}
	if in.Query != "" {
		where = append(where, fmt.Sprintf("(registered_name ILIKE $%d OR org_no ILIKE $%d OR COALESCE(registration_no,'') ILIKE $%d)", len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+in.Query+"%")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM org_members"+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	args = append(args, in.Limit, in.Offset)
	rows, err := tx.Query(ctx, `SELECT `+orgSelectCols+` FROM org_members`+whereSQL+
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &ListOrgsResult{Total: total}
	for rows.Next() {
		var o domain.Org
		if err := rows.Scan(orgScanDests(&o)...); err != nil {
			return nil, err
		}
		out.Orgs = append(out.Orgs, &o)
	}
	return out, rows.Err()
}

func (s *OrgMemberStore) ApproveTx(ctx context.Context, tx pgx.Tx, id, approver uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE org_members
		SET status = 'active', approved_at = now(), approved_by = $2,
		    rejection_reason = NULL,
		    kyc_status = CASE WHEN kyc_status = 'not_started' THEN 'in_review' ELSE kyc_status END
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

func (s *OrgMemberStore) RejectTx(ctx context.Context, tx pgx.Tx, id, approver uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE org_members
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

func (s *OrgMemberStore) SetStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.OrgStatus) error {
	tag, err := tx.Exec(ctx, `UPDATE org_members SET status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *OrgMemberStore) SetKYCStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.KYCReviewStatus) error {
	tag, err := tx.Exec(ctx, `UPDATE org_members SET kyc_status = $2 WHERE id = $1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ─────────── Documents ───────────

type OrgDocumentStore struct{ pool *pgxpool.Pool }

func NewOrgDocumentStore(pool *pgxpool.Pool) *OrgDocumentStore { return &OrgDocumentStore{pool: pool} }

type CreateOrgDocumentInput struct {
	OrgID       uuid.UUID
	TenantID    uuid.UUID
	Kind        domain.OrgDocKind
	StoragePath string
	MIME        string
	SizeBytes   int64
	IssueDate   *time.Time
	ExpiryDate  *time.Time
	UploadedBy  *uuid.UUID
}

func (s *OrgDocumentStore) UpsertTx(ctx context.Context, tx pgx.Tx, in CreateOrgDocumentInput) (*domain.OrgDocument, error) {
	var d domain.OrgDocument
	err := tx.QueryRow(ctx, `
		INSERT INTO org_documents (org_id, tenant_id, kind, storage_path, mime, size_bytes, issue_date, expiry_date, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (org_id, kind) DO UPDATE
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
		RETURNING id, org_id, kind, storage_path, mime, size_bytes,
		          issue_date, expiry_date, verification, verified_by, verified_at,
		          COALESCE(verification_note,''), uploaded_at
	`, in.OrgID, in.TenantID, in.Kind, in.StoragePath, in.MIME, in.SizeBytes, in.IssueDate, in.ExpiryDate, in.UploadedBy).
		Scan(&d.ID, &d.OrgID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes,
			&d.IssueDate, &d.ExpiryDate, &d.Verification, &d.VerifiedBy, &d.VerifiedAt,
			&d.VerificationNote, &d.UploadedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *OrgDocumentStore) SetVerificationTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status domain.DocVerification, by uuid.UUID, note string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE org_documents
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

func (s *OrgDocumentStore) ListForOrgTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) ([]*domain.OrgDocument, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, org_id, kind, storage_path, mime, size_bytes,
		       issue_date, expiry_date, verification, verified_by, verified_at,
		       COALESCE(verification_note,''), uploaded_at
		FROM org_documents WHERE org_id = $1 ORDER BY kind
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.OrgDocument
	for rows.Next() {
		var d domain.OrgDocument
		if err := rows.Scan(&d.ID, &d.OrgID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes,
			&d.IssueDate, &d.ExpiryDate, &d.Verification, &d.VerifiedBy, &d.VerifiedAt,
			&d.VerificationNote, &d.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *OrgDocumentStore) ByKindTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, kind domain.OrgDocKind) (*domain.OrgDocument, error) {
	var d domain.OrgDocument
	err := tx.QueryRow(ctx, `
		SELECT id, org_id, kind, storage_path, mime, size_bytes,
		       issue_date, expiry_date, verification, verified_by, verified_at,
		       COALESCE(verification_note,''), uploaded_at
		FROM org_documents WHERE org_id = $1 AND kind = $2
	`, orgID, kind).Scan(&d.ID, &d.OrgID, &d.Kind, &d.StoragePath, &d.MIME, &d.SizeBytes,
		&d.IssueDate, &d.ExpiryDate, &d.Verification, &d.VerifiedBy, &d.VerifiedAt,
		&d.VerificationNote, &d.UploadedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ─────────── Officials ───────────

type OrgOfficialStore struct{ pool *pgxpool.Pool }

func NewOrgOfficialStore(pool *pgxpool.Pool) *OrgOfficialStore { return &OrgOfficialStore{pool: pool} }

type CreateOfficialInput struct {
	OrgID    uuid.UUID
	TenantID uuid.UUID

	FullName        string
	IDDocKind       domain.IDDocKind
	IDDocNumber     string
	KraPIN          string
	DateOfBirth     *time.Time
	Gender          domain.Gender
	Nationality     string
	Phone           string
	Email           string
	PhysicalAddress string
	Occupation      string

	Position      domain.OfficialPosition
	PositionLabel string
	AppointedOn   *time.Time

	IsPEP             bool
	PEPNote           string
	IsBeneficialOwner bool
	OwnershipPercent  *float64

	PositionOrder int
}

func (s *OrgOfficialStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateOfficialInput) (*domain.Official, error) {
	var o domain.Official
	err := tx.QueryRow(ctx, `
		INSERT INTO org_officials (
		  org_id, tenant_id,
		  full_name, id_doc_kind, id_doc_number, kra_pin,
		  date_of_birth, gender, nationality, phone, email, physical_address, occupation,
		  position, position_label, appointed_on,
		  is_pep, pep_note,
		  is_beneficial_owner, ownership_percent,
		  position_order
		) VALUES (
		  $1, $2,
		  $3, $4, $5, NULLIF($6,''),
		  $7, $8, NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), NULLIF($12,''), NULLIF($13,''),
		  $14, NULLIF($15,''), $16,
		  $17, NULLIF($18,''),
		  $19, $20,
		  $21
		)
		RETURNING `+officialSelectCols+`
	`,
		in.OrgID, in.TenantID,
		in.FullName, in.IDDocKind, in.IDDocNumber, in.KraPIN,
		in.DateOfBirth, in.Gender, in.Nationality, in.Phone, in.Email, in.PhysicalAddress, in.Occupation,
		in.Position, in.PositionLabel, in.AppointedOn,
		in.IsPEP, in.PEPNote,
		in.IsBeneficialOwner, in.OwnershipPercent,
		in.PositionOrder,
	).Scan(officialScanDests(&o)...)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *OrgOfficialStore) ListForOrgTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) ([]*domain.Official, error) {
	rows, err := tx.Query(ctx, `SELECT `+officialSelectCols+` FROM org_officials WHERE org_id = $1 ORDER BY position_order, full_name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Official
	for rows.Next() {
		var o domain.Official
		if err := rows.Scan(officialScanDests(&o)...); err != nil {
			return nil, err
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

func (s *OrgOfficialStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Official, error) {
	var o domain.Official
	err := tx.QueryRow(ctx, `SELECT `+officialSelectCols+` FROM org_officials WHERE id = $1`, id).Scan(officialScanDests(&o)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *OrgOfficialStore) SetSanctionsScreenedTx(ctx context.Context, tx pgx.Tx, id, by uuid.UUID, hit bool, note string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE org_officials
		SET sanctions_screened_at = now(),
		    sanctions_screened_by = $2,
		    sanctions_hit = $3,
		    sanctions_note = NULLIF($4,'')
		WHERE id = $1
	`, id, by, hit, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *OrgOfficialStore) SetFilesTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, files domain.OfficialFiles) error {
	b, err := json.Marshal(files)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE org_officials SET files = $2 WHERE id = $1`, id, b)
	return err
}

func (s *OrgOfficialStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM org_officials WHERE id = $1`, id)
	return err
}

// ─────────── Signatories + Mandate ───────────

type OrgSignatoryStore struct{ pool *pgxpool.Pool }

func NewOrgSignatoryStore(pool *pgxpool.Pool) *OrgSignatoryStore { return &OrgSignatoryStore{pool: pool} }

type SignatoryInput struct {
	OfficialID   uuid.UUID
	Class        domain.SignatoryClass
	SigningOrder int
	TxnLimit     *float64
}

func (s *OrgSignatoryStore) ReplaceTx(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID, sigs []SignatoryInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM org_signatories WHERE org_id = $1`, orgID); err != nil {
		return err
	}
	for i, sig := range sigs {
		order := sig.SigningOrder
		if order == 0 {
			order = i
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO org_signatories (org_id, tenant_id, official_id, class, signing_order, txn_limit)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, orgID, tenantID, sig.OfficialID, sig.Class, order, sig.TxnLimit); err != nil {
			return err
		}
	}
	return nil
}

func (s *OrgSignatoryStore) ListForOrgTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) ([]*domain.Signatory, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, org_id, official_id, class, signing_order, txn_limit, effective_from
		FROM org_signatories WHERE org_id = $1 ORDER BY signing_order, id
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Signatory
	for rows.Next() {
		var s domain.Signatory
		if err := rows.Scan(&s.ID, &s.OrgID, &s.OfficialID, &s.Class, &s.SigningOrder, &s.TxnLimit, &s.EffectiveFrom); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (s *OrgSignatoryStore) UpsertMandateTx(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID, rules map[string]any) error {
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO org_mandate (org_id, tenant_id, rules)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id) DO UPDATE
		  SET rules = EXCLUDED.rules, updated_at = now()
	`, orgID, tenantID, b)
	return err
}

func (s *OrgSignatoryStore) GetMandateTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (*domain.Mandate, error) {
	var m domain.Mandate
	var rules []byte
	err := tx.QueryRow(ctx, `SELECT org_id, rules, updated_at FROM org_mandate WHERE org_id = $1`, orgID).
		Scan(&m.OrgID, &rules, &m.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // mandate is optional
	}
	if err != nil {
		return nil, err
	}
	if len(rules) > 0 {
		_ = json.Unmarshal(rules, &m.Rules)
	}
	return &m, nil
}

// ─────────── Banking ───────────

type OrgBankingStore struct{ pool *pgxpool.Pool }

func NewOrgBankingStore(pool *pgxpool.Pool) *OrgBankingStore { return &OrgBankingStore{pool: pool} }

func (s *OrgBankingStore) UpsertTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, b domain.Banking) (*domain.Banking, error) {
	_, err := tx.Exec(ctx, `
		INSERT INTO org_banking (
		  org_id, tenant_id,
		  bank_name, bank_branch, bank_code, swift_code,
		  account_name, account_number,
		  paybill, till_number, mobile_money_phones, mobile_settlement_account,
		  preferred_disbursement, preferred_repayment,
		  standing_order_details, checkoff_arrangement
		) VALUES (
		  $1, $2,
		  NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''),
		  NULLIF($7,''), NULLIF($8,''),
		  NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), NULLIF($12,''),
		  NULLIF($13,''), NULLIF($14,''),
		  NULLIF($15,''), NULLIF($16,'')
		)
		ON CONFLICT (org_id) DO UPDATE SET
		  bank_name = EXCLUDED.bank_name,
		  bank_branch = EXCLUDED.bank_branch,
		  bank_code = EXCLUDED.bank_code,
		  swift_code = EXCLUDED.swift_code,
		  account_name = EXCLUDED.account_name,
		  account_number = EXCLUDED.account_number,
		  paybill = EXCLUDED.paybill,
		  till_number = EXCLUDED.till_number,
		  mobile_money_phones = EXCLUDED.mobile_money_phones,
		  mobile_settlement_account = EXCLUDED.mobile_settlement_account,
		  preferred_disbursement = EXCLUDED.preferred_disbursement,
		  preferred_repayment = EXCLUDED.preferred_repayment,
		  standing_order_details = EXCLUDED.standing_order_details,
		  checkoff_arrangement = EXCLUDED.checkoff_arrangement,
		  updated_at = now()
	`, b.OrgID, tenantID,
		b.BankName, b.BankBranch, b.BankCode, b.SwiftCode,
		b.AccountName, b.AccountNumber,
		b.Paybill, b.TillNumber, b.MobileMoneyPhones, b.MobileSettlementAccount,
		b.PreferredDisbursement, b.PreferredRepayment,
		b.StandingOrderDetails, b.CheckoffArrangement,
	)
	if err != nil {
		return nil, err
	}
	return s.ForOrgTx(ctx, tx, b.OrgID)
}

func (s *OrgBankingStore) ForOrgTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (*domain.Banking, error) {
	var b domain.Banking
	err := tx.QueryRow(ctx, `
		SELECT org_id,
		       COALESCE(bank_name,''), COALESCE(bank_branch,''), COALESCE(bank_code,''), COALESCE(swift_code,''),
		       COALESCE(account_name,''), COALESCE(account_number,''),
		       COALESCE(paybill,''), COALESCE(till_number,''), COALESCE(mobile_money_phones,''), COALESCE(mobile_settlement_account,''),
		       COALESCE(preferred_disbursement,''), COALESCE(preferred_repayment,''),
		       COALESCE(standing_order_details,''), COALESCE(checkoff_arrangement,''),
		       updated_at
		FROM org_banking WHERE org_id = $1
	`, orgID).Scan(&b.OrgID,
		&b.BankName, &b.BankBranch, &b.BankCode, &b.SwiftCode,
		&b.AccountName, &b.AccountNumber,
		&b.Paybill, &b.TillNumber, &b.MobileMoneyPhones, &b.MobileSettlementAccount,
		&b.PreferredDisbursement, &b.PreferredRepayment,
		&b.StandingOrderDetails, &b.CheckoffArrangement,
		&b.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ─────────── Contacts ───────────

type OrgContactStore struct{ pool *pgxpool.Pool }

func NewOrgContactStore(pool *pgxpool.Pool) *OrgContactStore { return &OrgContactStore{pool: pool} }

type ContactInput struct {
	Kind     domain.ContactKind
	FullName string
	Role     string
	Phone    string
	Email    string
}

// ReplaceTx wipes the existing contact set and re-inserts. The wizard
// posts the whole list on each save, which keeps the API tiny.
func (s *OrgContactStore) ReplaceTx(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID, rows []ContactInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM org_contacts WHERE org_id = $1`, orgID); err != nil {
		return err
	}
	for i, c := range rows {
		if strings.TrimSpace(c.FullName) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO org_contacts (org_id, tenant_id, kind, full_name, role, phone, email, position)
			VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), $8)
		`, orgID, tenantID, c.Kind, c.FullName, c.Role, c.Phone, c.Email, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *OrgContactStore) ListForOrgTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) ([]*domain.Contact, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, org_id, kind, full_name, COALESCE(role,''), COALESCE(phone,''), COALESCE(email,''), position
		FROM org_contacts WHERE org_id = $1 ORDER BY position, kind
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Contact
	for rows.Next() {
		var c domain.Contact
		if err := rows.Scan(&c.ID, &c.OrgID, &c.Kind, &c.FullName, &c.Role, &c.Phone, &c.Email, &c.Position); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ─────────── helpers ───────────

const orgSelectCols = `
id, tenant_id, org_no, status,
registered_name, COALESCE(trading_name,''),
kind, COALESCE(registration_no,''),
date_of_registration, date_of_operation,
COALESCE(industry,''), COALESCE(nature_of_business,''),
member_count, employee_count,
COALESCE(physical_address,''), COALESCE(postal_address,''),
COALESCE(county,''), COALESCE(sub_county,''), COALESCE(ward,''),
gps_lat, gps_lng, branch_id,
risk_category, kyc_status, blacklisted, COALESCE(blacklist_reason,''), dormant_since,
approved_at, approved_by, COALESCE(rejection_reason,''),
created_at, updated_at, created_by`

func orgScanDests(o *domain.Org) []any {
	return []any{
		&o.ID, &o.TenantID, &o.OrgNo, &o.Status,
		&o.RegisteredName, &o.TradingName,
		&o.Kind, &o.RegistrationNo,
		&o.DateOfRegistration, &o.DateOfOperation,
		&o.Industry, &o.NatureOfBusiness,
		&o.MemberCount, &o.EmployeeCount,
		&o.PhysicalAddress, &o.PostalAddress,
		&o.County, &o.SubCounty, &o.Ward,
		&o.GPSLat, &o.GPSLng, &o.BranchID,
		&o.RiskCategory, &o.KYCStatus, &o.Blacklisted, &o.BlacklistReason, &o.DormantSince,
		&o.ApprovedAt, &o.ApprovedBy, &o.RejectionReason,
		&o.CreatedAt, &o.UpdatedAt, &o.CreatedBy,
	}
}

const officialSelectCols = `
id, org_id, tenant_id,
full_name, id_doc_kind, id_doc_number, COALESCE(kra_pin,''),
date_of_birth, gender, COALESCE(nationality,''),
COALESCE(phone,''), COALESCE(email,''), COALESCE(physical_address,''), COALESCE(occupation,''),
position, COALESCE(position_label,''), appointed_on,
is_pep, COALESCE(pep_note,''),
sanctions_screened_at, sanctions_screened_by, sanctions_hit, COALESCE(sanctions_note,''),
is_beneficial_owner, ownership_percent,
files, position_order,
created_at, updated_at`

func officialScanDests(o *domain.Official) []any {
	return []any{
		&o.ID, &o.OrgID, &o.TenantID,
		&o.FullName, &o.IDDocKind, &o.IDDocNumber, &o.KraPIN,
		&o.DateOfBirth, &o.Gender, &o.Nationality,
		&o.Phone, &o.Email, &o.PhysicalAddress, &o.Occupation,
		&o.Position, &o.PositionLabel, &o.AppointedOn,
		&o.IsPEP, &o.PEPNote,
		&o.SanctionsScreenedAt, &o.SanctionsScreenedBy, &o.SanctionsHit, &o.SanctionsNote,
		&o.IsBeneficialOwner, &o.OwnershipPercent,
		officialFilesScan(&o.Files), &o.PositionOrder,
		&o.CreatedAt, &o.UpdatedAt,
	}
}

// officialFilesScan adapts JSONB → OfficialFiles via pgx's []byte path.
type officialFilesScanner struct{ dst *domain.OfficialFiles }

func (s officialFilesScanner) Scan(src any) error {
	if src == nil {
		*s.dst = nil
		return nil
	}
	b, ok := src.([]byte)
	if !ok {
		if str, ok2 := src.(string); ok2 {
			b = []byte(str)
		} else {
			return fmt.Errorf("officialFilesScanner: unsupported scan type %T", src)
		}
	}
	if len(b) == 0 {
		*s.dst = nil
		return nil
	}
	return json.Unmarshal(b, s.dst)
}

func officialFilesScan(dst *domain.OfficialFiles) any { return officialFilesScanner{dst: dst} }
