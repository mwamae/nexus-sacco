// Counterparty store — Phase A read path + cp_number mint + the
// mirror-write helpers MemberStore + OrgStore call from inside their
// own write transactions.
//
// Sequence note: cp_number uses the shared share_number_seq table
// with kind='counterparty', mirroring the savings service's reuse of
// the same table for L-/LA-/SHA- prefixes. Format: CP-YYYY-NNNNN.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/domain"
)

type CounterpartyStore struct {
	pool *pgxpool.Pool
}

func NewCounterpartyStore(pool *pgxpool.Pool) *CounterpartyStore {
	return &CounterpartyStore{pool: pool}
}

const cpCols = `
  id, tenant_id, cp_number, legacy_id, kind, display_name, trading_as,
  status, kyc_state, risk_band, registration_no,
  individual, institution, contact,
  joined_at, closed_at, created_at, updated_at, created_by, updated_by
`

// cpColsWithLegacyTarget — used by reads (List + Get). LEFT JOINs
// onto the two legacy bridges so each row carries the id of the
// `members` or `org_members` row it mirrors. Coalescing in SQL keeps
// the column count fixed at 21.
const cpColsWithLegacyTarget = cpCols + `,
  COALESCE(
    (SELECT m.id FROM members m WHERE m.counterparty_id = counterparties.id),
    (SELECT o.id FROM org_members o WHERE o.counterparty_id = counterparties.id)
  ) AS legacy_target_id
`

func scanCounterparty(row pgx.Row) (*domain.Counterparty, error) {
	var c domain.Counterparty
	if err := row.Scan(
		&c.ID, &c.TenantID, &c.CPNumber, &c.LegacyID, &c.Kind, &c.DisplayName, &c.TradingAs,
		&c.Status, &c.KYCState, &c.RiskBand, &c.RegistrationNo,
		&c.Individual, &c.Institution, &c.Contact,
		&c.JoinedAt, &c.ClosedAt, &c.CreatedAt, &c.UpdatedAt, &c.CreatedBy, &c.UpdatedBy,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// scanCounterpartyWithTarget — same shape as scanCounterparty + the
// legacy_target_id tail column. Used by SELECTs only.
func scanCounterpartyWithTarget(row pgx.Row) (*domain.Counterparty, error) {
	var c domain.Counterparty
	if err := row.Scan(
		&c.ID, &c.TenantID, &c.CPNumber, &c.LegacyID, &c.Kind, &c.DisplayName, &c.TradingAs,
		&c.Status, &c.KYCState, &c.RiskBand, &c.RegistrationNo,
		&c.Individual, &c.Institution, &c.Contact,
		&c.JoinedAt, &c.ClosedAt, &c.CreatedAt, &c.UpdatedAt, &c.CreatedBy, &c.UpdatedBy,
		&c.LegacyTargetID,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// NextCPNumberTx mints the next CP-YYYY-NNNNN for the tenant. Uses
// the existing share_number_seq table with kind='counterparty' so we
// don't have to add another sequence table.
func (s *CounterpartyStore) NextCPNumberTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (string, error) {
	year := time.Now().UTC().Year()
	var next int
	err := tx.QueryRow(ctx, `
		INSERT INTO share_number_seq (tenant_id, kind, year, last_value)
		VALUES ($1, 'counterparty', $2, 1)
		ON CONFLICT (tenant_id, kind, year)
		DO UPDATE SET last_value = share_number_seq.last_value + 1
		RETURNING last_value
	`, tenantID, year).Scan(&next)
	if err != nil {
		return "", fmt.Errorf("bump counterparty seq: %w", err)
	}
	return fmt.Sprintf("CP-%d-%05d", year, next), nil
}

// ─────────── Reads ───────────

// Renamed CPListInput / CPListResult to avoid colliding with the
// legacy MemberStore.ListInput / ListResult types that the same
// package exposes today.
type CPListInput struct {
	Kind         []domain.CounterpartyKind
	Status       []domain.CounterpartyStatus
	IncludeTest  bool
	Query        string // matches cp_number / legacy_id / display_name / contact.email / contact.phone
	Limit        int
	Offset       int
}

type CPListResult struct {
	Counterparties []domain.Counterparty
	Total          int
}

func (s *CounterpartyStore) ListTx(ctx context.Context, tx pgx.Tx, in CPListInput) (*CPListResult, error) {
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	args := []any{}
	where := []string{}
	if len(in.Kind) > 0 {
		kinds := make([]string, len(in.Kind))
		for i, k := range in.Kind { kinds[i] = string(k) }
		where = append(where, fmt.Sprintf("kind = ANY($%d::counterparty_kind[])", len(args)+1))
		args = append(args, kinds)
	}
	if len(in.Status) > 0 {
		statuses := make([]string, len(in.Status))
		for i, s := range in.Status { statuses[i] = string(s) }
		where = append(where, fmt.Sprintf("status = ANY($%d::counterparty_status[])", len(args)+1))
		args = append(args, statuses)
	}
	if q := strings.TrimSpace(in.Query); q != "" {
		// Single text predicate matching every label our register
		// search exposes. JSON ops on contact use ->> '...' which
		// is sequential-scan today; a GIN index lands when needed.
		where = append(where, fmt.Sprintf(
			"(cp_number ILIKE $%d OR legacy_id ILIKE $%d OR display_name ILIKE $%d "+
				"OR contact->>'email' ILIKE $%d OR contact->>'phone' ILIKE $%d)",
			len(args)+1, len(args)+1, len(args)+1, len(args)+1, len(args)+1,
		))
		args = append(args, "%"+q+"%")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM counterparties"+whereSQL, args...,
	).Scan(&total); err != nil {
		return nil, err
	}

	args2 := append(args, in.Limit, in.Offset)
	rows, err := tx.Query(ctx,
		"SELECT "+cpColsWithLegacyTarget+" FROM counterparties"+whereSQL+
			fmt.Sprintf(" ORDER BY created_at DESC, cp_number DESC LIMIT $%d OFFSET $%d",
				len(args)+1, len(args)+2), args2...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &CPListResult{Counterparties: []domain.Counterparty{}, Total: total}
	for rows.Next() {
		c, err := scanCounterpartyWithTarget(rows)
		if err != nil {
			return nil, err
		}
		out.Counterparties = append(out.Counterparties, *c)
	}
	return out, rows.Err()
}

func (s *CounterpartyStore) GetTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*domain.Counterparty, error) {
	row := tx.QueryRow(ctx, "SELECT "+cpColsWithLegacyTarget+" FROM counterparties WHERE id = $1", id)
	c, err := scanCounterpartyWithTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ─────────── Writes ───────────

// CreateInput is what mirror writers + the Phase B create endpoint
// hand to CreateTx. The caller is responsible for matching kind ↔
// payload (the DB CHECK constraint enforces it as a backstop).
type CreateInput struct {
	TenantID       uuid.UUID
	LegacyID       *string
	Kind           domain.CounterpartyKind
	DisplayName    string
	TradingAs      *string
	Status         domain.CounterpartyStatus
	KYCState       domain.CounterpartyKYCState
	RiskBand       domain.CounterpartyRiskBand
	RegistrationNo *string
	Individual     json.RawMessage
	Institution    json.RawMessage
	Contact        json.RawMessage
	CreatedBy      *uuid.UUID
}

func (s *CounterpartyStore) CreateTx(ctx context.Context, tx pgx.Tx, in CreateInput) (*domain.Counterparty, error) {
	cpNo, err := s.NextCPNumberTx(ctx, tx, in.TenantID)
	if err != nil {
		return nil, err
	}
	if in.KYCState == "" {
		in.KYCState = domain.CPKYCNotStarted
	}
	if in.RiskBand == "" {
		in.RiskBand = domain.CPRiskNA
	}
	if in.Contact == nil {
		in.Contact = json.RawMessage(`{}`)
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO counterparties (
		  tenant_id, cp_number, legacy_id, kind, display_name, trading_as,
		  status, kyc_state, risk_band, registration_no,
		  individual, institution, contact,
		  joined_at, created_by
		) VALUES (
		  current_tenant_id(), $1, $2, $3, $4, $5,
		  $6, $7, $8, $9,
		  $10, $11, $12,
		  now(), $13
		)
		RETURNING `+cpCols,
		cpNo, in.LegacyID, string(in.Kind), in.DisplayName, in.TradingAs,
		string(in.Status), string(in.KYCState), string(in.RiskBand), in.RegistrationNo,
		in.Individual, in.Institution, in.Contact,
		in.CreatedBy,
	)
	return scanCounterparty(row)
}

// UpdatePatch is the partial-update shape for Phase B PATCH. Nil
// fields are skipped; the JSONB bags can be replaced wholesale (no
// jsonb_patch merge) — callers that want per-key merge can fetch +
// merge + replace.
type UpdatePatch struct {
	DisplayName    *string
	TradingAs      *string
	Status         *domain.CounterpartyStatus
	KYCState       *domain.CounterpartyKYCState
	RiskBand       *domain.CounterpartyRiskBand
	RegistrationNo *string
	Individual     *json.RawMessage
	Institution    *json.RawMessage
	Contact        *json.RawMessage
	UpdatedBy      *uuid.UUID
}

func (s *CounterpartyStore) UpdateTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, p UpdatePatch) (*domain.Counterparty, error) {
	sets := []string{"updated_at = now()"}
	args := []any{id}
	idx := 2
	add := func(col string, v any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, v); idx++
	}
	if p.DisplayName != nil    { add("display_name", *p.DisplayName) }
	if p.TradingAs != nil      { add("trading_as", p.TradingAs) }
	if p.Status != nil         { add("status", string(*p.Status)) }
	if p.KYCState != nil       { add("kyc_state", string(*p.KYCState)) }
	if p.RiskBand != nil       { add("risk_band", string(*p.RiskBand)) }
	if p.RegistrationNo != nil { add("registration_no", p.RegistrationNo) }
	if p.Individual != nil     { add("individual", *p.Individual) }
	if p.Institution != nil    { add("institution", *p.Institution) }
	if p.Contact != nil        { add("contact", *p.Contact) }
	if p.UpdatedBy != nil      { add("updated_by", *p.UpdatedBy) }

	q := fmt.Sprintf("UPDATE counterparties SET %s WHERE id = $1 RETURNING %s",
		strings.Join(sets, ", "), cpCols)
	row := tx.QueryRow(ctx, q, args...)
	c, err := scanCounterparty(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// SetCounterpartyOnMemberTx + SetCounterpartyOnOrgTx stamp the bridge
// column after a mirror create. Idempotent — re-running on a row that
// already has a counterparty_id is a no-op.
func (s *CounterpartyStore) SetCounterpartyOnMemberTx(ctx context.Context, tx pgx.Tx, memberID, cpID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE members SET counterparty_id = $2 WHERE id = $1 AND counterparty_id IS NULL`,
		memberID, cpID)
	return err
}

func (s *CounterpartyStore) SetCounterpartyOnOrgTx(ctx context.Context, tx pgx.Tx, orgID, cpID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE org_members SET counterparty_id = $2 WHERE id = $1 AND counterparty_id IS NULL`,
		orgID, cpID)
	return err
}
