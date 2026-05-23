// member_relations covers both next-of-kin and beneficiaries (kind column).
// All writes are "replace the whole set" so the wizard's final submit can
// pass the full list without diffing.

package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/domain"
)

type RelationStore struct {
	pool *pgxpool.Pool
}

func NewRelationStore(pool *pgxpool.Pool) *RelationStore {
	return &RelationStore{pool: pool}
}

type RelationInput struct {
	Kind          domain.RelationKind
	FullName      string
	Relationship  string
	Phone         string
	Email         string
	IDDocNumber   string
	SharePercent  *float64
}

func (s *RelationStore) ReplaceTx(ctx context.Context, tx pgx.Tx, tenantID, memberID uuid.UUID, kind domain.RelationKind, rows []RelationInput) error {
	cpID, err := ResolveCounterpartyID(ctx, tx, memberID)
	if err != nil {
		return fmt.Errorf("resolve counterparty for relation replace: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM member_relations WHERE counterparty_id = $1 AND kind = $2`, cpID, kind); err != nil {
		return err
	}
	for i, r := range rows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO member_relations
			  (counterparty_id, tenant_id, kind, full_name, relationship, phone, email, id_doc_number, share_percent, position)
			VALUES
			  ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9, $10)
		`, cpID, tenantID, kind, r.FullName, r.Relationship, r.Phone, r.Email, r.IDDocNumber, r.SharePercent, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *RelationStore) ListForMemberTx(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) ([]*domain.Relation, error) {
	cpID, err := ResolveCounterpartyID(ctx, tx, memberID)
	if err != nil {
		return nil, fmt.Errorf("resolve counterparty for relation list: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT id, counterparty_id, kind, full_name, relationship,
		       COALESCE(phone,''), COALESCE(email,''), COALESCE(id_doc_number,''),
		       share_percent, position
		FROM member_relations WHERE counterparty_id = $1 ORDER BY kind, position
	`, cpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Relation
	for rows.Next() {
		var r domain.Relation
		if err := rows.Scan(&r.ID, &r.CounterpartyID, &r.Kind, &r.FullName, &r.Relationship,
			&r.Phone, &r.Email, &r.IDDocNumber, &r.SharePercent, &r.Position); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}
