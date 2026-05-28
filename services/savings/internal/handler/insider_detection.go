// Loans Phase 5 — insider detection.
//
// Maps a borrower counterparty → (is_insider, category) by matching
// the member's email/phone against the identity service's users table
// and inspecting their roles.
//
// Categories per CBK Prudential Guideline §10:
//   staff             — any system user who isn't a plain Member
//   board             — placeholder for future board_member role
//   committee         — placeholder for future committee_member role
//   spouse_of_insider — declared on the application; not auto-detected
//   related_party     — declared on the application; not auto-detected
//
// Current detection covers `staff` only. Board / committee additions
// land when those roles are seeded; until then the application UI
// captures them via an explicit declaration flow (deferred).

package handler

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// detectInsiderForCounterpartyTx returns the insider classification
// for a counterparty. Returns (false, "", nil) if not detected as an
// insider — callers should leave is_insider=false in that case.
func detectInsiderForCounterpartyTx(
	ctx context.Context, tx pgx.Tx, counterpartyID uuid.UUID,
) (bool, string, error) {
	// Resolve the member's email + phone from the directory view.
	var memberID *uuid.UUID
	var email, phone *string
	err := tx.QueryRow(ctx, `
		SELECT cd.member_id, m.email::text, m.phone
		  FROM counterparty_directory cd
		  LEFT JOIN members m ON m.id = cd.member_id
		 WHERE cd.counterparty_id = $1
	`, counterpartyID).Scan(&memberID, &email, &phone)
	if err == pgx.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if memberID == nil {
		// Institutional counterparty (group loan) — Phase 5 doesn't
		// auto-flag groups as insiders; if a group needs the flag,
		// the application creator passes is_insider explicitly.
		return false, "", nil
	}

	// Match against users.email / users.phone. The user must belong to
	// the same tenant (RLS gives us that for free since we're in a
	// per-tenant tx). Inspect roles; any role other than 'Member'
	// (role_id ending in '...000a') flags the user as staff.
	args := []any{}
	conds := ""
	if email != nil && *email != "" {
		conds = "u.email = $1"
		args = append(args, *email)
	}
	if phone != nil && *phone != "" {
		if conds != "" {
			conds += " OR u.phone = $2"
		} else {
			conds = "u.phone = $1"
		}
		args = append(args, *phone)
	}
	if conds == "" {
		return false, "", nil // no email/phone — can't match
	}

	query := `
		SELECT EXISTS (
		  SELECT 1
		    FROM users u
		    JOIN user_roles ur ON ur.user_id = u.id
		   WHERE (` + conds + `)
		     AND ur.role_id <> '00000000-0000-0000-0000-00000000000a'::uuid
		     AND u.status::text = 'active'
		)
	`
	var hasStaffRole bool
	if err := tx.QueryRow(ctx, query, args...).Scan(&hasStaffRole); err != nil {
		return false, "", err
	}
	if !hasStaffRole {
		return false, "", nil
	}
	return true, "staff", nil
}
