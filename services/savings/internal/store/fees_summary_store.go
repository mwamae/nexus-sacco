// Fees & Collections Summary report data.
//
// Built on receipts × receipt_lines × fee_catalog. The fee_catalog
// gl_credit_code column (PR fee-coa) gives the income account each
// fee maps to; receipt_lines.posted_txn_id links to journal_entries
// via source_ref (PR collection-desk-outbox).
//
// The "unposted" branch surfaces receipt lines whose posted_txn_id is
// still NULL after 5 minutes — typically a brief accounting-service
// outage that the dispatcher couldn't drain. Operations replays them
// via the /v1/finance/posting-outbox/{id}/replay endpoint.

package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

// FeesSummaryFilter — query knobs for the report.
type FeesSummaryFilter struct {
	From           time.Time
	To             time.Time
	Channel        string    // optional — empty = all channels
	FeeCode        string    // optional — empty = all codes
	CounterpartyID uuid.UUID // optional — Nil = all members
}

// FeeCodeRow — per-fee breakdown.
type FeeCodeRow struct {
	FeeCode      string          `json:"fee_code"`
	FeeLabel     string          `json:"fee_label"`
	GLCreditCode string          `json:"gl_credit_code"`
	Count        int             `json:"count"`
	TotalAmount  decimal.Decimal `json:"total_amount"`
	VoidedAmount decimal.Decimal `json:"voided_amount"`
	NetAmount    decimal.Decimal `json:"net_amount"`
}

// ChannelRow — per-channel breakdown.
type ChannelRow struct {
	Channel      string          `json:"channel"`
	Count        int             `json:"count"`
	TotalAmount  decimal.Decimal `json:"total_amount"`
	VoidedAmount decimal.Decimal `json:"voided_amount"`
	NetAmount    decimal.Decimal `json:"net_amount"`
}

// UnpostedRow — a fee/welfare line that should have posted but hasn't.
// posted_txn_id is NULL by definition. To replay: jump to the Posting
// Outbox triage page and replay the stuck row by id. The line-to-outbox
// link via narration matching is brittle, so we don't precompute it
// here — keeps the report query honest.
type UnpostedRow struct {
	ReceiptID     uuid.UUID       `json:"receipt_id"`
	ReceiptSerial string          `json:"receipt_serial"`
	LineID        uuid.UUID       `json:"line_id"`
	FeeCode       string          `json:"fee_code"`
	Channel       string          `json:"channel"`
	Amount        decimal.Decimal `json:"amount"`
	AgeMinutes    int             `json:"age_minutes"`
}

// FeesSummary — full report DTO.
type FeesSummary struct {
	From         time.Time     `json:"from"`
	To           time.Time     `json:"to"`
	TotalAmount  decimal.Decimal `json:"total_amount"`
	TotalVoided  decimal.Decimal `json:"total_voided"`
	NetAmount    decimal.Decimal `json:"net_amount"`
	ByFeeCode    []FeeCodeRow  `json:"by_fee_code"`
	ByChannel    []ChannelRow  `json:"by_channel"`
	Unposted     []UnpostedRow `json:"unposted"`
}

// FeesSummaryStore wraps a single pool — kept thin since this report
// is read-only.
type FeesSummaryStore struct{ pool *pgxpool.Pool }

func NewFeesSummaryStore(pool *pgxpool.Pool) *FeesSummaryStore {
	return &FeesSummaryStore{pool: pool}
}

// SummaryTx computes the report inside the caller's tx. All three
// branches (by_fee_code, by_channel, unposted) share the same WHERE
// filter so totals match across the breakdowns.
//
// Scope: only `fee` and `welfare` kind receipt-lines (the GL-bound
// ones). Other line kinds (savings_deposit, share_purchase,
// loan_repayment) don't run through fee_catalog and aren't in scope
// for this report.
func (s *FeesSummaryStore) SummaryTx(ctx context.Context, tx pgx.Tx, f FeesSummaryFilter) (*FeesSummary, error) {
	// Defensive defaults — caller validates in the handler, but guard
	// against zero-time queries returning everything since 1970.
	if f.From.IsZero() || f.To.IsZero() {
		return nil, fmt.Errorf("FeesSummary: from + to are required")
	}

	// Build the shared WHERE fragment.
	conds := []string{
		"rl.kind IN ('fee','welfare')",
		"r.status = 'posted'",
		"r.value_date BETWEEN $1 AND $2",
	}
	args := []any{f.From, f.To}
	idx := 3
	if f.Channel != "" {
		conds = append(conds, fmt.Sprintf("r.channel = $%d", idx))
		args = append(args, f.Channel)
		idx++
	}
	if f.FeeCode != "" {
		conds = append(conds, fmt.Sprintf("rl.fee_code = $%d", idx))
		args = append(args, f.FeeCode)
		idx++
	}
	if f.CounterpartyID != uuid.Nil {
		conds = append(conds, fmt.Sprintf("r.counterparty_id = $%d", idx))
		args = append(args, f.CounterpartyID)
		idx++
	}
	where := strings.Join(conds, " AND ")

	out := &FeesSummary{From: f.From, To: f.To}

	// ─── Totals ───
	row := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(rl.amount), 0)                                                  AS total,
		  COALESCE(SUM(CASE WHEN rl.voided_at IS NOT NULL THEN rl.amount ELSE 0 END), 0) AS voided
		  FROM receipts r
		  JOIN receipt_lines rl ON rl.receipt_id = r.id
		 WHERE `+where, args...)
	if err := row.Scan(&out.TotalAmount, &out.TotalVoided); err != nil {
		return nil, fmt.Errorf("FeesSummary totals: %w", err)
	}
	out.NetAmount = out.TotalAmount.Sub(out.TotalVoided)

	// ─── By fee code ───
	feeRows, err := tx.Query(ctx, `
		SELECT
		  rl.fee_code,
		  COALESCE(fc.label, rl.fee_code)              AS label,
		  COALESCE(fc.gl_credit_code, '')              AS gl_code,
		  COUNT(*)                                     AS cnt,
		  COALESCE(SUM(rl.amount), 0)                  AS total,
		  COALESCE(SUM(CASE WHEN rl.voided_at IS NOT NULL THEN rl.amount ELSE 0 END), 0) AS voided
		  FROM receipts r
		  JOIN receipt_lines rl    ON rl.receipt_id = r.id
		  LEFT JOIN fee_catalog fc ON fc.tenant_id = r.tenant_id AND fc.code = rl.fee_code
		 WHERE `+where+`
		 GROUP BY rl.fee_code, COALESCE(fc.label, rl.fee_code), COALESCE(fc.gl_credit_code, '')
		 ORDER BY total DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("FeesSummary by_fee_code: %w", err)
	}
	for feeRows.Next() {
		var r FeeCodeRow
		var code *string
		if err := feeRows.Scan(&code, &r.FeeLabel, &r.GLCreditCode, &r.Count, &r.TotalAmount, &r.VoidedAmount); err != nil {
			feeRows.Close()
			return nil, err
		}
		if code != nil {
			r.FeeCode = *code
		}
		r.NetAmount = r.TotalAmount.Sub(r.VoidedAmount)
		out.ByFeeCode = append(out.ByFeeCode, r)
	}
	feeRows.Close()
	if out.ByFeeCode == nil {
		out.ByFeeCode = []FeeCodeRow{}
	}

	// ─── By channel ───
	chRows, err := tx.Query(ctx, `
		SELECT
		  r.channel::text                              AS channel,
		  COUNT(*)                                     AS cnt,
		  COALESCE(SUM(rl.amount), 0)                  AS total,
		  COALESCE(SUM(CASE WHEN rl.voided_at IS NOT NULL THEN rl.amount ELSE 0 END), 0) AS voided
		  FROM receipts r
		  JOIN receipt_lines rl ON rl.receipt_id = r.id
		 WHERE `+where+`
		 GROUP BY r.channel
		 ORDER BY total DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("FeesSummary by_channel: %w", err)
	}
	for chRows.Next() {
		var r ChannelRow
		if err := chRows.Scan(&r.Channel, &r.Count, &r.TotalAmount, &r.VoidedAmount); err != nil {
			chRows.Close()
			return nil, err
		}
		r.NetAmount = r.TotalAmount.Sub(r.VoidedAmount)
		out.ByChannel = append(out.ByChannel, r)
	}
	chRows.Close()
	if out.ByChannel == nil {
		out.ByChannel = []ChannelRow{}
	}

	// ─── Unposted: lines where posted_txn_id is still NULL after 5
	// minutes, not voided, and not awaiting approval (approval_id
	// NULL → ran inline → should already be posted).
	//
	// Window is intentionally wider than the report's [from, to] —
	// drift hides behind narrow time filters when on-call is looking
	// at "today" and the stuck row is from yesterday. Scope only by
	// tenant + voided=false + age >5m.
	unpostedRows, err := tx.Query(ctx, `
		SELECT
		  r.id, r.serial, rl.id, COALESCE(rl.fee_code, ''),
		  r.channel::text, rl.amount,
		  EXTRACT(EPOCH FROM (now() - rl.created_at))::int / 60 AS age_minutes
		  FROM receipt_lines rl
		  JOIN receipts r ON r.id = rl.receipt_id
		 WHERE rl.kind IN ('fee','welfare')
		   AND rl.posted_txn_id IS NULL
		   AND rl.voided_at IS NULL
		   AND rl.approval_id IS NULL
		   AND rl.created_at < now() - interval '5 minutes'
		   AND r.tenant_id = current_tenant_id()
		 ORDER BY rl.created_at DESC
		 LIMIT 200
	`)
	if err != nil {
		return nil, fmt.Errorf("FeesSummary unposted: %w", err)
	}
	for unpostedRows.Next() {
		var u UnpostedRow
		if err := unpostedRows.Scan(&u.ReceiptID, &u.ReceiptSerial, &u.LineID, &u.FeeCode,
			&u.Channel, &u.Amount, &u.AgeMinutes); err != nil {
			unpostedRows.Close()
			return nil, err
		}
		out.Unposted = append(out.Unposted, u)
	}
	unpostedRows.Close()
	if out.Unposted == nil {
		out.Unposted = []UnpostedRow{}
	}

	return out, nil
}
