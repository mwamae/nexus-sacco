// Role store — reads system + tenant-scoped roles, manages user→role
// assignments, expands a user's roles into the flat permission set the
// JWT carries.

package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/identity/internal/domain"
)

type RoleStore struct {
	pool *pgxpool.Pool
}

func NewRoleStore(pool *pgxpool.Pool) *RoleStore {
	return &RoleStore{pool: pool}
}

// SystemRoleByCode loads a system role (tenant_id IS NULL) by code,
// using a connection outside any tenant transaction.
func (s *RoleStore) SystemRoleByCode(ctx context.Context, code string) (*domain.Role, error) {
	var r domain.Role
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, code, name, COALESCE(description,''), is_system
		FROM roles WHERE tenant_id IS NULL AND code = $1
	`, code).Scan(&r.ID, &r.TenantID, &r.Code, &r.Name, &r.Description, &r.IsSystem)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// AssignTx grants a role to a user in the current tenant transaction.
func (s *RoleStore) AssignTx(ctx context.Context, tx pgx.Tx, userID, roleID uuid.UUID, grantedBy *uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id, granted_by)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, userID, roleID, grantedBy)
	return err
}

// RolesForUserTx returns the user's role codes (system + tenant).
func (s *RoleStore) RolesForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.code FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1
		ORDER BY r.code
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PermissionsForUserTx expands the user's roles into the deduplicated
// flat permission set carried by the access token.
func (s *RoleStore) PermissionsForUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT rp.permission_code
		FROM user_roles ur
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1
		ORDER BY rp.permission_code
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListVisibleTx returns roles available to assign within a tenant:
// system roles + the tenant's own roles. Excludes platform_admin.
func (s *RoleStore) ListVisibleTx(ctx context.Context, tx pgx.Tx) ([]*domain.Role, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, code, name, COALESCE(description,''), is_system
		FROM roles
		WHERE (tenant_id IS NULL AND code <> 'platform_admin')
		   OR tenant_id = current_tenant_id()
		ORDER BY tenant_id NULLS FIRST, code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Role
	for rows.Next() {
		var r domain.Role
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Code, &r.Name, &r.Description, &r.IsSystem); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
