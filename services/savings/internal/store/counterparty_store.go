// Read-only access to the unified counterparties register. The
// savings service touches counterparties for one purpose: looking up
// the kind-aware "principal" view (name, status, phone, email) when
// it needs to display owner info or address a notification.
//
// Before Phase E B the savings handlers used MemberStore.GetByCounterpartyTx
// for the same purpose, which 404'd on institutional counterparties
// (they have no members row). CounterpartyStore.GetByIDTx replaces
// that — it returns a unified view for both individuals (sourced via
// members) and institutionals (sourced via org_members), so every
// downstream handler works for both kinds without branching.

package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CounterpartyStore struct {
	pool *pgxpool.Pool
}

func NewCounterpartyStore(pool *pgxpool.Pool) *CounterpartyStore {
	return &CounterpartyStore{pool: pool}
}

// CounterpartyKind discriminates the legacy-source. Mirrors the
// member service's domain.CounterpartyKind but redeclared here so
// the savings service doesn't pull in a cross-service import.
type CounterpartyKind string

const (
	CounterpartyIndividual CounterpartyKind = "individual"
	// Institutional kinds — chama / company / ngo / church / school / other.
	// Savings doesn't need to distinguish between them at this layer;
	// IsIndividual()/IsInstitutional() handle the dispatch.
)

// CounterpartyView is the kind-aware "principal" snapshot the savings
// handlers need. Field names match the legacy MemberLite shape so
// callers don't have to branch — FullName carries the registered name
// for institutionals + the natural-person name for individuals;
// MemberNo carries the CP-YYYY-NNNNN number for both kinds (the
// legacy M-* / ORG-* numbers live on LegacyID for audit reference).
type CounterpartyView struct {
	ID       uuid.UUID
	Kind     CounterpartyKind
	MemberNo string  // really cp_number — name kept for MemberLite parity
	LegacyID *string // members.id-or-org_members.id text, for legacy URL routing
	FullName string  // really display_name — name kept for MemberLite parity
	Status   string  // counterparties.status enum
	Phone    string  // best-effort from contact JSON
	Email    string  // best-effort from contact JSON
}

// IsIndividual reports whether this counterparty is a natural person.
// Institutional savings actions (e.g. share transfer) still flow but
// callers that need members-only side effects (next-of-kin, etc.) can
// gate on this.
func (c *CounterpartyView) IsIndividual() bool {
	return c.Kind == CounterpartyIndividual
}

// GetByIDTx fetches the counterparty by its canonical id. Returns
// ErrNotFound when the row doesn't exist in the current tenant
// (RLS-scoped). Phone/email come out of the contact JSONB bag — both
// individuals and institutions store it under the same shape
// ({phone, email}) so the savings notifier doesn't need to branch.
func (s *CounterpartyStore) GetByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*CounterpartyView, error) {
	var (
		v          CounterpartyView
		kindStr    string
		legacyID   *string
		contactRaw []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT id, kind::text, cp_number, legacy_id, display_name, status::text, contact
		  FROM counterparties WHERE id = $1
	`, id).Scan(&v.ID, &kindStr, &v.MemberNo, &legacyID, &v.FullName, &v.Status, &contactRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.Kind = CounterpartyKind(kindStr)
	v.LegacyID = legacyID
	// contact JSONB is "always present" but defensively decode — bad
	// data should surface as missing channels, not a 500.
	if len(contactRaw) > 0 {
		var c map[string]any
		if err := json.Unmarshal(contactRaw, &c); err == nil {
			if s, ok := c["phone"].(string); ok {
				v.Phone = s
			}
			if s, ok := c["email"].(string); ok {
				v.Email = s
			}
		}
	}
	return &v, nil
}

// TouchActivityTx bumps last_activity_at on the underlying member
// row. No-op for institutional counterparties — org_members doesn't
// have an activity column today (it's tracked at the signatory
// level rather than the entity level). Phase E C's status-mirror
// follow-up moves activity tracking onto counterparties itself, at
// which point this branches away.
func (s *CounterpartyStore) TouchActivityTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE members SET last_activity_at = now() WHERE counterparty_id = $1`, cpID,
	)
	return err
}
