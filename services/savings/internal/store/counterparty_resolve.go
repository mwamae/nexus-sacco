// Cross-store helper for the bridge from members.id (handed in by
// handlers from URL params) to counterparty.id (the FK column on every
// post-Phase D savings table). Stores call this at INSERT boundaries
// so they accept the historic members.id input but write the new
// counterparty_id column.
//
// Phase D sub-PR 2b removed the inverse helper
// (ResolveMemberIDFromCounterpartyID) — its only caller (handler-side
// h.Members.GetTx(.MemberID)) was replaced with
// h.Members.GetByCounterpartyTx(.CounterpartyID), which does the
// inverse lookup inline and removes the indirection.
//
// Sub-PR 3 will drop the remaining forward bridge by renaming the URL
// routes /by-member/{member_id} → /by-counterparty/{counterparty_id}
// and updating the frontend to send counterparty.ids directly.

package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResolveCounterpartyID looks up the counterparty_id that bridges
// the given members.id. Returns ErrNotFound if the member doesn't
// exist (or hasn't been backfilled with a counterparty_id, which
// shouldn't happen post-Phase A but is worth surfacing distinctly).
func ResolveCounterpartyID(ctx context.Context, tx pgx.Tx, memberID uuid.UUID) (uuid.UUID, error) {
	var cpID uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM members WHERE id = $1`, memberID,
	).Scan(&cpID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve counterparty for member %s: %w", memberID, err)
	}
	if cpID == uuid.Nil {
		// The bridge column exists but is null — means the member
		// pre-dates the backfill, which shouldn't happen on any
		// tenant that ran migration 0008. Treat as not-found so
		// the caller gets a clean 404 rather than a panic.
		return uuid.Nil, ErrNotFound
	}
	return cpID, nil
}
