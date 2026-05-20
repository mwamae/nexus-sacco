// Workflow definition store. Definitions are versioned: editing an
// existing definition creates a new version (the previous version is
// kept around so running instances still see the shape they were started
// against). At most one row per (tenant, process_kind) is active.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/workflow/internal/domain"
)

type DefinitionStore struct {
	pool *pgxpool.Pool
}

func NewDefinitionStore(pool *pgxpool.Pool) *DefinitionStore {
	return &DefinitionStore{pool: pool}
}

type CreateDefinitionInput struct {
	TenantID    uuid.UUID
	ProcessKind string
	Name        string
	Description string
	Levels      []domain.LevelDef
	CreatedBy   *uuid.UUID
	// Active toggles whether the new version becomes the live one
	// (deactivating any predecessor). Defaults to true.
	Active bool
}

// CreateTx inserts a definition + its levels. If another active
// definition exists for the same (tenant, process_kind), it is
// deactivated.
func (s *DefinitionStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateDefinitionInput) (*domain.Definition, error) {
	// Bump version above any existing.
	var maxVersion int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0)
		FROM wf_definitions WHERE tenant_id = $1 AND process_kind = $2
	`, in.TenantID, in.ProcessKind).Scan(&maxVersion); err != nil {
		return nil, err
	}
	nextVersion := maxVersion + 1

	if in.Active {
		// Deactivate any other active.
		if _, err := tx.Exec(ctx, `
			UPDATE wf_definitions SET active = false
			WHERE tenant_id = $1 AND process_kind = $2 AND active = true
		`, in.TenantID, in.ProcessKind); err != nil {
			return nil, err
		}
	}

	var d domain.Definition
	err := tx.QueryRow(ctx, `
		INSERT INTO wf_definitions (tenant_id, process_kind, name, description, version, active, created_by)
		VALUES ($1, $2, $3, NULLIF($4,''), $5, $6, $7)
		RETURNING id, tenant_id, process_kind, name, COALESCE(description,''), version, active, created_at, updated_at, created_by
	`, in.TenantID, in.ProcessKind, in.Name, in.Description, nextVersion, in.Active, in.CreatedBy).
		Scan(&d.ID, &d.TenantID, &d.ProcessKind, &d.Name, &d.Description, &d.Version, &d.Active, &d.CreatedAt, &d.UpdatedAt, &d.CreatedBy)
	if err != nil {
		return nil, fmt.Errorf("insert definition: %w", err)
	}

	for i, lvl := range in.Levels {
		var condBytes []byte
		if lvl.ConditionExpr != nil {
			condBytes, err = json.Marshal(lvl.ConditionExpr)
			if err != nil {
				return nil, fmt.Errorf("marshal condition level %d: %w", i, err)
			}
		}
		quorum := lvl.Quorum
		if quorum == "" {
			quorum = domain.QuorumAnyOne
		}
		// Postgres' text[]/uuid[] columns map a Go nil slice to SQL NULL
		// even when the column has DEFAULT '{}'. Normalise so the engine
		// always stores well-formed arrays.
		roles := lvl.ApproverRoles
		if roles == nil {
			roles = []string{}
		}
		users := lvl.ApproverUserIDs
		if users == nil {
			users = []uuid.UUID{}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO wf_levels (
			  definition_id, tenant_id, level_order, name,
			  approver_roles, approver_user_ids, quorum,
			  condition_expr, sla_hours, escalation_role, escalation_user_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10,''), $11)
		`, d.ID, in.TenantID, i, lvl.Name,
			roles, users, quorum,
			condBytes, lvl.SLAHours, lvl.EscalationRole, lvl.EscalationUser,
		); err != nil {
			return nil, fmt.Errorf("insert level %d: %w", i, err)
		}
		lvl.LevelOrder = i
		d.Levels = append(d.Levels, lvl)
	}
	return &d, nil
}

func (s *DefinitionStore) SetActiveTx(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, active bool) error {
	if active {
		var processKind string
		err := tx.QueryRow(ctx, `SELECT process_kind FROM wf_definitions WHERE id = $1`, id).Scan(&processKind)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE wf_definitions SET active = false
			WHERE tenant_id = $1 AND process_kind = $2 AND active = true AND id <> $3
		`, tenantID, processKind, id); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE wf_definitions SET active = $2 WHERE id = $1`, id, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DefinitionStore) ByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Definition, error) {
	d := &domain.Definition{}
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, process_kind, name, COALESCE(description,''), version, active,
		       created_at, updated_at, created_by
		FROM wf_definitions WHERE id = $1
	`, id).Scan(&d.ID, &d.TenantID, &d.ProcessKind, &d.Name, &d.Description, &d.Version, &d.Active,
		&d.CreatedAt, &d.UpdatedAt, &d.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	levels, err := s.LevelsForTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	d.Levels = levels
	return d, nil
}

// ActiveByKindTx returns the live definition for a (tenant, process_kind).
func (s *DefinitionStore) ActiveByKindTx(ctx context.Context, tx pgx.Tx, processKind string) (*domain.Definition, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM wf_definitions
		WHERE process_kind = $1 AND active = true
	`, processKind).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.ByIDTx(ctx, tx, id)
}

func (s *DefinitionStore) LevelsForTx(ctx context.Context, tx pgx.Tx, defID uuid.UUID) ([]domain.LevelDef, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, level_order, name, approver_roles, approver_user_ids,
		       quorum, condition_expr, sla_hours,
		       COALESCE(escalation_role,''), escalation_user_id
		FROM wf_levels WHERE definition_id = $1 ORDER BY level_order
	`, defID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LevelDef
	for rows.Next() {
		var l domain.LevelDef
		var condBytes []byte
		if err := rows.Scan(&l.ID, &l.LevelOrder, &l.Name,
			&l.ApproverRoles, &l.ApproverUserIDs, &l.Quorum,
			&condBytes, &l.SLAHours, &l.EscalationRole, &l.EscalationUser); err != nil {
			return nil, err
		}
		if len(condBytes) > 0 {
			var cond any
			if err := json.Unmarshal(condBytes, &cond); err == nil {
				l.ConditionExpr = cond
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type ListDefsInput struct {
	ProcessKind string
	OnlyActive  bool
	Limit       int
	Offset      int
}

func (s *DefinitionStore) ListTx(ctx context.Context, tx pgx.Tx, in ListDefsInput) ([]*domain.Definition, error) {
	args := []any{}
	where := ""
	if in.ProcessKind != "" {
		args = append(args, in.ProcessKind)
		where += fmt.Sprintf(" AND process_kind = $%d", len(args))
	}
	if in.OnlyActive {
		where += " AND active = true"
	}
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, process_kind, name, COALESCE(description,''), version, active,
		       created_at, updated_at, created_by
		FROM wf_definitions WHERE 1=1`+where+`
		ORDER BY process_kind, version DESC
		LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Definition
	for rows.Next() {
		d := &domain.Definition{}
		if err := rows.Scan(&d.ID, &d.TenantID, &d.ProcessKind, &d.Name, &d.Description, &d.Version, &d.Active,
			&d.CreatedAt, &d.UpdatedAt, &d.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Attach levels per definition (cheap — definitions are small).
	for _, d := range out {
		levels, err := s.LevelsForTx(ctx, tx, d.ID)
		if err != nil {
			return nil, err
		}
		d.Levels = levels
	}
	return out, nil
}

