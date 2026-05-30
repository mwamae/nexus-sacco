// DSID Phase 2.1 — statement payload assemblers.
//
// For each statement kind, returns a `Payload` struct + a pre-rendered
// HTML blob for the table rows. The notification service's PDF template
// engine is plain {{var}} substitution (no loops), so the assembler
// does the row HTML server-side and the template plugs it in via a
// single {{transactions_html}} / {{lines_html}} placeholder.
//
// Each Payload is convertible to a map[string]any via ToPayload() — the
// notifier client passes that map to the PDF generator.

package store

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type StatementsStore struct {
	pool *pgxpool.Pool
}

func NewStatementsStore(pool *pgxpool.Pool) *StatementsStore {
	return &StatementsStore{pool: pool}
}

// ─────────── shared types ───────────

type StatementCommon struct {
	TenantName       string
	TenantAddress    string
	TenantDisclaimer string
	Currency         string
	MemberName       string
	MemberNo         string
	PeriodLabel      string
	GeneratedDate    string
}

// ─────────── deposit statement ───────────

type DepositStatementPayload struct {
	StatementCommon
	AccountLabel     string // "All accounts" when consolidated; otherwise "<product_name> · <account_no>"
	TransactionsHTML string // pre-rendered <tr> rows
	MaxTxnAt         time.Time // for the cache key
}

// BuildDepositStatementTx returns the payload for a member's deposit
// account(s) over [from, to]. When accountID is nil, every account the
// member holds is consolidated into one table.
func (s *StatementsStore) BuildDepositStatementTx(
	ctx context.Context, tx pgx.Tx,
	common StatementCommon, memberID uuid.UUID, accountID *uuid.UUID,
	from, to time.Time,
) (*DepositStatementPayload, error) {
	var label string
	if accountID == nil {
		label = "All deposit accounts"
	} else {
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(p.name, '') || ' · ' || a.account_no
			  FROM deposit_accounts a
			  LEFT JOIN deposit_products p ON p.id = a.product_id
			 WHERE a.id = $1
		`, *accountID).Scan(&label)
	}

	// Member's accounts → counterparty_id (member_id on the account in
	// older schemas). Deposit accounts in this codebase carry
	// counterparty_id; we match either column name to stay safe.
	var rows pgx.Rows
	var err error
	if accountID != nil {
		rows, err = tx.Query(ctx, `
			SELECT t.posted_at, t.txn_no, t.txn_type::text, t.channel,
			       COALESCE(t.narration, ''), t.amount, t.balance_after
			  FROM deposit_transactions t
			 WHERE t.account_id = $1
			   AND t.posted_at >= $2 AND t.posted_at < $3
			 ORDER BY t.posted_at, t.txn_no
		`, *accountID, from, to)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT t.posted_at, t.txn_no, t.txn_type::text, t.channel,
			       COALESCE(t.narration, ''), t.amount, t.balance_after
			  FROM deposit_transactions t
			  JOIN deposit_accounts a ON a.id = t.account_id
			 WHERE a.counterparty_id = $1
			   AND t.posted_at >= $2 AND t.posted_at < $3
			 ORDER BY t.posted_at, t.txn_no
		`, memberID, from, to)
	}
	if err != nil {
		return nil, fmt.Errorf("query deposit txns: %w", err)
	}
	defer rows.Close()

	var (
		sb       strings.Builder
		maxTxnAt time.Time
		rowCount int
	)
	for rows.Next() {
		var postedAt time.Time
		var txnNo, txnType, channel, narration string
		var amount, balanceAfter decimal.Decimal
		if err := rows.Scan(&postedAt, &txnNo, &txnType, &channel, &narration, &amount, &balanceAfter); err != nil {
			return nil, err
		}
		if postedAt.After(maxTxnAt) {
			maxTxnAt = postedAt
		}
		amountClass := "num"
		if amount.IsNegative() {
			amountClass = "num neg"
		}
		fmt.Fprintf(&sb, `<tr>
		  <td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td>
		  <td class="%s">%s</td><td class="num">%s</td></tr>`,
			html.EscapeString(postedAt.Format("2006-01-02")),
			html.EscapeString(txnNo),
			html.EscapeString(humanise(txnType)),
			html.EscapeString(channel),
			html.EscapeString(truncate(narration, 40)),
			amountClass, html.EscapeString(money(amount)),
			html.EscapeString(money(balanceAfter)),
		)
		rowCount++
	}
	if rowCount == 0 {
		sb.WriteString(`<tr><td colspan="7" style="text-align:center;color:#888;padding:16px;">No transactions in this period.</td></tr>`)
	}

	return &DepositStatementPayload{
		StatementCommon:  common,
		AccountLabel:     label,
		TransactionsHTML: sb.String(),
		MaxTxnAt:         maxTxnAt,
	}, nil
}

// ToPayload — flatten into the map the PDF generator consumes.
func (p *DepositStatementPayload) ToPayload() map[string]any {
	m := commonMap(p.StatementCommon)
	m["account_label"] = p.AccountLabel
	m["transactions_html"] = p.TransactionsHTML
	return m
}

// ─────────── share statement ───────────

type ShareStatementPayload struct {
	StatementCommon
	OpeningShares    int
	ClosingShares    int
	ParValue         decimal.Decimal
	TotalWorth       decimal.Decimal
	PledgedShares    int
	TransactionsHTML string
	MaxTxnAt         time.Time
}

func (s *StatementsStore) BuildShareStatementTx(
	ctx context.Context, tx pgx.Tx,
	common StatementCommon, memberID uuid.UUID, from, to time.Time,
) (*ShareStatementPayload, error) {
	// Closing position from share_accounts (snapshot).
	var heldShares, pledgedShares int
	var parValue decimal.Decimal
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(shares_held), 0),
		       COALESCE(SUM(shares_pledged), 0),
		       COALESCE(MAX(par_value_at_open), 0)
		  FROM share_accounts
		 WHERE counterparty_id = $1
	`, memberID).Scan(&heldShares, &pledgedShares, &parValue)

	// Opening shares = first balance_after_shares - shares_delta of the
	// earliest txn in range. Cheaper: compute as closingShares - sum(
	// shares_delta) of the in-range rows.
	var sumDelta int
	_ = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(shares_delta), 0)
		  FROM share_transactions
		 WHERE counterparty_id = $1
		   AND posted_at >= $2 AND posted_at < $3
	`, memberID, from, to).Scan(&sumDelta)
	openingShares := heldShares - sumDelta

	// Pre-render rows.
	rows, err := tx.Query(ctx, `
		SELECT t.posted_at, t.txn_no, t.txn_type::text, t.shares_delta,
		       t.par_value_at_txn, t.amount, t.balance_after_shares
		  FROM share_transactions t
		 WHERE t.counterparty_id = $1
		   AND t.posted_at >= $2 AND t.posted_at < $3
		 ORDER BY t.posted_at, t.txn_no
	`, memberID, from, to)
	if err != nil {
		return nil, fmt.Errorf("query share txns: %w", err)
	}
	defer rows.Close()
	var sb strings.Builder
	var maxTxnAt time.Time
	var n int
	for rows.Next() {
		var postedAt time.Time
		var txnNo, txnType string
		var shareDelta int
		var par, amount decimal.Decimal
		var balanceShares int
		if err := rows.Scan(&postedAt, &txnNo, &txnType, &shareDelta, &par, &amount, &balanceShares); err != nil {
			return nil, err
		}
		if postedAt.After(maxTxnAt) {
			maxTxnAt = postedAt
		}
		fmt.Fprintf(&sb, `<tr>
		  <td>%s</td><td>%s</td><td>%s</td>
		  <td class="num">%+d</td><td class="num">%s</td><td class="num">%s</td>
		  <td class="num">%d</td></tr>`,
			html.EscapeString(postedAt.Format("2006-01-02")),
			html.EscapeString(txnNo),
			html.EscapeString(humanise(txnType)),
			shareDelta,
			html.EscapeString(money(par)),
			html.EscapeString(money(amount)),
			balanceShares,
		)
		n++
	}
	if n == 0 {
		sb.WriteString(`<tr><td colspan="7" style="text-align:center;color:#888;padding:16px;">No share transactions in this period.</td></tr>`)
	}

	totalWorth := parValue.Mul(decimal.NewFromInt(int64(heldShares)))
	_ = openingShares

	return &ShareStatementPayload{
		StatementCommon:  common,
		OpeningShares:    openingShares,
		ClosingShares:    heldShares,
		ParValue:         parValue,
		TotalWorth:       totalWorth,
		PledgedShares:    pledgedShares,
		TransactionsHTML: sb.String(),
		MaxTxnAt:         maxTxnAt,
	}, nil
}

func (p *ShareStatementPayload) ToPayload() map[string]any {
	m := commonMap(p.StatementCommon)
	m["closing_shares"] = fmt.Sprintf("%d", p.ClosingShares)
	m["par_value"] = money(p.ParValue)
	m["total_worth"] = money(p.TotalWorth)
	m["pledged_shares"] = fmt.Sprintf("%d", p.PledgedShares)
	m["transactions_html"] = p.TransactionsHTML
	return m
}

// ─────────── interest statement ───────────

type InterestStatementPayload struct {
	StatementCommon
	FYLabel     string
	AGMRatePct  decimal.Decimal
	WHTRatePct  decimal.Decimal
	RunNo       string
	RunDate     string
	LinesHTML   string
	MaxTxnAt    time.Time
}

func (s *StatementsStore) BuildInterestStatementTx(
	ctx context.Context, tx pgx.Tx,
	common StatementCommon, memberID uuid.UUID, fyLabel string,
) (*InterestStatementPayload, error) {
	// Resolve the (most recent) interest_run for the FY label.
	var runID uuid.UUID
	var runNo string
	var runDate time.Time
	var agmRate, whtRate decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT id, run_no, COALESCE(posted_at, created_at), agm_rate_pct, wht_rate_pct
		  FROM interest_runs
		 WHERE financial_year_label = $1
		 ORDER BY posted_at DESC NULLS LAST, created_at DESC
		 LIMIT 1
	`, fyLabel).Scan(&runID, &runNo, &runDate, &agmRate, &whtRate)
	if err == pgx.ErrNoRows {
		// No run for this FY — return an empty-but-valid statement.
		return &InterestStatementPayload{
			StatementCommon: common,
			FYLabel:         fyLabel,
			LinesHTML:       `<tr><td colspan="7" style="text-align:center;color:#888;padding:16px;">No interest run posted for ` + html.EscapeString(fyLabel) + `.</td></tr>`,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query interest_runs: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT a.account_no,
		       l.weighted_avg_balance, l.rate_applied_pct,
		       l.gross_interest, l.wht_amount, l.net_interest,
		       l.payout_method::text,
		       COALESCE(pa.account_no, l.payout_external_channel, '')
		  FROM interest_run_lines l
		  JOIN deposit_accounts a ON a.id = l.account_id
		  LEFT JOIN deposit_accounts pa ON pa.id = l.payout_target_account_id
		 WHERE l.run_id = $1 AND l.counterparty_id = $2
		 ORDER BY a.account_no
	`, runID, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sb strings.Builder
	var totalGross, totalWHT, totalNet decimal.Decimal
	var n int
	for rows.Next() {
		var accountNo string
		var weighted decimal.Decimal
		var ratePct decimal.Decimal
		var gross, wht, net decimal.Decimal
		var payoutMethod, payoutDest string
		if err := rows.Scan(&accountNo, &weighted, &ratePct, &gross, &wht, &net, &payoutMethod, &payoutDest); err != nil {
			return nil, err
		}
		totalGross = totalGross.Add(gross)
		totalWHT = totalWHT.Add(wht)
		totalNet = totalNet.Add(net)
		payoutLabel := humanise(payoutMethod)
		if payoutDest != "" {
			payoutLabel += " → " + payoutDest
		}
		fmt.Fprintf(&sb, `<tr>
		  <td>%s</td>
		  <td class="num">%s</td><td class="num">%s%%</td>
		  <td class="num">%s</td><td class="num">%s</td><td class="num">%s</td>
		  <td>%s</td></tr>`,
			html.EscapeString(accountNo),
			html.EscapeString(money(weighted)),
			html.EscapeString(ratePct.String()),
			html.EscapeString(money(gross)),
			html.EscapeString(money(wht)),
			html.EscapeString(money(net)),
			html.EscapeString(payoutLabel),
		)
		n++
	}
	if n == 0 {
		sb.WriteString(`<tr><td colspan="7" style="text-align:center;color:#888;padding:16px;">No interest lines posted to this member for this run.</td></tr>`)
	} else {
		fmt.Fprintf(&sb, `<tr class="totals">
		  <td>Totals</td>
		  <td class="num">—</td><td class="num">—</td>
		  <td class="num">%s</td><td class="num">%s</td><td class="num">%s</td>
		  <td>—</td></tr>`,
			html.EscapeString(money(totalGross)),
			html.EscapeString(money(totalWHT)),
			html.EscapeString(money(totalNet)),
		)
	}

	return &InterestStatementPayload{
		StatementCommon: common,
		FYLabel:         fyLabel,
		AGMRatePct:      agmRate,
		WHTRatePct:      whtRate,
		RunNo:           runNo,
		RunDate:         runDate.Format("2006-01-02"),
		LinesHTML:       sb.String(),
		MaxTxnAt:        runDate,
	}, nil
}

func (p *InterestStatementPayload) ToPayload() map[string]any {
	m := commonMap(p.StatementCommon)
	m["fy_label"] = p.FYLabel
	m["agm_rate_pct"] = p.AGMRatePct.String()
	m["wht_rate_pct"] = p.WHTRatePct.String()
	m["run_no"] = p.RunNo
	m["run_date"] = p.RunDate
	m["lines_html"] = p.LinesHTML
	return m
}

// ─────────── dividend statement ───────────

type DividendStatementPayload struct {
	StatementCommon
	FYLabel             string
	AGMRatePct          decimal.Decimal
	WHTRatePct          decimal.Decimal
	RunNo               string
	RunDate             string
	AverageShareCapital decimal.Decimal
	SharesBasis         decimal.Decimal
	GrossDividend       decimal.Decimal
	WHTAmount           decimal.Decimal
	NetDividend         decimal.Decimal
	PayoutMethod        string
	PayoutDestination   string
	MaxTxnAt            time.Time
}

func (s *StatementsStore) BuildDividendStatementTx(
	ctx context.Context, tx pgx.Tx,
	common StatementCommon, memberID uuid.UUID, fyLabel string,
) (*DividendStatementPayload, error) {
	var runID uuid.UUID
	var runNo string
	var runDate time.Time
	var agmRate, whtRate decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT id, run_no, COALESCE(posted_at, created_at), agm_rate_pct, wht_rate_pct
		  FROM dividend_runs
		 WHERE financial_year_label = $1
		 ORDER BY posted_at DESC NULLS LAST, created_at DESC
		 LIMIT 1
	`, fyLabel).Scan(&runID, &runNo, &runDate, &agmRate, &whtRate)
	if err == pgx.ErrNoRows {
		return &DividendStatementPayload{
			StatementCommon: common,
			FYLabel:         fyLabel,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query dividend_runs: %w", err)
	}
	// Aggregate the member's lines (multiple if they hold multiple share accounts).
	var sharesBasis, capitalBasis, gross, wht, net decimal.Decimal
	var payoutMethod, payoutDest string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(shares_basis), 0),
		       COALESCE(SUM(capital_basis), 0),
		       COALESCE(SUM(gross_dividend), 0),
		       COALESCE(SUM(wht_amount), 0),
		       COALESCE(SUM(net_dividend), 0),
		       COALESCE(MAX(payout_method::text), ''),
		       COALESCE(MAX(COALESCE(pa.account_no, l.payout_external_channel, '')), '')
		  FROM dividend_run_lines l
		  LEFT JOIN deposit_accounts pa ON pa.id = l.payout_target_account_id
		 WHERE l.run_id = $1 AND l.counterparty_id = $2
	`, runID, memberID).Scan(&sharesBasis, &capitalBasis, &gross, &wht, &net, &payoutMethod, &payoutDest)
	if err != nil {
		return nil, err
	}
	return &DividendStatementPayload{
		StatementCommon:     common,
		FYLabel:             fyLabel,
		AGMRatePct:          agmRate,
		WHTRatePct:          whtRate,
		RunNo:               runNo,
		RunDate:             runDate.Format("2006-01-02"),
		AverageShareCapital: capitalBasis,
		SharesBasis:         sharesBasis,
		GrossDividend:       gross,
		WHTAmount:           wht,
		NetDividend:         net,
		PayoutMethod:        humanise(payoutMethod),
		PayoutDestination:   payoutDest,
		MaxTxnAt:            runDate,
	}, nil
}

func (p *DividendStatementPayload) ToPayload() map[string]any {
	m := commonMap(p.StatementCommon)
	m["fy_label"] = p.FYLabel
	m["agm_rate_pct"] = p.AGMRatePct.String()
	m["wht_rate_pct"] = p.WHTRatePct.String()
	m["run_no"] = p.RunNo
	m["run_date"] = p.RunDate
	m["average_share_capital"] = money(p.AverageShareCapital)
	m["shares_basis"] = money(p.SharesBasis)
	m["gross_dividend"] = money(p.GrossDividend)
	m["wht_amount"] = money(p.WHTAmount)
	m["net_dividend"] = money(p.NetDividend)
	m["payout_method"] = p.PayoutMethod
	dest := ""
	if p.PayoutDestination != "" {
		dest = " → " + p.PayoutDestination
	}
	m["payout_destination_suffix"] = dest
	return m
}

// ─────────── helpers ───────────

func commonMap(c StatementCommon) map[string]any {
	return map[string]any{
		"tenant_name":       c.TenantName,
		"tenant_address":    c.TenantAddress,
		"tenant_disclaimer": c.TenantDisclaimer,
		"currency":          ifEmpty(c.Currency, "KES"),
		"member_name":       c.MemberName,
		"member_no":         c.MemberNo,
		"period_label":      c.PeriodLabel,
		"generated_date":    c.GeneratedDate,
	}
}

func money(d decimal.Decimal) string {
	return d.StringFixed(2)
}

func humanise(s string) string {
	if s == "" {
		return "—"
	}
	return strings.ReplaceAll(s, "_", " ")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
