// Audience resolver — converts a campaign's audience filter into the
// concrete list of recipients (counterparty_id, name, phone, email).
//
// Stage 7 supports four filter shapes; more land in stage 8 as the
// frontend exposes them.
//
//   {"type":"all_members"}
//   {"type":"status",          "status":"active"|"dormant"|"suspended"}
//   {"type":"active_loans"}                  — members with an active loan
//   {"type":"loan_defaulters", "dpd_min":N}  — members with a loan whose dpd ≥ N
//   {"type":"custom_list",     "member_ids":["uuid",…]}
//
// All queries hit the shared `members` and `loans` tables under the
// current_tenant_id() RLS context.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AudienceStore struct {
	pool *pgxpool.Pool
}

func NewAudienceStore(pool *pgxpool.Pool) *AudienceStore {
	return &AudienceStore{pool: pool}
}

type AudienceRecipient struct {
	CounterpartyID uuid.UUID
	MemberNo string
	FullName string
	Phone    *string
	Email    *string
}

type AudienceFilter struct {
	Type      string      `json:"type"`
	Status    string      `json:"status,omitempty"`
	DPDMin    int         `json:"dpd_min,omitempty"`
	MemberIDs []uuid.UUID `json:"member_ids,omitempty"`
}

func ParseAudience(raw []byte) (*AudienceFilter, error) {
	if len(raw) == 0 {
		return &AudienceFilter{Type: "all_members"}, nil
	}
	var f AudienceFilter
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse audience: %w", err)
	}
	if f.Type == "" {
		f.Type = "all_members"
	}
	return &f, nil
}

// ResolveCountTx returns the recipient count for a filter without
// materialising the full list — used by the campaign preview endpoint.
func (s *AudienceStore) ResolveCountTx(ctx context.Context, tx pgx.Tx, f *AudienceFilter) (int, error) {
	q, args, err := buildAudienceQuery(f, true)
	if err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ResolveTx materialises the recipient list. Caller is expected to
// iterate and dispatch — for very large lists (>100k) we'd want
// cursor-based streaming, but that's a stage-7b concern.
func (s *AudienceStore) ResolveTx(ctx context.Context, tx pgx.Tx, f *AudienceFilter) ([]AudienceRecipient, error) {
	q, args, err := buildAudienceQuery(f, false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AudienceRecipient{}
	for rows.Next() {
		var r AudienceRecipient
		if err := rows.Scan(&r.CounterpartyID, &r.MemberNo, &r.FullName, &r.Phone, &r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func buildAudienceQuery(f *AudienceFilter, countOnly bool) (string, []any, error) {
	if f == nil {
		f = &AudienceFilter{Type: "all_members"}
	}
	selectCols := "m.id, m.member_no, m.full_name, m.phone, m.email"
	if countOnly {
		selectCols = "COUNT(DISTINCT m.id)"
	}
	switch f.Type {
	case "all_members":
		return `SELECT ` + selectCols + ` FROM members m`, nil, nil

	case "status":
		if f.Status == "" {
			return "", nil, errors.New("audience.status requires a status value")
		}
		return `SELECT ` + selectCols + ` FROM members m WHERE m.status = $1::member_status`,
			[]any{f.Status}, nil

	case "active_loans":
		return `
			SELECT ` + selectCols + `
			FROM members m
			JOIN loans l ON l.counterparty_id = m.id
			WHERE l.status IN ('active', 'in_arrears', 'restructured')
		`, nil, nil

	case "loan_defaulters":
		dpd := f.DPDMin
		if dpd <= 0 {
			dpd = 30
		}
		return `
			SELECT ` + selectCols + `
			FROM members m
			JOIN loans l ON l.counterparty_id = m.id
			WHERE l.status IN ('active', 'in_arrears', 'restructured', 'defaulted')
			  AND l.days_past_due >= $1
		`, []any{dpd}, nil

	case "custom_list":
		if len(f.MemberIDs) == 0 {
			return "", nil, errors.New("audience.custom_list requires member_ids")
		}
		return `SELECT ` + selectCols + ` FROM members m WHERE m.id = ANY($1::uuid[])`,
			[]any{f.MemberIDs}, nil

	default:
		return "", nil, fmt.Errorf("unknown audience type %q", f.Type)
	}
}
