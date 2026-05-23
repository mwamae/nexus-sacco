// Cross-store helpers that bridge the members.id ↔ counterparty.id
// translation. Created as part of Phase D sub-PR 2a to support the
// destructive cleanup that drops members.id-keyed columns on
// member_documents, member_relations, member_status_changes, and
// member_status_proposals (preamble-bridged via migration 0011).
//
// The savings service has its own copy of these helpers — keeping
// each service self-contained avoids a cross-service import for what
// amounts to two trivial single-row lookups.
//
// Sub-PR 2b will replace these with a kind-aware
// CounterpartyStore.GetTx that resolves directly to the entity
// (member or org) without going through the members table.

package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrCounterpartyNotFound surfaces when a bridge lookup misses.
// Stays local to the member package — the savings package defines
// its own ErrNotFound. Callers downstream of the resolve typically
// surface this as 404.
var ErrCounterpartyNotFound = errors.New("counterparty bridge not found")

// ResolveCounterpartyID looks up the counterparty_id that bridges
// the given members.id. Used inside store INSERT/UPDATE paths where
// the handler still receives a members.id from URL params but the
// destination column FKs counterparties(id).
func ResolveCounterpartyID(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (uuid.UUID, error) {
	var cpID uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM members WHERE id = $1`, memberID,
	).Scan(&cpID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrCounterpartyNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve counterparty for member %s: %w", memberID, err)
	}
	if cpID == uuid.Nil {
		return uuid.Nil, ErrCounterpartyNotFound
	}
	return cpID, nil
}

// (ResolveMemberIDFromCounterpartyID was removed in Phase D sub-PR
// 2b — its only call sites were rewired to MemberStore.GetByCounterpartyTx,
// which does the inverse lookup inline. No callers remain.)
