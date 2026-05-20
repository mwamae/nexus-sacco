// Permission store — read-only catalog of the global permission codes.
// Codes are developer-defined (they correspond to actual RequirePermission
// checks in handlers), so no insert/update/delete is exposed.

package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/identity/internal/domain"
)

type PermissionStore struct {
	pool *pgxpool.Pool
}

func NewPermissionStore(pool *pgxpool.Pool) *PermissionStore {
	return &PermissionStore{pool: pool}
}

// List returns every permission in the catalog, ordered by category then code.
func (s *PermissionStore) List(ctx context.Context) ([]*domain.Permission, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT code, description, category
		FROM permissions
		ORDER BY category, code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Permission
	for rows.Next() {
		var p domain.Permission
		if err := rows.Scan(&p.Code, &p.Description, &p.Category); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}
