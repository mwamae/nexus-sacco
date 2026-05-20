// Role store — reads system + tenant-scoped roles, manages user→role
// assignments, expands a user's roles into the flat permission set the
// JWT carries.

package store

import (
	"context"
	"errors"
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

// ListVisibleWithPermissionsTx is ListVisibleTx + each role's permission
// codes populated. Two queries; cheap because the role set is small.
func (s *RoleStore) ListVisibleWithPermissionsTx(ctx context.Context, tx pgx.Tx) ([]*domain.Role, error) {
	roles, err := s.ListVisibleTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		return roles, nil
	}
	ids := make([]uuid.UUID, 0, len(roles))
	byID := make(map[uuid.UUID]*domain.Role, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID)
		byID[r.ID] = r
		r.Permissions = []string{} // ensure non-nil JSON array
	}
	rows, err := tx.Query(ctx, `
		SELECT role_id, permission_code FROM role_permissions WHERE role_id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid uuid.UUID
		var code string
		if err := rows.Scan(&rid, &code); err != nil {
			return nil, err
		}
		if r, ok := byID[rid]; ok {
			r.Permissions = append(r.Permissions, code)
		}
	}
	return roles, rows.Err()
}

// ByIDWithPermissionsTx loads a single role (system or tenant-custom)
// plus its permission codes.
func (s *RoleStore) ByIDWithPermissionsTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Role, error) {
	var r domain.Role
	err := tx.QueryRow(ctx, `
		SELECT id, tenant_id, code, name, COALESCE(description,''), is_system
		FROM roles WHERE id = $1
	`, id).Scan(&r.ID, &r.TenantID, &r.Code, &r.Name, &r.Description, &r.IsSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT permission_code FROM role_permissions WHERE role_id = $1 ORDER BY permission_code`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	r.Permissions = []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		r.Permissions = append(r.Permissions, c)
	}
	return &r, rows.Err()
}

type CreateRoleInput struct {
	TenantID    uuid.UUID
	Code        string
	Name        string
	Description string
}

// CreateCustomTx inserts a tenant-scoped role (is_system=false).
func (s *RoleStore) CreateCustomTx(ctx context.Context, tx pgx.Tx, in CreateRoleInput) (*domain.Role, error) {
	var r domain.Role
	err := tx.QueryRow(ctx, `
		INSERT INTO roles (tenant_id, code, name, description, is_system)
		VALUES ($1, $2, $3, NULLIF($4,''), false)
		RETURNING id, tenant_id, code, name, COALESCE(description,''), is_system
	`, in.TenantID, in.Code, in.Name, in.Description).
		Scan(&r.ID, &r.TenantID, &r.Code, &r.Name, &r.Description, &r.IsSystem)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateMetaTx changes a custom role's name + description.
func (s *RoleStore) UpdateMetaTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, name, description string) error {
	_, err := tx.Exec(ctx, `
		UPDATE roles SET name = $2, description = NULLIF($3,'')
		WHERE id = $1 AND is_system = false
	`, id, name, description)
	return err
}

// SetPermissionsTx replaces a role's permission set with the given codes.
// Idempotent. Pass an empty slice to clear.
func (s *RoleStore) SetPermissionsTx(ctx context.Context, tx pgx.Tx, roleID uuid.UUID, codes []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, roleID); err != nil {
		return err
	}
	if len(codes) == 0 {
		return nil
	}
	// Deduplicate before insert.
	seen := make(map[string]struct{}, len(codes))
	clean := make([]string, 0, len(codes))
	for _, c := range codes {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		clean = append(clean, c)
	}
	batch := &pgx.Batch{}
	for _, c := range clean {
		batch.Queue(`INSERT INTO role_permissions (role_id, permission_code) VALUES ($1, $2)`, roleID, c)
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for range clean {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTx deletes a custom role. Refuses if is_system.
func (s *RoleStore) DeleteTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `DELETE FROM roles WHERE id = $1 AND is_system = false`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UnassignTx removes a role grant from a user.
func (s *RoleStore) UnassignTx(ctx context.Context, tx pgx.Tx, userID, roleID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1 AND role_id = $2`, userID, roleID)
	return err
}

// RolesForUserDetailedTx returns the user's role objects (system + tenant),
// with permission codes left empty — callers that need permissions should
// use PermissionsForUserTx separately.
func (s *RoleStore) RolesForUserDetailedTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]*domain.Role, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id, r.tenant_id, r.code, r.name, COALESCE(r.description,''), r.is_system
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1
		ORDER BY r.is_system DESC, r.code
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Role
	for rows.Next() {
		var r domain.Role
		if err := rows.Scan(&r.ID, &r.TenantID, &r.Code, &r.Name, &r.Description, &r.IsSystem); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
