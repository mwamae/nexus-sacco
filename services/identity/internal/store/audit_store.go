// Audit-log writes. Best-effort: callers log if Insert fails but never block.

package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditStore struct {
	pool *pgxpool.Pool
}

func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{pool: pool}
}

type AuditEntry struct {
	TenantID   *uuid.UUID
	ActorID    *uuid.UUID
	Action     string
	TargetKind string
	TargetID   string
	IP         string
	UserAgent  string
	Metadata   map[string]any
}

// AuditEntryRead is the read-shape returned by ByTarget.
type AuditEntryRead struct {
	ID         int64          `json:"id"`
	TenantID   *uuid.UUID     `json:"tenant_id,omitempty"`
	ActorID    *uuid.UUID     `json:"actor_id,omitempty"`
	Action     string         `json:"action"`
	TargetKind string         `json:"target_kind,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	IP         string         `json:"ip,omitempty"`
	UserAgent  string         `json:"user_agent,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// ByTarget returns audit entries for (targetKind, targetID), newest first.
// Tenant filtering: when tenantID is non-nil we constrain to that tenant
// so a tenant owner can only see their own tenant's events. Platform
// admins pass uuid.Nil to skip the filter.
func (s *AuditStore) ByTarget(ctx context.Context, tenantID *uuid.UUID, targetKind, targetID string, limit int) ([]*AuditEntryRead, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{targetKind, targetID, limit}
	q := `
		SELECT id, tenant_id, actor_id, action, COALESCE(target_kind,''), COALESCE(target_id,''),
		       COALESCE(host(ip),''), COALESCE(user_agent,''), metadata, created_at
		FROM audit_log
		WHERE target_kind = $1 AND target_id = $2
	`
	if tenantID != nil {
		q += " AND tenant_id = $4"
		args = append(args, *tenantID)
	}
	q += " ORDER BY created_at DESC LIMIT $3"

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AuditEntryRead
	for rows.Next() {
		var e AuditEntryRead
		var meta []byte
		if err := rows.Scan(&e.ID, &e.TenantID, &e.ActorID, &e.Action, &e.TargetKind, &e.TargetID,
			&e.IP, &e.UserAgent, &meta, &e.CreatedAt); err != nil {
			return nil, err
		}
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &e.Metadata)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// Write inserts an audit entry on the shared pool (no tenant tx).
// audit_log has no RLS — tenant_id is just a column.
func (s *AuditStore) Write(ctx context.Context, e AuditEntry) error {
	meta, err := json.Marshal(e.Metadata)
	if err != nil {
		meta = []byte("{}")
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, actor_id, action, target_kind, target_id, ip, user_agent, metadata)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,'')::inet, NULLIF($7,''), $8)
	`, e.TenantID, e.ActorID, e.Action, e.TargetKind, e.TargetID, e.IP, e.UserAgent, meta)
	return err
}
