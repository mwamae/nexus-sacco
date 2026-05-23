// Cross-store helper for the Phase D Sub-PR 1 read-side switchover.
//
// Handlers in the savings service still receive `member_id` as URL
// params (the frontend hasn't been changed yet), but every read-side
// store function now expects a counterparty_id since that's the
// indexed column post-Phase C Tier 2. ResolveCounterpartyID is the
// one-line bridge: a single indexed lookup on members.counterparty_id
// (the column added by member migration 0007). Cost is one PK lookup;
// safe to call inside any tenant-scoped tx.
//
// Writes are intentionally NOT routed through here — they continue to
// pass member_id and let the BEFORE INSERT trigger
// populate_counterparty_id_from_member (savings migration 0018) fill
// in counterparty_id automatically. That dual-write contract is what
// makes the read switchover reversible: revert this sub-PR's SQL
// edits and reads go back to working off member_id.
//
// The guarantor variant exists because loan_guarantees uses
// guarantor_member_id instead of member_id, and its bridge column is
// guarantor_counterparty_id.

package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResolveMemberIDFromCounterpartyID is the inverse of ResolveCounterpartyID.
// Looks up the members.id whose bridge column equals the given
// counterparty.id. Used post-Phase D sub-PR 2a at handler sites that
// scan .MemberID from a counterparty_id column but still need a real
// members.id to call MemberStore.GetTx / TouchActivityTx / etc.
//
// Returns ErrNotFound when the counterparty has no matching member —
// typically because the counterparty is institutional (its bridge
// lives on org_members instead). Callers that mix individual +
// institutional flows should treat ErrNotFound as "this row belongs
// to an institutional counterparty" and route through OrgStore
// instead. Most savings callers were written assuming individual
// borrowers/depositors and predate institutional support, so an
// ErrNotFound here preserves their pre-Phase D behaviour (they would
// have hit pgx.ErrNoRows on the original members lookup anyway).
//
// Sub-PR 2b's job is to delete this helper by introducing a
// kind-aware CounterpartyStore.GetTx, at which point every consumer
// can switch from "resolve to member, look up member" to "look up
// counterparty directly" without the indirection.
func ResolveMemberIDFromCounterpartyID(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (uuid.UUID, error) {
	var memberID uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM members WHERE counterparty_id = $1`, cpID,
	).Scan(&memberID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve member for counterparty %s: %w", cpID, err)
	}
	if memberID == uuid.Nil {
		return uuid.Nil, ErrNotFound
	}
	return memberID, nil
}

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
