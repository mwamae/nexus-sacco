// Status-change store. Three things live here:
//   * StatusChangeStore.ApplyTx     — atomic status update + audit row +
//                                     per-status side-effect column updates
//   * StatusChangeStore.HistoryTx   — paginated history for a single member
//   * StatusChangeStore.ProposalsTx — pending workflow-mediated proposals
//   * StatusChangeStore.DormancyCandidatesTx — for the dormancy runner
//
// All writes happen inside an outer WithTenantTx so RLS applies.

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

	"github.com/nexussacco/member/internal/domain"
)

type StatusChangeStore struct {
	pool *pgxpool.Pool
}

func NewStatusChangeStore(pool *pgxpool.Pool) *StatusChangeStore {
	return &StatusChangeStore{pool: pool}
}

type ApplyInput struct {
	TenantID           uuid.UUID
	MemberID           uuid.UUID
	FromStatus         domain.MemberStatus
	ToStatus           domain.MemberStatus
	ReasonCategory     domain.StatusReason
	ReasonNote         string
	SupportingDocPath  string
	SupportingDocMIME  string
	ChangedBy          *uuid.UUID
	WorkflowInstanceID *uuid.UUID
	ReviewDate         *time.Time
}

// ApplyTx updates the member's status, writes the audit row, and bumps
// the per-status side-effect columns (blacklisted_at, deceased_at, etc.).
// The caller has already validated the transition via domain.ValidateTransition.
func (s *StatusChangeStore) ApplyTx(ctx context.Context, tx pgx.Tx, in ApplyInput) (*domain.MemberStatusChange, error) {
	// Status flip + side-effect column updates in a single statement.
	// The $2::member_status casts are required because pgx can't infer
	// the parameter type when the same placeholder is compared against
	// multiple enum literals.
	if _, err := tx.Exec(ctx, `
		UPDATE members SET
		  status              = $2::member_status,
		  status_changed_at   = now(),
		  blacklist_reason    = CASE WHEN $2::member_status = 'blacklisted' THEN NULLIF($3,'') ELSE blacklist_reason END,
		  blacklist_authorized_by = CASE WHEN $2::member_status = 'blacklisted' THEN $4 ELSE blacklist_authorized_by END,
		  blacklisted_at      = CASE WHEN $2::member_status = 'blacklisted' AND blacklisted_at IS NULL THEN now() ELSE blacklisted_at END,
		  deceased_at         = CASE WHEN $2::member_status = 'deceased' AND deceased_at IS NULL THEN now() ELSE deceased_at END,
		  exit_initiated_at   = CASE WHEN $2::member_status = 'exited' AND exit_initiated_at IS NULL THEN now() ELSE exit_initiated_at END,
		  exit_completed_at   = CASE WHEN $2::member_status = 'exited' THEN now() ELSE exit_completed_at END,
		  dormancy_warning_sent_at = CASE WHEN $2::member_status = 'active' THEN NULL ELSE dormancy_warning_sent_at END,
		  last_activity_at    = CASE WHEN $2::member_status = 'active' AND last_activity_at IS NULL THEN now() ELSE last_activity_at END
		WHERE id = $1
	`, in.MemberID, string(in.ToStatus), in.ReasonNote, in.ChangedBy); err != nil {
		return nil, fmt.Errorf("update member status: %w", err)
	}
	var c domain.MemberStatusChange
	var path *string
	var mime *string
	err := tx.QueryRow(ctx, `
		INSERT INTO member_status_changes (
		  tenant_id, member_id, from_status, to_status,
		  reason_category, reason_note, supporting_doc_path, supporting_doc_mime,
		  changed_by, workflow_instance_id, review_date
		) VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10, $11)
		RETURNING id, member_id, from_status, to_status, reason_category, COALESCE(reason_note,''),
		          supporting_doc_path, supporting_doc_mime, changed_by, changed_at,
		          workflow_instance_id, review_date
	`, in.TenantID, in.MemberID, in.FromStatus, in.ToStatus,
		in.ReasonCategory, in.ReasonNote, in.SupportingDocPath, in.SupportingDocMIME,
		in.ChangedBy, in.WorkflowInstanceID, in.ReviewDate,
	).Scan(&c.ID, &c.MemberID, &c.FromStatus, &c.ToStatus, &c.ReasonCategory, &c.ReasonNote,
		&path, &mime, &c.ChangedBy, &c.ChangedAt, &c.WorkflowInstanceID, &c.ReviewDate)
	if err != nil {
		return nil, fmt.Errorf("insert status change row: %w", err)
	}
	if path != nil {
		c.SupportingDocPath = *path
		c.HasSupportingDoc = true
	}
	if mime != nil {
		c.SupportingDocMIME = *mime
	}
	return &c, nil
}

// HistoryTx lists status changes for a member, newest first.
func (s *StatusChangeStore) HistoryTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID, limit int) ([]*domain.MemberStatusChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := tx.Query(ctx, `
		SELECT id, member_id, from_status, to_status, reason_category, COALESCE(reason_note,''),
		       supporting_doc_path, supporting_doc_mime, changed_by, changed_at,
		       workflow_instance_id, review_date
		FROM member_status_changes
		WHERE member_id = $1
		ORDER BY changed_at DESC LIMIT $2
	`, memberID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.MemberStatusChange
	for rows.Next() {
		var c domain.MemberStatusChange
		var path, mime *string
		if err := rows.Scan(&c.ID, &c.MemberID, &c.FromStatus, &c.ToStatus, &c.ReasonCategory, &c.ReasonNote,
			&path, &mime, &c.ChangedBy, &c.ChangedAt, &c.WorkflowInstanceID, &c.ReviewDate); err != nil {
			return nil, err
		}
		if path != nil {
			c.SupportingDocPath = *path
			c.HasSupportingDoc = true
		}
		if mime != nil {
			c.SupportingDocMIME = *mime
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ─────────── Proposals (sensitive transitions awaiting workflow approval) ───────────

type ProposalInput struct {
	TenantID           uuid.UUID
	MemberID           uuid.UUID
	WorkflowInstanceID uuid.UUID
	ProposedStatus     domain.MemberStatus
	ReasonCategory     domain.StatusReason
	ReasonNote         string
	SupportingDocPath  string
	SupportingDocMIME  string
	ReviewDate         *time.Time
	ProposedBy         *uuid.UUID
}

func (s *StatusChangeStore) CreateProposalTx(ctx context.Context, tx pgx.Tx, in ProposalInput) (*domain.MemberStatusProposal, error) {
	var p domain.MemberStatusProposal
	var path, mime *string
	err := tx.QueryRow(ctx, `
		INSERT INTO member_status_proposals (
		  tenant_id, member_id, workflow_instance_id, proposed_status,
		  reason_category, reason_note,
		  supporting_doc_path, supporting_doc_mime,
		  review_date, proposed_by
		) VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10)
		RETURNING id, member_id, workflow_instance_id, proposed_status,
		          reason_category, COALESCE(reason_note,''),
		          supporting_doc_path, supporting_doc_mime,
		          review_date, proposed_by, proposed_at, resolved_at, COALESCE(resolution,'')
	`, in.TenantID, in.MemberID, in.WorkflowInstanceID, in.ProposedStatus,
		in.ReasonCategory, in.ReasonNote,
		in.SupportingDocPath, in.SupportingDocMIME,
		in.ReviewDate, in.ProposedBy,
	).Scan(&p.ID, &p.MemberID, &p.WorkflowInstanceID, &p.ProposedStatus,
		&p.ReasonCategory, &p.ReasonNote,
		&path, &mime,
		&p.ReviewDate, &p.ProposedBy, &p.ProposedAt, &p.ResolvedAt, &p.Resolution,
	)
	if err != nil {
		return nil, err
	}
	if path != nil { p.HasSupportingDoc = true }
	_ = mime
	return &p, nil
}

func (s *StatusChangeStore) ProposalByWorkflowTx(ctx context.Context, tx pgx.Tx, workflowInstanceID uuid.UUID) (*domain.MemberStatusProposal, string, string, error) {
	var p domain.MemberStatusProposal
	var path, mime *string
	err := tx.QueryRow(ctx, `
		SELECT id, member_id, workflow_instance_id, proposed_status,
		       reason_category, COALESCE(reason_note,''),
		       supporting_doc_path, supporting_doc_mime,
		       review_date, proposed_by, proposed_at, resolved_at, COALESCE(resolution,'')
		FROM member_status_proposals
		WHERE workflow_instance_id = $1
	`, workflowInstanceID).Scan(&p.ID, &p.MemberID, &p.WorkflowInstanceID, &p.ProposedStatus,
		&p.ReasonCategory, &p.ReasonNote,
		&path, &mime,
		&p.ReviewDate, &p.ProposedBy, &p.ProposedAt, &p.ResolvedAt, &p.Resolution,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", ErrNotFound
	}
	if err != nil {
		return nil, "", "", err
	}
	if path != nil { p.HasSupportingDoc = true }
	pp, mm := "", ""
	if path != nil {
		pp = *path
	}
	if mime != nil {
		mm = *mime
	}
	return &p, pp, mm, nil
}

func (s *StatusChangeStore) ResolveProposalTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, resolution string) error {
	_, err := tx.Exec(ctx, `
		UPDATE member_status_proposals
		SET resolved_at = now(), resolution = $2
		WHERE id = $1 AND resolved_at IS NULL
	`, id, resolution)
	return err
}

func (s *StatusChangeStore) OpenProposalsForMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) ([]*domain.MemberStatusProposal, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, member_id, workflow_instance_id, proposed_status,
		       reason_category, COALESCE(reason_note,''),
		       review_date, proposed_by, proposed_at, resolved_at, COALESCE(resolution,'')
		FROM member_status_proposals
		WHERE member_id = $1 AND resolved_at IS NULL
		ORDER BY proposed_at DESC
	`, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.MemberStatusProposal
	for rows.Next() {
		var p domain.MemberStatusProposal
		if err := rows.Scan(&p.ID, &p.MemberID, &p.WorkflowInstanceID, &p.ProposedStatus,
			&p.ReasonCategory, &p.ReasonNote,
			&p.ReviewDate, &p.ProposedBy, &p.ProposedAt, &p.ResolvedAt, &p.Resolution); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// ─────────── Dormancy + activity ───────────

// TouchActivityTx bumps last_activity_at — call this when ANY
// member-initiated event happens (deposit, withdrawal, loan repayment,
// portal login, …). Today the member service has no transactional
// endpoints so this is exposed for the future modules to call.
func (s *StatusChangeStore) TouchActivityTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE members SET last_activity_at = now() WHERE id = $1`, memberID)
	return err
}

// DormancyCandidate is one row in the dormancy run report.
type DormancyCandidate struct {
	MemberID       uuid.UUID  `json:"member_id"`
	MemberNo       string     `json:"member_no"`
	FullName       string     `json:"full_name"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	DaysInactive   int        `json:"days_inactive"`
}

// DormancyCandidatesTx returns active members whose last_activity_at is
// older than the threshold (or null AND created longer ago than the
// threshold).
func (s *StatusChangeStore) DormancyCandidatesTx(ctx context.Context, tx pgx.Tx, thresholdDays int) ([]*DormancyCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, member_no, full_name,
		       last_activity_at,
		       (EXTRACT(EPOCH FROM (now() - COALESCE(last_activity_at, created_at))) / 86400)::int AS days_inactive
		FROM members
		WHERE status = 'active'
		  AND COALESCE(last_activity_at, created_at) < now() - ($1::int * INTERVAL '1 day')
		ORDER BY COALESCE(last_activity_at, created_at) ASC
	`, thresholdDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DormancyCandidate
	for rows.Next() {
		var c DormancyCandidate
		if err := rows.Scan(&c.MemberID, &c.MemberNo, &c.FullName, &c.LastActivityAt, &c.DaysInactive); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// DormancyPipelineTx returns active members within `warnDays` of the
// threshold so the UI can show a "approaching dormancy" panel.
func (s *StatusChangeStore) DormancyPipelineTx(ctx context.Context, tx pgx.Tx, thresholdDays, warnDays int) ([]*DormancyCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, member_no, full_name,
		       last_activity_at,
		       (EXTRACT(EPOCH FROM (now() - COALESCE(last_activity_at, created_at))) / 86400)::int AS days_inactive
		FROM members
		WHERE status = 'active'
		  AND COALESCE(last_activity_at, created_at) BETWEEN
		       (now() - ($1::int * INTERVAL '1 day'))
		   AND (now() - (($1 - $2)::int * INTERVAL '1 day'))
		ORDER BY COALESCE(last_activity_at, created_at) ASC
	`, thresholdDays, warnDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DormancyCandidate
	for rows.Next() {
		var c DormancyCandidate
		if err := rows.Scan(&c.MemberID, &c.MemberNo, &c.FullName, &c.LastActivityAt, &c.DaysInactive); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ─────────── Member roll-call counts (canonical) ───────────
//
// MemberStatusCounts is the canonical roll-call shape returned by the
// member_status_counts(tenant_id) Postgres function. Every UI that
// shows "members on the register" or "active members" MUST consume
// this struct so the dashboard widget and the Members page KPI strip
// can never disagree. See migration 0006 for bucket semantics.

type MemberStatusCounts struct {
	Active               int `json:"active"`
	Dormant              int `json:"dormant"`
	Pending              int `json:"pending"`
	Suspended            int `json:"suspended"`
	Blacklisted          int `json:"blacklisted"`
	Exited               int `json:"exited"`
	Deceased             int `json:"deceased"`
	Rejected             int `json:"rejected"`
	TotalOnRegister      int `json:"total_on_register"`
	TotalActiveServicing int `json:"total_active_servicing"`
}

func (s *StatusChangeStore) MemberStatusCountsTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*MemberStatusCounts, error) {
	var c MemberStatusCounts
	err := tx.QueryRow(ctx, `
		SELECT active, dormant, pending, suspended, blacklisted, exited, deceased, rejected,
		       total_on_register, total_active_servicing
		  FROM member_status_counts($1)
	`, tenantID).Scan(
		&c.Active, &c.Dormant, &c.Pending, &c.Suspended, &c.Blacklisted,
		&c.Exited, &c.Deceased, &c.Rejected,
		&c.TotalOnRegister, &c.TotalActiveServicing,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ByStatus exposes the per-bucket counts as a map for backwards-compatible
// callers (the existing /v1/members/status/summary response shape).
func (c *MemberStatusCounts) ByStatus() map[domain.MemberStatus]int {
	return map[domain.MemberStatus]int{
		domain.StatusActive:      c.Active,
		domain.StatusDormant:     c.Dormant,
		domain.StatusPending:     c.Pending,
		domain.StatusSuspended:   c.Suspended,
		domain.StatusBlacklisted: c.Blacklisted,
		domain.StatusExited:      c.Exited,
		domain.StatusDeceased:    c.Deceased,
		domain.StatusRejected:    c.Rejected,
	}
}

// StatusSummaryTx is retained for callers that still want the legacy
// map shape. It now delegates to MemberStatusCountsTx so the bucket
// semantics live in exactly one place (the SQL function). The caller
// MUST already be inside a tenant-scoped tx.
func (s *StatusChangeStore) StatusSummaryTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (map[domain.MemberStatus]int, error) {
	counts, err := s.MemberStatusCountsTx(ctx, tx, tenantID)
	if err != nil {
		return nil, err
	}
	return counts.ByStatus(), nil
}

// RecentChangesTx returns the most recent status changes across the
// tenant for the dashboard "recent activity" panel.
type RecentChange struct {
	*domain.MemberStatusChange
	MemberNo string `json:"member_no"`
	FullName string `json:"full_name"`
}

func (s *StatusChangeStore) RecentChangesTx(ctx context.Context, tx pgx.Tx, limit int) ([]*RecentChange, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := tx.Query(ctx, `
		SELECT c.id, c.member_id, c.from_status, c.to_status, c.reason_category, COALESCE(c.reason_note,''),
		       c.supporting_doc_path, c.supporting_doc_mime, c.changed_by, c.changed_at,
		       c.workflow_instance_id, c.review_date,
		       m.member_no, m.full_name
		FROM member_status_changes c
		JOIN members m ON m.id = c.member_id
		ORDER BY c.changed_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RecentChange
	for rows.Next() {
		var c domain.MemberStatusChange
		var path, mime *string
		var memberNo, fullName string
		if err := rows.Scan(&c.ID, &c.MemberID, &c.FromStatus, &c.ToStatus, &c.ReasonCategory, &c.ReasonNote,
			&path, &mime, &c.ChangedBy, &c.ChangedAt, &c.WorkflowInstanceID, &c.ReviewDate,
			&memberNo, &fullName); err != nil {
			return nil, err
		}
		if path != nil { c.HasSupportingDoc = true }
		_ = mime
		out = append(out, &RecentChange{MemberStatusChange: &c, MemberNo: memberNo, FullName: fullName})
	}
	return out, rows.Err()
}

// Marshal helper so tests can dump a summary.
func (s *StatusChangeStore) MarshalSummary(summary map[domain.MemberStatus]int) ([]byte, error) {
	return json.Marshal(summary)
}
