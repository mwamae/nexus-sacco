// Workflow instance store. Per-level state lives in a jsonb array on
// the wf_instances row, so a single SELECT loads the whole instance
// and a single UPDATE persists a state transition.

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

	"github.com/nexussacco/workflow/internal/domain"
)

type InstanceStore struct {
	pool *pgxpool.Pool
}

func NewInstanceStore(pool *pgxpool.Pool) *InstanceStore {
	return &InstanceStore{pool: pool}
}

type CreateInstanceInput struct {
	TenantID       uuid.UUID
	DefinitionID   uuid.UUID
	ProcessKind    string
	SubjectKind    string
	SubjectID      uuid.UUID
	Context        map[string]any
	CallbackURL    string
	CallbackSecret string
	InitiatorID    *uuid.UUID
	LevelsSnapshot []domain.LevelState // already-resolved (conditional levels marked skipped)
	StartingLevel  int
	StartingStatus domain.Status
	// Unified Inbox additions:
	Summary     string     // one-line description for the Inbox row
	SourceURL   string     // deep-link back to the creating page
	SLABreachAt *time.Time // active-level SLA mirror at creation time
}

func (s *InstanceStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateInstanceInput) (*domain.Instance, error) {
	ctxBytes, _ := json.Marshal(in.Context)
	lvlsBytes, _ := json.Marshal(in.LevelsSnapshot)
	var i domain.Instance
	err := tx.QueryRow(ctx, `
		INSERT INTO wf_instances (
		  tenant_id, definition_id, process_kind, subject_kind, subject_id,
		  status, current_level, context, callback_url, callback_secret,
		  initiator_id, levels, summary, source_url, sla_breach_at
		) VALUES (
		  $1, $2, $3, $4, $5,
		  $6, $7, $8, NULLIF($9,''), NULLIF($10,''),
		  $11, $12, NULLIF($13,''), NULLIF($14,''), $15
		)
		RETURNING `+instanceSelectCols+`
	`, in.TenantID, in.DefinitionID, in.ProcessKind, in.SubjectKind, in.SubjectID,
		in.StartingStatus, in.StartingLevel, ctxBytes, in.CallbackURL, in.CallbackSecret,
		in.InitiatorID, lvlsBytes, in.Summary, in.SourceURL, in.SLABreachAt,
	).Scan(instanceScanDests(&i)...)
	if err != nil {
		return nil, fmt.Errorf("insert instance: %w", err)
	}
	return &i, nil
}

func (s *InstanceStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Instance, error) {
	var i domain.Instance
	err := tx.QueryRow(ctx, `SELECT `+instanceSelectCols+` FROM wf_instances WHERE id = $1`, id).
		Scan(instanceScanDests(&i)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

type ListInstancesInput struct {
	Status      domain.Status
	ProcessKind string
	SubjectKind string
	SubjectID   *uuid.UUID
	Limit       int
	Offset      int
}

type ListInstancesResult struct {
	Instances []*domain.Instance `json:"instances"`
	Total     int                `json:"total"`
}

func (s *InstanceStore) ListTx(ctx context.Context, tx pgx.Tx, in ListInstancesInput) (*ListInstancesResult, error) {
	args := []any{}
	where := []string{}
	if in.Status != "" {
		where = append(where, fmt.Sprintf("status = $%d", len(args)+1))
		args = append(args, in.Status)
	}
	if in.ProcessKind != "" {
		where = append(where, fmt.Sprintf("process_kind = $%d", len(args)+1))
		args = append(args, in.ProcessKind)
	}
	if in.SubjectKind != "" {
		where = append(where, fmt.Sprintf("subject_kind = $%d", len(args)+1))
		args = append(args, in.SubjectKind)
	}
	if in.SubjectID != nil {
		where = append(where, fmt.Sprintf("subject_id = $%d", len(args)+1))
		args = append(args, *in.SubjectID)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM wf_instances"+whereSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := tx.Query(ctx, `SELECT `+instanceSelectCols+` FROM wf_instances`+whereSQL+
		fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &ListInstancesResult{Total: total}
	for rows.Next() {
		var i domain.Instance
		if err := rows.Scan(instanceScanDests(&i)...); err != nil {
			return nil, err
		}
		out.Instances = append(out.Instances, &i)
	}
	return out, rows.Err()
}

// UpdateProgressTx persists the per-level array + top-level status/current_level/completed_at.
// Also mirrors the active level's sla_due_at to the indexed sla_breach_at
// column, and releases the claim if the instance becomes terminal.
func (s *InstanceStore) UpdateProgressTx(ctx context.Context, tx pgx.Tx, i *domain.Instance) error {
	lvlsBytes, err := json.Marshal(i.Levels)
	if err != nil {
		return err
	}
	breachAt := activeLevelSLA(i)
	terminal := isTerminal(i.Status)
	_, err = tx.Exec(ctx, `
		UPDATE wf_instances
		SET status         = $2,
		    current_level  = $3,
		    levels         = $4,
		    completed_at   = $5,
		    sla_breach_at  = $6,
		    -- terminal status clears any outstanding claim so the
		    -- Inbox doesn't show ghost locks.
		    claimed_by     = CASE WHEN $7 THEN NULL ELSE claimed_by    END,
		    claimed_at     = CASE WHEN $7 THEN NULL ELSE claimed_at    END,
		    claim_expires  = CASE WHEN $7 THEN NULL ELSE claim_expires END
		WHERE id = $1
	`, i.ID, i.Status, i.CurrentLevel, lvlsBytes, i.CompletedAt, breachAt, terminal)
	return err
}

// activeLevelSLA returns the SLA-due timestamp of the current
// (in-progress / awaiting_info / returned) level, if any. NULL means
// no active SLA — either the instance is terminal or the current
// level has no sla_hours configured.
func activeLevelSLA(i *domain.Instance) *time.Time {
	if isTerminal(i.Status) {
		return nil
	}
	if i.CurrentLevel < 0 || i.CurrentLevel >= len(i.Levels) {
		return nil
	}
	return i.Levels[i.CurrentLevel].SLADueAt
}

func isTerminal(s domain.Status) bool {
	switch s {
	case domain.StatusApproved, domain.StatusRejected, domain.StatusCancelled, domain.StatusExpired:
		return true
	}
	return false
}

// ─────────── Claim / release ───────────

// ErrClaimContested is returned when a claim attempt finds the instance
// already locked by another user whose claim hasn't expired.
var ErrClaimContested = errors.New("workflow: instance already claimed")

// ClaimTx atomically takes a lock on the instance for the given user
// for ttl duration. If another user holds an unexpired lock,
// returns ErrClaimContested. Idempotent: same user re-claiming
// extends the expiry to now+ttl.
func (s *InstanceStore) ClaimTx(ctx context.Context, tx pgx.Tx, id, userID uuid.UUID, ttl time.Duration) (*domain.Instance, error) {
	now := time.Now().UTC()
	expires := now.Add(ttl)
	// Atomic update — only succeeds if no live claim by a different user.
	tag, err := tx.Exec(ctx, `
		UPDATE wf_instances
		SET claimed_by    = $2,
		    claimed_at    = $3,
		    claim_expires = $4
		WHERE id = $1
		  AND (claimed_by IS NULL
		       OR claimed_by = $2
		       OR claim_expires < $3)
	`, id, userID, now, expires)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		// Either the instance doesn't exist or someone else holds it.
		// Differentiate so the handler can pick 404 vs 409.
		var holder uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT claimed_by FROM wf_instances WHERE id = $1`, id).Scan(&holder); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		return nil, ErrClaimContested
	}
	return s.ByIDTx(ctx, tx, id)
}

// ReleaseTx clears the claim. Caller must be the current claimant
// OR have a privileged override (the handler decides).
func (s *InstanceStore) ReleaseTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE wf_instances
		SET claimed_by = NULL, claimed_at = NULL, claim_expires = NULL
		WHERE id = $1
	`, id)
	return err
}

func (s *InstanceStore) SetCallbackStatusTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, status string, delivered *time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE wf_instances
		SET callback_status = $2, callback_delivered_at = $3
		WHERE id = $1
	`, id, status, delivered)
	return err
}

// MarkCallbackPendingTx flags a row for the callback-dispatcher to pick
// up on its next poll. Idempotent — re-marking a row that's already
// pending or in-flight does nothing. A no-op when the instance has no
// callback_url (the original Create didn't ask for delivery).
//
// Resets the attempt counter + next-attempt clock so a re-mark after
// a previous failure restarts the backoff from zero. The dispatcher
// uses this in two cases today: when the original Action transition
// is terminal, and (future) when an operator clicks "Retry callback"
// on a DLQ row.
func (s *InstanceStore) MarkCallbackPendingTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE wf_instances
		   SET callback_status        = 'pending',
		       callback_attempts      = 0,
		       callback_next_attempt_at = NULL,
		       callback_last_error    = NULL
		 WHERE id = $1
		   AND callback_url IS NOT NULL
	`, id)
	return err
}

// Dashboard counts.
type DashboardCounts struct {
	Total       int                       `json:"total"`
	ByStatus    map[domain.Status]int     `json:"by_status"`
	ByProcess   map[string]int            `json:"by_process_kind"`
	BreachCount int                       `json:"sla_breach_count"`
	// Average turnaround in seconds across approved+rejected.
	AvgTATSeconds float64 `json:"avg_tat_seconds"`
}

func (s *InstanceStore) DashboardTx(ctx context.Context, tx pgx.Tx) (*DashboardCounts, error) {
	d := &DashboardCounts{
		ByStatus:  map[domain.Status]int{},
		ByProcess: map[string]int{},
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM wf_instances`).Scan(&total); err != nil {
		return nil, err
	}
	d.Total = total

	statusRows, err := tx.Query(ctx, `SELECT status::text, count(*) FROM wf_instances GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var s string
		var c int
		if err := statusRows.Scan(&s, &c); err != nil {
			return nil, err
		}
		d.ByStatus[domain.Status(s)] = c
	}

	procRows, err := tx.Query(ctx, `SELECT process_kind, count(*) FROM wf_instances GROUP BY process_kind`)
	if err != nil {
		return nil, err
	}
	defer procRows.Close()
	for procRows.Next() {
		var p string
		var c int
		if err := procRows.Scan(&p, &c); err != nil {
			return nil, err
		}
		d.ByProcess[p] = c
	}

	// Average TAT (completed_at - started_at) in seconds, terminal states only.
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(EXTRACT(epoch FROM AVG(completed_at - started_at)), 0)::float8
		FROM wf_instances
		WHERE status IN ('approved','rejected','cancelled','expired') AND completed_at IS NOT NULL
	`).Scan(&d.AvgTATSeconds); err != nil {
		return nil, err
	}

	// SLA breach count = number of in-progress levels where now() > sla_due_at.
	// We compute this from the levels jsonb. Cheap on small tenants; if it
	// becomes hot we'll add a partial index.
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM wf_instances i,
		       jsonb_array_elements(i.levels) lvl
		WHERE i.status IN ('pending','in_progress','awaiting_info','escalated')
		  AND (lvl ->> 'status') IN ('in_progress','escalated')
		  AND (lvl ->> 'sla_due_at') IS NOT NULL
		  AND (lvl ->> 'sla_due_at')::timestamptz < now()
	`).Scan(&d.BreachCount); err != nil {
		return nil, err
	}

	return d, nil
}

// ─────────── Action audit ───────────

type ActionStore struct {
	pool *pgxpool.Pool
}

func NewActionStore(pool *pgxpool.Pool) *ActionStore {
	return &ActionStore{pool: pool}
}

type CreateActionInput struct {
	TenantID   uuid.UUID
	InstanceID uuid.UUID
	LevelOrder *int
	Action     domain.ActionKind
	ActorID    *uuid.UUID
	ActorRole  string
	Comments   string
	Metadata   map[string]any
}

func (s *ActionStore) WriteTx(ctx context.Context, tx pgx.Tx, in CreateActionInput) (*domain.Action, error) {
	meta, _ := json.Marshal(in.Metadata)
	var a domain.Action
	var metaBytes []byte
	err := tx.QueryRow(ctx, `
		INSERT INTO wf_actions (tenant_id, instance_id, level_order, action, actor_id, actor_role, comments, metadata)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), $8)
		RETURNING id, instance_id, level_order, action, actor_id, COALESCE(actor_role,''), COALESCE(comments,''), metadata, created_at
	`, in.TenantID, in.InstanceID, in.LevelOrder, in.Action, in.ActorID, in.ActorRole, in.Comments, meta).
		Scan(&a.ID, &a.InstanceID, &a.LevelOrder, &a.Action, &a.ActorID, &a.ActorRole, &a.Comments, &metaBytes, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	if len(metaBytes) > 0 {
		var m any
		_ = json.Unmarshal(metaBytes, &m)
		a.Metadata = m
	}
	return &a, nil
}

func (s *ActionStore) ListForInstanceTx(ctx context.Context, tx pgx.Tx, instanceID uuid.UUID) ([]*domain.Action, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, instance_id, level_order, action, actor_id, COALESCE(actor_role,''), COALESCE(comments,''), metadata, created_at
		FROM wf_actions WHERE instance_id = $1 ORDER BY created_at
	`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Action
	for rows.Next() {
		var a domain.Action
		var metaBytes []byte
		if err := rows.Scan(&a.ID, &a.InstanceID, &a.LevelOrder, &a.Action, &a.ActorID, &a.ActorRole, &a.Comments, &metaBytes, &a.CreatedAt); err != nil {
			return nil, err
		}
		if len(metaBytes) > 0 {
			var m any
			_ = json.Unmarshal(metaBytes, &m)
			a.Metadata = m
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ─────────── helpers ───────────

const instanceSelectCols = `
id, tenant_id, definition_id, process_kind, subject_kind, subject_id,
status, current_level, context, COALESCE(callback_url,''),
COALESCE(callback_status,''), callback_delivered_at,
initiator_id, started_at, completed_at, levels,
COALESCE(summary,''), COALESCE(source_url,''),
claimed_by, claimed_at, claim_expires, sla_breach_at`

func instanceScanDests(i *domain.Instance) []any {
	return []any{
		&i.ID, &i.TenantID, &i.DefinitionID, &i.ProcessKind, &i.SubjectKind, &i.SubjectID,
		&i.Status, &i.CurrentLevel, jsonScanner(&i.Context), &i.CallbackURL,
		&i.CallbackStatus, &i.CallbackDeliveredAt,
		&i.InitiatorID, &i.StartedAt, &i.CompletedAt, levelsScanner(&i.Levels),
		&i.Summary, &i.SourceURL,
		&i.ClaimedBy, &i.ClaimedAt, &i.ClaimExpires, &i.SLABreachAt,
	}
}

// Generic JSONB → any scanner.
type jsonAnyScanner struct{ dst *any }

func (s jsonAnyScanner) Scan(src any) error {
	if src == nil {
		*s.dst = nil
		return nil
	}
	b, ok := src.([]byte)
	if !ok {
		if str, ok := src.(string); ok {
			b = []byte(str)
		} else {
			return fmt.Errorf("jsonAnyScanner: unsupported scan type %T", src)
		}
	}
	if len(b) == 0 {
		*s.dst = nil
		return nil
	}
	return json.Unmarshal(b, s.dst)
}

func jsonScanner(dst *any) any { return jsonAnyScanner{dst: dst} }

type levelsAnyScanner struct{ dst *[]domain.LevelState }

func (s levelsAnyScanner) Scan(src any) error {
	if src == nil {
		*s.dst = nil
		return nil
	}
	b, ok := src.([]byte)
	if !ok {
		if str, ok := src.(string); ok {
			b = []byte(str)
		} else {
			return fmt.Errorf("levelsScanner: unsupported scan type %T", src)
		}
	}
	if len(b) == 0 {
		*s.dst = nil
		return nil
	}
	return json.Unmarshal(b, s.dst)
}

func levelsScanner(dst *[]domain.LevelState) any { return levelsAnyScanner{dst: dst} }
