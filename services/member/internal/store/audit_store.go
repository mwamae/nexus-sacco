// Audit-log writes. Best-effort: callers log if Insert fails but never block.
// Writes to the shared audit_log table owned by identity. The table has
// no RLS so we don't need a tenant tx.

package store

import (
	"context"
	"encoding/json"

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
