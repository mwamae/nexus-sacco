// Membership-application persistence.
//
// Owns the unified application table that backs the onboarding queue
// for both individual and institutional applicants. The status state
// machine lives in domain; this store enforces the legal transitions
// at write time by checking CanTransitionApp before each UPDATE.

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
	"github.com/shopspring/decimal"

	"github.com/nexussacco/member/internal/domain"
)

type ApplicationStore struct {
	pool *pgxpool.Pool
}

func NewApplicationStore(pool *pgxpool.Pool) *ApplicationStore {
	return &ApplicationStore{pool: pool}
}

var (
	ErrApplicationNotFound = errors.New("application not found")
)

// Phase E C: materialized_member_id + materialized_org_id columns
// dropped; materialized_counterparty_id is the canonical post-approval
// bridge from an application to the unified register.
const appCols = `
	id, tenant_id, application_no, kind::text, status::text,
	applicant_name, entity_type, primary_phone, primary_email, branch_id,
	applicant_payload,
	fee_required, fee_amount_due, fee_amount_paid,
	fee_payment_channel, fee_payment_reference, fee_payment_date,
	fee_proof_doc_path, fee_shortfall_note, fee_status,
	submitted_at, submitted_by,
	reviewer_user_id, review_started_at, review_completed_at, review_summary_note,
	approver_user_id, approved_at, decline_reason, approval_conditions, workflow_instance_id,
	withdrawn_at, withdrawn_by, withdraw_reason,
	materialized_counterparty_id, materialized_at, fee_journal_entry_id, fee_refund_journal_entry_id,
	opening_share_amount, opening_bosa_amount,
	created_at, updated_at,
	EXTRACT(EPOCH FROM (now() - submitted_at))::int / 86400 AS days_in_queue
`

func scanApplication(row pgx.Row) (*domain.MembershipApplication, error) {
	var a domain.MembershipApplication
	var kind, status string
	var payload []byte
	if err := row.Scan(
		&a.ID, &a.TenantID, &a.ApplicationNo, &kind, &status,
		&a.ApplicantName, &a.EntityType, &a.PrimaryPhone, &a.PrimaryEmail, &a.BranchID,
		&payload,
		&a.FeeRequired, &a.FeeAmountDue, &a.FeeAmountPaid,
		&a.FeePaymentChannel, &a.FeePaymentReference, &a.FeePaymentDate,
		&a.FeeProofDocPath, &a.FeeShortfallNote, &a.FeeStatus,
		&a.SubmittedAt, &a.SubmittedBy,
		&a.ReviewerUserID, &a.ReviewStartedAt, &a.ReviewCompletedAt, &a.ReviewSummaryNote,
		&a.ApproverUserID, &a.ApprovedAt, &a.DeclineReason, &a.ApprovalConditions, &a.WorkflowInstanceID,
		&a.WithdrawnAt, &a.WithdrawnBy, &a.WithdrawReason,
		&a.MaterializedCounterpartyID, &a.MaterializedAt, &a.FeeJournalEntryID, &a.FeeRefundJournalEntryID,
		&a.OpeningShareAmount, &a.OpeningBosaAmount,
		&a.CreatedAt, &a.UpdatedAt,
		&a.DaysInQueue,
	); err != nil {
		return nil, err
	}
	a.Kind = domain.ApplicationKind(kind)
	a.Status = domain.ApplicationStatus(status)
	a.ApplicantPayload = json.RawMessage(payload)
	return &a, nil
}

// ─────────── Application number sequence ───────────

// NextAppNo returns the next sequential application number for the
// tenant + current year, formatted APP-YYYY-NNNNNN.
func (s *ApplicationStore) NextAppNoTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (string, error) {
	year := time.Now().UTC().Year()
	var next int
	err := tx.QueryRow(ctx, `
		INSERT INTO membership_application_seq (tenant_id, year, last_no)
		VALUES ($1, $2, 1)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET year    = CASE WHEN membership_application_seq.year = $2 THEN membership_application_seq.year ELSE $2 END,
		       last_no = CASE WHEN membership_application_seq.year = $2 THEN membership_application_seq.last_no + 1 ELSE 1 END
		RETURNING last_no
	`, tenantID, year).Scan(&next)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("APP-%04d-%06d", year, next), nil
}

// ─────────── Create / submit ───────────

type CreateApplicationInput struct {
	TenantID            uuid.UUID
	Kind                domain.ApplicationKind
	ApplicantName       string
	EntityType          *string
	PrimaryPhone        *string
	PrimaryEmail        *string
	BranchID            *uuid.UUID
	Payload             domain.ApplicantPayload
	FeeRequired         bool
	FeeAmountDue        decimal.Decimal
	FeeAmountPaid       decimal.Decimal
	FeePaymentChannel   *string
	FeePaymentReference *string
	FeePaymentDate      *time.Time
	FeeProofDocPath     *string
	FeeShortfallNote    *string
	FeeStatus           string
	SubmittedBy         uuid.UUID
	// PR 5b — Opening contributions. Zero (the default) means "not
	// captured on the application"; the materialise handler skips
	// the corresponding savings call. Either column being non-zero
	// triggers the cross-service create + fund call.
	OpeningShareAmount decimal.Decimal
	OpeningBosaAmount  decimal.Decimal
}

func (s *ApplicationStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateApplicationInput) (*domain.MembershipApplication, error) {
	if !in.Kind.Valid() {
		return nil, fmt.Errorf("invalid application kind: %q", in.Kind)
	}
	if in.ApplicantName == "" {
		return nil, errors.New("applicant_name is required")
	}
	appNo, err := s.NextAppNoTx(ctx, tx, in.TenantID)
	if err != nil {
		return nil, fmt.Errorf("allocate application_no: %w", err)
	}
	payloadJSON, err := domain.EncodePayload(in.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	id := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO membership_applications (
		  id, tenant_id, application_no, kind, status,
		  applicant_name, entity_type, primary_phone, primary_email, branch_id,
		  applicant_payload,
		  fee_required, fee_amount_due, fee_amount_paid,
		  fee_payment_channel, fee_payment_reference, fee_payment_date,
		  fee_proof_doc_path, fee_shortfall_note, fee_status,
		  submitted_by,
		  opening_share_amount, opening_bosa_amount
		) VALUES (
		  $1, $2, $3, $4::membership_application_kind, 'submitted',
		  $5, $6, $7, $8, $9,
		  $10::jsonb,
		  $11, $12, $13,
		  $14, $15, $16,
		  $17, $18, $19,
		  $20,
		  $21, $22
		)
	`, id, in.TenantID, appNo, string(in.Kind),
		in.ApplicantName, in.EntityType, in.PrimaryPhone, in.PrimaryEmail, in.BranchID,
		payloadJSON,
		in.FeeRequired, in.FeeAmountDue, in.FeeAmountPaid,
		in.FeePaymentChannel, in.FeePaymentReference, in.FeePaymentDate,
		in.FeeProofDocPath, in.FeeShortfallNote, in.FeeStatus,
		in.SubmittedBy,
		in.OpeningShareAmount, in.OpeningBosaAmount,
	)
	if err != nil {
		return nil, fmt.Errorf("insert application: %w", err)
	}
	return s.GetTx(ctx, tx, id)
}

func (s *ApplicationStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.MembershipApplication, error) {
	row := tx.QueryRow(ctx, `SELECT `+appCols+` FROM membership_applications WHERE id = $1`, id)
	a, err := scanApplication(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrApplicationNotFound
	}
	return a, err
}

// ─────────── Queue list with filters ───────────

type ApplicationListFilter struct {
	Kind            string // individual | institutional | (empty for both)
	Status          string
	BranchID        *uuid.UUID
	SubmittedBy     *uuid.UUID
	FeeStatus       string // paid | shortfall | not_paid | not_required | (empty)
	Unassigned      bool   // reviewer_user_id IS NULL
	DateFrom        *time.Time
	DateTo          *time.Time
	SearchTerm      string // matches application_no, applicant_name, primary_email
	Limit, Offset   int
}

func (s *ApplicationStore) ListTx(ctx context.Context, tx pgx.Tx, f ApplicationListFilter) ([]domain.MembershipApplication, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}

	var wheres []string
	var args []any
	pos := 1
	if f.Kind != "" {
		wheres = append(wheres, fmt.Sprintf("kind = $%d::membership_application_kind", pos))
		args = append(args, f.Kind)
		pos++
	}
	if f.Status != "" {
		wheres = append(wheres, fmt.Sprintf("status = $%d::membership_application_status", pos))
		args = append(args, f.Status)
		pos++
	}
	if f.BranchID != nil {
		wheres = append(wheres, fmt.Sprintf("branch_id = $%d", pos))
		args = append(args, *f.BranchID)
		pos++
	}
	if f.SubmittedBy != nil {
		wheres = append(wheres, fmt.Sprintf("submitted_by = $%d", pos))
		args = append(args, *f.SubmittedBy)
		pos++
	}
	if f.FeeStatus != "" {
		wheres = append(wheres, fmt.Sprintf("fee_status = $%d", pos))
		args = append(args, f.FeeStatus)
		pos++
	}
	if f.Unassigned {
		wheres = append(wheres, "reviewer_user_id IS NULL")
	}
	if f.DateFrom != nil {
		wheres = append(wheres, fmt.Sprintf("submitted_at >= $%d", pos))
		args = append(args, *f.DateFrom)
		pos++
	}
	if f.DateTo != nil {
		wheres = append(wheres, fmt.Sprintf("submitted_at <= $%d", pos))
		args = append(args, *f.DateTo)
		pos++
	}
	if s := strings.TrimSpace(f.SearchTerm); s != "" {
		wheres = append(wheres, fmt.Sprintf("(application_no ILIKE $%d OR applicant_name ILIKE $%d OR COALESCE(primary_email,'') ILIKE $%d)", pos, pos, pos))
		args = append(args, "%"+s+"%")
		pos++
	}

	whereSQL := ""
	if len(wheres) > 0 {
		whereSQL = "WHERE " + strings.Join(wheres, " AND ")
	}

	// total count
	var total int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM membership_applications `+whereSQL, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `SELECT ` + appCols + ` FROM membership_applications ` + whereSQL +
		fmt.Sprintf(" ORDER BY submitted_at DESC LIMIT $%d OFFSET $%d", pos, pos+1)
	args = append(args, f.Limit, f.Offset)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []domain.MembershipApplication{}
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	return out, total, rows.Err()
}

// ─────────── Status transitions ───────────

// TransitionTx flips the application's status, recording the actor
// metadata for the new state. Returns the fresh application row.
type TransitionInput struct {
	ID            uuid.UUID
	From          domain.ApplicationStatus
	To            domain.ApplicationStatus
	ActorUserID   uuid.UUID
	Note          string
	DeclineReason string
	Conditions    string
}

func (s *ApplicationStore) TransitionTx(ctx context.Context, tx pgx.Tx, in TransitionInput) (*domain.MembershipApplication, error) {
	if !domain.CanTransitionApp(in.From, in.To) {
		return nil, domain.ErrIllegalAppTransition
	}

	// Common UPDATE applies status + updated_at + per-state side-effect
	// columns. We rely on Postgres' CASE WHEN to keep the SQL terse.
	_, err := tx.Exec(ctx, `
		UPDATE membership_applications SET
		  status            = $2::membership_application_status,
		  updated_at        = now(),

		  reviewer_user_id  = CASE WHEN $2 = 'under_review' AND reviewer_user_id IS NULL THEN $3 ELSE reviewer_user_id END,
		  review_started_at = CASE WHEN $2 = 'under_review' AND review_started_at IS NULL THEN now() ELSE review_started_at END,

		  review_completed_at = CASE WHEN $2 = 'reviewed_pending_approval' THEN now() ELSE review_completed_at END,
		  review_summary_note = CASE WHEN $2 = 'reviewed_pending_approval' THEN NULLIF($4,'') ELSE review_summary_note END,

		  approver_user_id  = CASE WHEN $2 IN ('approved_active','declined') THEN $3 ELSE approver_user_id END,
		  approved_at       = CASE WHEN $2 = 'approved_active' THEN now() ELSE approved_at END,
		  decline_reason    = CASE WHEN $2 = 'declined' THEN NULLIF($5,'') ELSE decline_reason END,
		  approval_conditions = CASE WHEN $2 = 'approved_active' AND $6 <> '' THEN $6 ELSE approval_conditions END,

		  withdrawn_at      = CASE WHEN $2 = 'withdrawn' THEN now() ELSE withdrawn_at END,
		  withdrawn_by      = CASE WHEN $2 = 'withdrawn' THEN $3   ELSE withdrawn_by END,
		  withdraw_reason   = CASE WHEN $2 = 'withdrawn' THEN NULLIF($4,'') ELSE withdraw_reason END

		 WHERE id = $1 AND status = $7::membership_application_status
	`, in.ID, string(in.To), in.ActorUserID, in.Note, in.DeclineReason, in.Conditions, string(in.From))
	if err != nil {
		return nil, err
	}
	return s.GetTx(ctx, tx, in.ID)
}

// ─────────── Checklist (per tenant) ───────────

func (s *ApplicationStore) ListChecklistItemsTx(ctx context.Context, tx pgx.Tx, kind domain.ApplicationKind) ([]domain.ChecklistItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, kind::text, code, label, description, mandatory, display_order, is_active
		  FROM membership_application_checklist_items
		 WHERE kind = $1::membership_application_kind AND is_active = true
		 ORDER BY display_order, code
	`, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ChecklistItem{}
	for rows.Next() {
		var it domain.ChecklistItem
		var k string
		if err := rows.Scan(&it.ID, &k, &it.Code, &it.Label, &it.Description, &it.Mandatory, &it.DisplayOrder, &it.IsActive); err != nil {
			return nil, err
		}
		it.Kind = domain.ApplicationKind(k)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *ApplicationStore) UpsertChecklistResponseTx(ctx context.Context, tx pgx.Tx,
	appID uuid.UUID, code, response, note string, actor uuid.UUID,
) (*domain.ChecklistResponse, error) {
	if response != "confirmed" && response != "flagged" && response != "n/a" {
		return nil, errors.New("response must be confirmed, flagged, or n/a")
	}
	var r domain.ChecklistResponse
	err := tx.QueryRow(ctx, `
		INSERT INTO membership_application_checklist_responses (
		  tenant_id, application_id, checklist_code, response, note, responded_by
		) VALUES (current_tenant_id(), $1, $2, $3, NULLIF($4,''), $5)
		ON CONFLICT (application_id, checklist_code) DO UPDATE
		   SET response = EXCLUDED.response,
		       note     = EXCLUDED.note,
		       responded_by = EXCLUDED.responded_by,
		       responded_at = now()
		RETURNING id, application_id, checklist_code, response, note, responded_by, responded_at
	`, appID, code, response, note, actor).Scan(
		&r.ID, &r.ApplicationID, &r.ChecklistCode, &r.Response, &r.Note, &r.RespondedBy, &r.RespondedAt,
	)
	return &r, err
}

func (s *ApplicationStore) ListChecklistResponsesTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.ChecklistResponse, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, application_id, checklist_code, response, note, responded_by, responded_at
		  FROM membership_application_checklist_responses
		 WHERE application_id = $1
		 ORDER BY responded_at DESC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ChecklistResponse{}
	for rows.Next() {
		var r domain.ChecklistResponse
		if err := rows.Scan(&r.ID, &r.ApplicationID, &r.ChecklistCode, &r.Response, &r.Note, &r.RespondedBy, &r.RespondedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────── Correction history ───────────

func (s *ApplicationStore) AppendCorrectionTx(ctx context.Context, tx pgx.Tx,
	appID uuid.UUID, eventKind, note string, actor uuid.UUID,
) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO membership_application_correction_history
		  (tenant_id, application_id, event_kind, actor_user_id, note)
		VALUES (current_tenant_id(), $1, $2, $3, $4)
	`, appID, eventKind, actor, note)
	return err
}

// ─────────── Activation ───────────

// ActivationResult — the artefacts produced when an application is
// approved. Created in the same tx as the status flip so a halfway
// failure rolls everything back.
//
// Individual applications produce: CounterpartyID + share account
// (mandatory) + deposit account (if default product configured).
//
// Institutional applications produce: OrgID only. Share and deposit
// auto-open are intentionally skipped — those tables FK to members.id
// (NOT NULL today). The org's first share / deposit accounts will be
// opened explicitly by an officer via the org-banking workflow once
// counterparty_id becomes the load-bearing FK in a follow-up PR.
//
// The XOR (Member vs Org) is the discriminator: exactly one of
// {CounterpartyID, OrgID} is set on any successful activation.
type ActivationResult struct {
	// Individual path
	CounterpartyID         uuid.UUID  // uuid.Nil for institutional apps
	MemberNo         string
	ShareAccountID   uuid.UUID
	ShareAccountNo   string
	DepositAccountID *uuid.UUID
	DepositAccountNo *string
	// PR 5b — opening contributions, set only when the application
	// carried non-zero opening_share_amount / opening_bosa_amount.
	// Zero shares or no BOSA → these stay at the empty defaults.
	BosaAccountID         *uuid.UUID
	BosaAccountNo         *string
	OpeningShareTxnID     *uuid.UUID
	OpeningBosaTxnID      *uuid.UUID
	OpeningSharesIssued   int
	// Institutional path
	OrgID uuid.UUID // uuid.Nil for individual apps
	OrgNo string
}

// nextSavingsSeq replicates the savings service's per-tenant number
// generator (share_number_seq + DPA/SHA prefix). The member service
// needs it because activation directly INSERTs into share/deposit
// accounts to keep the whole materialisation in one cross-table tx.
func nextSavingsSeq(ctx context.Context, tx pgx.Tx, kind, prefix string) (string, error) {
	year := time.Now().UTC().Year()
	var next int
	err := tx.QueryRow(ctx, `
		INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
		VALUES (current_tenant_id(), $1, $2, 1)
		ON CONFLICT (tenant_id, kind, year)
		DO UPDATE SET last_value = share_number_seq.last_value + 1
		RETURNING last_value
	`, kind, year).Scan(&next)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%d-%05d", prefix, year, next), nil
}

// ActivateApplicationTx is the in-tx half of the approval pipeline.
// Should be called immediately AFTER transitioning the application to
// approved_active and inside the same tx. Dispatches on kind: an
// individual application materialises into `members` + a share /
// deposit account; an institutional application materialises into
// `org_members` (no auto-opened accounts — see ActivationResult).
//
// `orgs` may be nil when the caller knows the application is
// individual; passing nil for an institutional application returns
// an error rather than risking an unhelpful nil-pointer panic.
func (s *ApplicationStore) ActivateApplicationTx(
	ctx context.Context, tx pgx.Tx,
	app *domain.MembershipApplication,
	members *MemberStore,
	orgs *OrgMemberStore,
	defaultDepositProductID *uuid.UUID,
	sharePolicyParValue decimal.Decimal,
	actorID uuid.UUID,
) (*ActivationResult, error) {
	if app.Kind == domain.ApplicationKindInstitutional {
		if orgs == nil {
			return nil, fmt.Errorf("activate institutional application: OrgMemberStore required")
		}
		return s.activateInstitutionalTx(ctx, tx, app, orgs, actorID)
	}
	return s.activateIndividualTx(ctx, tx, app, members, defaultDepositProductID, sharePolicyParValue, actorID)
}

// MaterialiseIndividualMemberTx inserts the members row for an
// approved individual application and nothing else. Returns the new
// member id + member_no. Auto-opened savings accounts and the
// materialized_member_id stamp run separately in
// OpenDefaultIndividualAccountsTx — the caller is expected to create
// the counterparty + stamp members.counterparty_id between the two
// calls. That ordering is load-bearing: the BEFORE INSERT triggers
// on share_accounts/deposit_accounts read members.counterparty_id at
// insert time, so the bridge must already be in place or the
// per-row counterparty_id silently nulls.
func (s *ApplicationStore) MaterialiseIndividualMemberTx(
	ctx context.Context, tx pgx.Tx,
	app *domain.MembershipApplication,
	members *MemberStore,
	actorID uuid.UUID,
) (uuid.UUID, string, error) {
	memberNo, err := members.NextMemberNoTx(ctx, tx, app.TenantID)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("allocate member_no: %w", err)
	}
	var payload domain.ApplicantPayload
	if len(app.ApplicantPayload) > 0 {
		_ = json.Unmarshal(app.ApplicantPayload, &payload)
	}
	idDocKind := payload.IDDocKind
	if idDocKind == "" {
		idDocKind = "national_id"
	}
	idDocNumber := payload.IDDocNumber
	if idDocNumber == "" {
		idDocNumber = payload.RegistrationNumber
		if idDocNumber == "" {
			idDocNumber = app.ApplicationNo
		}
	}
	gender := normalizeGender(payload.Gender)

	var memberID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO members (
		  tenant_id, member_no, status, full_name,
		  id_doc_kind, id_doc_number, kra_pin, gender, date_of_birth,
		  phone, email, county, sub_county, physical_address,
		  employment_status, employer,
		  approved_at, approved_by, created_by
		) VALUES (
		  current_tenant_id(), $1, 'active'::member_status, $2,
		  $3::id_doc_kind, $4, NULLIF($5,''), $6::gender, $7,
		  $8, $9, $10, $11, $12,
		  $13, $14,
		  now(), $15, $15
		)
		RETURNING id
	`,
		memberNo, app.ApplicantName,
		idDocKind, idDocNumber, payload.KRAPIN, gender, payload.DateOfBirth.Time,
		valOrNil(app.PrimaryPhone), valOrNil(app.PrimaryEmail),
		payload.County, payload.SubCounty, payload.PhysicalAddress,
		payload.Occupation, payload.Employer,
		actorID,
	).Scan(&memberID)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert member: %w", err)
	}
	return memberID, memberNo, nil
}

// OpenDefaultIndividualAccountsTx opens the default share + (optional)
// deposit account for an already-materialised member and stamps
// materialized_member_id + materialized_at on the application row.
// MUST be called AFTER members.counterparty_id is populated for the
// given memberID — otherwise the BEFORE INSERT triggers on
// share_accounts / deposit_accounts read NULL and the per-row
// counterparty_id ends up NULL, which silently corrupts every
// member-scoped read after the Phase D sub-PR 1 switchover.
func (s *ApplicationStore) OpenDefaultIndividualAccountsTx(
	ctx context.Context, tx pgx.Tx,
	app *domain.MembershipApplication,
	memberID uuid.UUID, memberNo string,
	defaultDepositProductID *uuid.UUID,
	sharePolicyParValue decimal.Decimal,
	actorID uuid.UUID,
) (*ActivationResult, error) {
	shareAcctNo, err := nextSavingsSeq(ctx, tx, "account", "SHA")
	if err != nil {
		return nil, fmt.Errorf("allocate share account_no: %w", err)
	}
	// Resolve members.id → counterparty.id at the boundary; the
	// counterparty_id columns on share_accounts and deposit_accounts
	// FK counterparties(id) post-Phase D sub-PR 2a.
	cpID, err := ResolveCounterpartyID(ctx, tx, memberID)
	if err != nil {
		return nil, fmt.Errorf("resolve counterparty for default account open: %w", err)
	}
	var shareAcctID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO share_accounts (tenant_id, counterparty_id, account_no, par_value_at_open)
		VALUES (current_tenant_id(), $1, $2, $3)
		RETURNING id
	`, cpID, shareAcctNo, sharePolicyParValue).Scan(&shareAcctID)
	if err != nil {
		return nil, fmt.Errorf("insert share account: %w", err)
	}

	result := &ActivationResult{
		CounterpartyID: cpID, MemberNo: memberNo,
		ShareAccountID: shareAcctID, ShareAccountNo: shareAcctNo,
	}

	if defaultDepositProductID != nil {
		depAcctNo, err := nextSavingsSeq(ctx, tx, "deposit_account", "DPA")
		if err != nil {
			return nil, fmt.Errorf("allocate deposit account_no: %w", err)
		}
		var depAcctID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO deposit_accounts (
			  tenant_id, counterparty_id, product_id, account_no, status,
			  current_balance, available_balance, opened_at, created_by
			) VALUES (
			  current_tenant_id(), $1, $2, $3, 'active'::deposit_account_status,
			  0, 0, now(), $4
			)
			RETURNING id
		`, cpID, *defaultDepositProductID, depAcctNo, actorID).Scan(&depAcctID)
		if err != nil {
			return nil, fmt.Errorf("insert deposit account: %w", err)
		}
		result.DepositAccountID = &depAcctID
		result.DepositAccountNo = &depAcctNo
	}

	// Phase E C: materialized_member_id column dropped. Just stamp
	// materialized_at; the canonical bridge (materialized_counterparty_id)
	// is stamped by the handler after the counterparty co-create runs.
	if _, err := tx.Exec(ctx, `
		UPDATE membership_applications
		   SET materialized_at = now(),
		       updated_at      = now()
		 WHERE id = $1
	`, app.ID); err != nil {
		return nil, fmt.Errorf("update application materialized_at: %w", err)
	}

	return result, nil
}

// activateIndividualTx — facade kept for parity with
// activateInstitutionalTx so ActivateApplicationTx's dispatch still
// works for any non-handler caller. The handler's ApproveAndActivate
// flow does NOT use this path; it calls the split pair directly so it
// can interleave counterparty creation between the two phases (the
// share/deposit BEFORE INSERT triggers depend on members.counterparty_id
// already being stamped). Any new caller that takes this single-call
// path will inherit that race — open the two phases and put the CP
// stamp between them instead.
func (s *ApplicationStore) activateIndividualTx(
	ctx context.Context, tx pgx.Tx,
	app *domain.MembershipApplication,
	members *MemberStore,
	defaultDepositProductID *uuid.UUID,
	sharePolicyParValue decimal.Decimal,
	actorID uuid.UUID,
) (*ActivationResult, error) {
	memberID, memberNo, err := s.MaterialiseIndividualMemberTx(ctx, tx, app, members, actorID)
	if err != nil {
		return nil, err
	}
	return s.OpenDefaultIndividualAccountsTx(ctx, tx, app, memberID, memberNo, defaultDepositProductID, sharePolicyParValue, actorID)
}

// resolveMemberIDForOpening looks up members.id from a counterparty
// id. Local to PR 5b's opening-contributions path; the share /
// deposit transaction tables both NOT NULL member_id. Sibling to
// ResolveCounterpartyID in counterparty_resolve.go, but inverse —
// the one in that file was removed in Phase D and the suggested
// replacement (MemberStore.GetByCounterpartyTx) returns the full
// member struct, which is more than this caller needs.
func resolveMemberIDForOpening(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (uuid.UUID, error) {
	var memberID uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM members WHERE counterparty_id = $1`, cpID,
	).Scan(&memberID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve member for counterparty %s: %w", cpID, err)
	}
	return memberID, nil
}

// PostOpeningContributionsTx fans out the opening_share_amount and
// opening_bosa_amount captured on the application into actual
// posted transactions, in the same activation tx so a halfway
// failure rolls everything back. Mutates `result` in-place with the
// new ids + account numbers. Safe to call with both amounts zero:
// it's a no-op when the application carried no contributions.
//
// Lookup conventions:
//   - The BOSA product is `SELECT id FROM deposit_products WHERE
//     segment='bosa' AND is_active=true ORDER BY created_at LIMIT 1`.
//     PR 1 seeds one per tenant, so for any tenant flipped onto the
//     BOSA/FOSA flag this resolves; if it doesn't exist we error
//     loudly rather than silently skipping (the application captured
//     the amount on the assumption that there is somewhere to put it).
//   - Share opening rounds DOWN to whole shares: shares_delta =
//     floor(opening_share_amount / par_value). The remainder (if any)
//     is intentionally lost — the spec wants whole-share semantics
//     and the form should validate at the boundary; the store is
//     the last line of defence, not the first.
//
// Direct INSERTs into share_transactions / deposit_accounts /
// deposit_transactions are the same pattern OpenDefaultIndividual
// AccountsTx already uses; no cross-service call is needed.
func (s *ApplicationStore) PostOpeningContributionsTx(
	ctx context.Context, tx pgx.Tx,
	app *domain.MembershipApplication,
	result *ActivationResult,
	sharePolicyParValue decimal.Decimal,
	actorID uuid.UUID,
) error {
	if result == nil || result.CounterpartyID == uuid.Nil {
		// Institutional activations don't carry a counterparty here;
		// PR 5b only covers the individual path.
		return nil
	}

	// ─── Opening shares ───
	if app.OpeningShareAmount.GreaterThan(decimal.Zero) && result.ShareAccountID != uuid.Nil {
		if sharePolicyParValue.IsZero() {
			return fmt.Errorf("post opening shares: tenant share_par_value is zero")
		}
		// shares_delta = floor(amount / par). The amount we actually
		// post = shares_delta × par; leftover cents (if any) are
		// dropped because the share-ledger schema only supports
		// whole-share quantities.
		sharesDelta := app.OpeningShareAmount.Div(sharePolicyParValue).Floor().IntPart()
		if sharesDelta > 0 {
			postedAmount := decimal.NewFromInt(sharesDelta).Mul(sharePolicyParValue)
			memberID, err := resolveMemberIDForOpening(ctx, tx, result.CounterpartyID)
			if err != nil {
				return fmt.Errorf("resolve member for share opening: %w", err)
			}
			txnNo, err := nextSavingsSeq(ctx, tx, "txn", "SHT")
			if err != nil {
				return fmt.Errorf("allocate share txn_no: %w", err)
			}
			var txnID uuid.UUID
			err = tx.QueryRow(ctx, `
				INSERT INTO share_transactions (
				  tenant_id, account_id, member_id, txn_no, txn_type, shares_delta,
				  par_value_at_txn, amount, payment_channel, narration,
				  balance_after_shares, balance_after_amount, initiated_by
				) VALUES (
				  current_tenant_id(), $1, $2, $3, 'purchase'::share_txn_type, $4,
				  $5, $6, 'cash'::share_payment_channel, $7,
				  $4, $6, $8
				)
				RETURNING id
			`, result.ShareAccountID, memberID, txnNo, sharesDelta,
				sharePolicyParValue, postedAmount,
				"Opening share purchase · "+app.ApplicationNo, actorID,
			).Scan(&txnID)
			if err != nil {
				return fmt.Errorf("insert opening share txn: %w", err)
			}
			// Roll the cached running balances on share_accounts.
			if _, err := tx.Exec(ctx, `
				UPDATE share_accounts
				   SET shares_held  = shares_held + $2,
				       total_value  = total_value + $3
				 WHERE id = $1
			`, result.ShareAccountID, sharesDelta, postedAmount); err != nil {
				return fmt.Errorf("bump share_accounts running balance: %w", err)
			}
			result.OpeningShareTxnID = &txnID
			result.OpeningSharesIssued = int(sharesDelta)
		}
	}

	// ─── Opening BOSA deposit ───
	if app.OpeningBosaAmount.GreaterThan(decimal.Zero) {
		// Resolve the tenant's seeded BOSA product. PR 1's seeded MD
		// row is the canonical target; tenants who configured their
		// own land on whichever has the earliest created_at.
		var bosaProductID uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM deposit_products
			 WHERE segment = 'bosa' AND is_active = true
			 ORDER BY created_at LIMIT 1
		`).Scan(&bosaProductID)
		if err != nil {
			return fmt.Errorf("locate BOSA product: %w", err)
		}
		// Open the BOSA deposit account.
		bosaAcctNo, err := nextSavingsSeq(ctx, tx, "deposit_account", "DPA")
		if err != nil {
			return fmt.Errorf("allocate BOSA deposit account_no: %w", err)
		}
		var bosaAcctID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO deposit_accounts (
			  tenant_id, counterparty_id, product_id, account_no, status,
			  current_balance, available_balance, opened_at, created_by
			) VALUES (
			  current_tenant_id(), $1, $2, $3, 'active'::deposit_account_status,
			  0, 0, now(), $4
			)
			RETURNING id
		`, result.CounterpartyID, bosaProductID, bosaAcctNo, actorID).Scan(&bosaAcctID)
		if err != nil {
			return fmt.Errorf("insert BOSA deposit account: %w", err)
		}
		// Post the opening_balance txn.
		memberID, err := resolveMemberIDForOpening(ctx, tx, result.CounterpartyID)
		if err != nil {
			return fmt.Errorf("resolve member for BOSA opening: %w", err)
		}
		txnNo, err := nextSavingsSeq(ctx, tx, "deposit_txn", "DPT")
		if err != nil {
			return fmt.Errorf("allocate deposit txn_no: %w", err)
		}
		var depTxnID uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO deposit_transactions (
			  tenant_id, account_id, counterparty_id, member_id, txn_no, txn_type,
			  amount, balance_after, narration, initiated_by
			) VALUES (
			  current_tenant_id(), $1, $2, $3, $4, 'opening_balance'::deposit_txn_type,
			  $5, $5, $6, $7
			)
			RETURNING id
		`, bosaAcctID, result.CounterpartyID, memberID, txnNo,
			app.OpeningBosaAmount, "Opening BOSA contribution · "+app.ApplicationNo, actorID,
		).Scan(&depTxnID)
		if err != nil {
			return fmt.Errorf("insert opening BOSA txn: %w", err)
		}
		// Bump the cached balance.
		if _, err := tx.Exec(ctx, `
			UPDATE deposit_accounts
			   SET current_balance = $2, available_balance = $2,
			       last_deposit_at = now(), last_activity_at = now()
			 WHERE id = $1
		`, bosaAcctID, app.OpeningBosaAmount); err != nil {
			return fmt.Errorf("bump deposit_accounts running balance: %w", err)
		}
		result.BosaAccountID = &bosaAcctID
		result.BosaAccountNo = &bosaAcctNo
		result.OpeningBosaTxnID = &depTxnID
	}
	return nil
}

// activateInstitutionalTx — the institutional branch of the
// approval pipeline. Materialises into org_members (not members),
// extracting org-shaped fields from the application's free-form
// applicant_payload. Does NOT auto-open a share or deposit account
// — those tables FK to members.id; the org's first accounts will be
// opened explicitly by an officer once the counterparty_id FK
// migration lands.
func (s *ApplicationStore) activateInstitutionalTx(
	ctx context.Context, tx pgx.Tx,
	app *domain.MembershipApplication,
	orgs *OrgMemberStore,
	actorID uuid.UUID,
) (*ActivationResult, error) {
	var payload domain.ApplicantPayload
	if len(app.ApplicantPayload) > 0 {
		_ = json.Unmarshal(app.ApplicantPayload, &payload)
	}

	orgNo, err := orgs.NextOrgNoTx(ctx, tx, app.TenantID)
	if err != nil {
		return nil, fmt.Errorf("allocate org_no: %w", err)
	}

	// Map the application's free-form payload onto the legacy
	// org_kind enum so the org_members CHECK constraints pass.
	// Mirrors handler/guessInstitutionalKind but emits the
	// org_kind enum values (which are a different set from the
	// counterparty_kind enum).
	orgKind := guessOrgKindFromPayload(app.ApplicantName, payload)

	var orgID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO org_members (
		  tenant_id, org_no, status, registered_name, trading_name, kind,
		  registration_no, date_of_registration, date_of_operation,
		  industry, nature_of_business,
		  physical_address, postal_address, county, sub_county, ward,
		  kyc_status, risk_category,
		  approved_at, approved_by, created_by
		) VALUES (
		  current_tenant_id(), $1, 'active'::org_status, $2, $3, $4::org_kind,
		  $5, $6, $7,
		  $8, $9,
		  $10, $11, $12, $13, $14,
		  'verified'::kyc_review_status, 'medium'::risk_category,
		  now(), $15, $15
		)
		RETURNING id
	`,
		orgNo, app.ApplicantName, payload.TradingName, orgKind,
		valOrEmptyStr(payload.RegistrationNumber), payload.DateOfRegistration.Time, payload.DateOfRegistration.Time,
		valOrEmptyStr(payload.Industry), valOrEmptyStr(payload.NatureOfBusiness),
		valOrEmptyStr(payload.PhysicalAddress), valOrEmptyStr(payload.PostalAddress),
		valOrEmptyStr(payload.County), valOrEmptyStr(payload.SubCounty), valOrEmptyStr(payload.Ward),
		actorID,
	).Scan(&orgID)
	if err != nil {
		return nil, fmt.Errorf("insert org_member: %w", err)
	}

	// Phase E C: materialized_org_id column dropped. Just stamp
	// materialized_at; the canonical bridge (materialized_counterparty_id)
	// is stamped by the handler after the org-counterparty co-create.
	if _, err := tx.Exec(ctx, `
		UPDATE membership_applications
		   SET materialized_at = now(),
		       updated_at      = now()
		 WHERE id = $1
	`, app.ID); err != nil {
		return nil, fmt.Errorf("update application materialized_at: %w", err)
	}

	return &ActivationResult{OrgID: orgID, OrgNo: orgNo}, nil
}

// valOrEmptyStr — pgx is happy with empty strings on nullable text
// columns. Centralises the empty-string convention so the INSERT
// doesn't drown in NULLIF() noise.
func valOrEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// guessOrgKindFromPayload — emit the legacy org_kind enum (NOT the
// counterparty_kind one). Mirrors the heuristic in
// handler/guessInstitutionalKind but for the right enum.
func guessOrgKindFromPayload(name string, p domain.ApplicantPayload) string {
	hint := name + " " + p.RegisteredName + " " + p.NatureOfBusiness + " " + p.Industry
	lower := ""
	for _, r := range hint {
		if r >= 'A' && r <= 'Z' {
			lower += string(r + 32)
		} else {
			lower += string(r)
		}
	}
	switch {
	case contains(lower, "chama"):       return "chama"
	case contains(lower, "group"):       return "group"
	case contains(lower, "church"):      return "church"
	case contains(lower, "school"):      return "school"
	case contains(lower, "academy"):     return "school"
	case contains(lower, "ngo"):         return "ngo"
	case contains(lower, "foundation"):  return "ngo"
	case contains(lower, "sole prop"):   return "sole_prop"
	case contains(lower, "cooperative"): return "cooperative"
	case contains(lower, "sacco"):       return "sacco"
	case contains(lower, "limited"), contains(lower, "ltd"), contains(lower, "company"):
		return "ltd"
	default:
		// 'group' is the broadest non-specific kind in the legacy
		// enum and matches the spirit of "we don't know yet".
		return "group"
	}
}

func contains(haystack, needle string) bool {
	if len(needle) > len(haystack) { return false }
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle { return true }
	}
	return false
}

// SetFeeJournalEntryTx records the journal-entry id of the
// registration-fee post. Run AFTER the accounting service returned a
// successful entry id.
func (s *ApplicationStore) SetFeeJournalEntryTx(ctx context.Context, tx pgx.Tx, appID, jeID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE membership_applications
		   SET fee_journal_entry_id = $2, updated_at = now()
		 WHERE id = $1
	`, appID, jeID)
	return err
}

// SetFeeRefundJournalEntryTx records the reversal journal-entry id
// and flips fee_status to 'refunded'.
func (s *ApplicationStore) SetFeeRefundJournalEntryTx(ctx context.Context, tx pgx.Tx, appID, jeID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE membership_applications
		   SET fee_refund_journal_entry_id = $2,
		       fee_status                  = 'refunded',
		       updated_at                  = now()
		 WHERE id = $1
	`, appID, jeID)
	return err
}

func valOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// normalizeGender coerces the free-form value captured at submission time
// into a value the members.gender enum (male/female/other/undisclosed)
// accepts. Single-letter shorthand from upstream forms is mapped to the
// long form; anything unrecognised becomes 'undisclosed'.
func normalizeGender(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "m", "male":
		return "male"
	case "f", "female":
		return "female"
	case "o", "other", "x":
		return "other"
	default:
		return "undisclosed"
	}
}

func (s *ApplicationStore) ListCorrectionsTx(ctx context.Context, tx pgx.Tx, appID uuid.UUID) ([]domain.CorrectionEvent, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, application_id, event_kind, actor_user_id, note, created_at
		  FROM membership_application_correction_history
		 WHERE application_id = $1
		 ORDER BY created_at DESC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CorrectionEvent{}
	for rows.Next() {
		var e domain.CorrectionEvent
		if err := rows.Scan(&e.ID, &e.ApplicationID, &e.EventKind, &e.ActorUserID, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
