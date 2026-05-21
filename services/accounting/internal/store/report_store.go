// Read-only report queries. Trial balance + GL detail for now;
// balance sheet / income statement land in the next phase but their
// data already lives in journal_lines.

package store

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/domain"
)

type ReportStore struct {
	pool *pgxpool.Pool
}

func NewReportStore(pool *pgxpool.Pool) *ReportStore {
	return &ReportStore{pool: pool}
}

// TrialBalanceTx computes the trial balance for every active account
// using the journal lines. "Opening" = sum of posted entries strictly
// before `from`; "period" = sum between [from, to]; "closing" = sum
// up to and including `to`. Asset/expense accounts have a debit
// normal balance so a positive net (debits − credits) lands in the
// debit column; liability/equity/income accounts land in the credit
// column.
func (s *ReportStore) TrialBalanceTx(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]domain.TrialBalanceRow, error) {
	rows, err := tx.Query(ctx, `
		WITH movements AS (
		    SELECT
		        a.id            AS account_id,
		        a.code, a.name, a.class, a.normal_balance,
		        COALESCE(SUM(CASE WHEN je.entry_date < $1 THEN l.debit  ELSE 0 END), 0) AS opening_d,
		        COALESCE(SUM(CASE WHEN je.entry_date < $1 THEN l.credit ELSE 0 END), 0) AS opening_c,
		        COALESCE(SUM(CASE WHEN je.entry_date BETWEEN $1 AND $2 THEN l.debit  ELSE 0 END), 0) AS period_d,
		        COALESCE(SUM(CASE WHEN je.entry_date BETWEEN $1 AND $2 THEN l.credit ELSE 0 END), 0) AS period_c
		    FROM chart_of_accounts a
		    LEFT JOIN journal_lines   l  ON l.account_id = a.id
		    LEFT JOIN journal_entries je ON je.id = l.entry_id AND je.status = 'posted'
		    WHERE a.is_active = true
		    GROUP BY a.id, a.code, a.name, a.class, a.normal_balance
		)
		SELECT account_id, code, name, class, normal_balance,
		       opening_d, opening_c, period_d, period_c
		FROM movements
		ORDER BY code
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.TrialBalanceRow{}
	for rows.Next() {
		var (
			r          domain.TrialBalanceRow
			class, nb  string
			oD, oC, pD, pC decimal.Decimal
		)
		if err := rows.Scan(
			&r.AccountID, &r.AccountCode, &r.AccountName, &class, &nb,
			&oD, &oC, &pD, &pC,
		); err != nil {
			return nil, err
		}
		r.Class = domain.AccountClass(class)
		r.NormalBalance = domain.NormalBalance(nb)
		// Net by side. We keep the gross debit/credit per period so
		// the UI can show both; closing = opening + period movements
		// projected onto the account's normal-balance side.
		r.OpeningDebit = oD
		r.OpeningCredit = oC
		r.PeriodDebits = pD
		r.PeriodCredits = pC
		closingNet := oD.Sub(oC).Add(pD.Sub(pC))
		if r.NormalBalance == domain.NormalDebit {
			if closingNet.IsNegative() {
				r.ClosingCredit = closingNet.Neg()
			} else {
				r.ClosingDebit = closingNet
			}
		} else {
			// credit-normal accounts: invert the sign so positive
			// equity/liability/income shows in the credit column.
			closingNet = closingNet.Neg()
			if closingNet.IsNegative() {
				r.ClosingDebit = closingNet.Neg()
			} else {
				r.ClosingCredit = closingNet
			}
		}
		// Filter out accounts with no movement and no opening balance
		// so the report doesn't drown in zero rows. Keep system-locked
		// accounts visible even if zero — they're the canonical CoA.
		if r.OpeningDebit.IsZero() && r.OpeningCredit.IsZero() &&
			r.PeriodDebits.IsZero() && r.PeriodCredits.IsZero() {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GLDetailTx — every line on a single account within a window.
type GLDetailRow struct {
	EntryID     uuid.UUID       `json:"entry_id"`
	EntryNo     *string         `json:"entry_no,omitempty"`
	EntryDate   time.Time       `json:"entry_date"`
	Narration   string          `json:"narration"`
	LineNarr    *string         `json:"line_narration,omitempty"`
	Debit       decimal.Decimal `json:"debit"`
	Credit      decimal.Decimal `json:"credit"`
	RunningBal  decimal.Decimal `json:"running_balance"`
	SourceMod   *string         `json:"source_module,omitempty"`
	SourceRef   *string         `json:"source_ref,omitempty"`
}

// BalanceSheetRow — one balance sheet line. Detail rows have an
// AccountID; the handler interleaves section subtotals on top.
//
// IsContra is true for accounts whose normal balance is the opposite
// of their class's natural side (contra-asset, contra-liability, etc.).
// The handler subtracts contra rows from their class total even though
// the displayed Amount stays positive.
type BalanceSheetRow struct {
	AccountID   *uuid.UUID          `json:"account_id,omitempty"`
	AccountCode string              `json:"account_code,omitempty"`
	AccountName string              `json:"account_name"`
	Class       domain.AccountClass `json:"class"`
	Amount      decimal.Decimal     `json:"amount"`
	IsContra    bool                `json:"is_contra,omitempty"`
}

// BalanceSheetTx — assets / liabilities / equity at `asOf`, computed
// purely from posted journal entries. Returns one row per non-zero
// account ordered by code; the handler groups + subtotals.
func (s *ReportStore) BalanceSheetTx(ctx context.Context, tx pgx.Tx, asOf time.Time) ([]BalanceSheetRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.code, a.name, a.class, a.normal_balance,
		       COALESCE(SUM(l.debit  - l.credit), 0) AS net
		FROM chart_of_accounts a
		LEFT JOIN journal_lines l   ON l.account_id = a.id
		LEFT JOIN journal_entries je ON je.id = l.entry_id
		                            AND je.status = 'posted'
		                            AND je.entry_date <= $1
		WHERE a.class IN ('asset', 'liability', 'equity')
		  AND a.is_active = true
		GROUP BY a.id, a.code, a.name, a.class, a.normal_balance
		ORDER BY a.code
	`, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BalanceSheetRow{}
	for rows.Next() {
		var (
			id        uuid.UUID
			code, nm  string
			cls, nb   string
			net       decimal.Decimal
		)
		if err := rows.Scan(&id, &code, &nm, &cls, &nb, &net); err != nil {
			return nil, err
		}
		// Project the net (debits − credits) onto the account's
		// natural side so balances display as positive when the
		// account is in its normal state.
		amount := net
		if nb == "credit" {
			amount = amount.Neg()
		}
		if amount.IsZero() {
			continue
		}
		out = append(out, BalanceSheetRow{
			AccountID:   &id,
			AccountCode: code,
			AccountName: nm,
			Class:       domain.AccountClass(cls),
			Amount:      amount,
			IsContra:    isContra(cls, nb),
		})
	}
	return out, rows.Err()
}

// isContra — true when an account sits in a class whose natural side
// is the opposite of the account's normal balance. Example: 1120
// Loan Loss Provision is class=asset but normal=credit, so it's a
// contra-asset and should be subtracted from the assets total.
func isContra(class, normalBalance string) bool {
	natural := map[string]string{
		"asset":     "debit",
		"liability": "credit",
		"equity":    "credit",
		"income":    "credit",
		"expense":   "debit",
	}
	if n, ok := natural[class]; ok {
		return n != normalBalance
	}
	return false
}

// NetSurplusTx — the unclosed P&L total (income − expense) from the
// start of the period containing `asOf` through `asOf`. Until the
// period is closed and the surplus is rolled into retained earnings,
// the Balance Sheet equity section needs to surface this number as a
// derived line so the accounting equation holds.
//
// Computes against the *full* posted ledger of income/expense accounts
// (no period filter) because the closing journal is what zeros them —
// any amount sitting on those accounts at as-of is by definition
// unclosed.
func (s *ReportStore) NetSurplusTx(ctx context.Context, tx pgx.Tx, asOf time.Time) (decimal.Decimal, error) {
	var income, expense decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN a.class = 'income'  THEN l.credit - l.debit ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN a.class = 'expense' THEN l.debit  - l.credit ELSE 0 END), 0)
		FROM journal_entries je
		JOIN journal_lines l   ON l.entry_id = je.id
		JOIN chart_of_accounts a ON a.id = l.account_id
		WHERE je.status = 'posted'
		  AND je.entry_date <= $1
		  AND a.class IN ('income', 'expense')
	`, asOf).Scan(&income, &expense)
	if err != nil {
		return decimal.Zero, err
	}
	return income.Sub(expense), nil
}

// IncomeStatementRow — same shape as balance sheet rows but for
// income/expense accounts within a [from, to] window.
type IncomeStatementRow struct {
	AccountID   *uuid.UUID          `json:"account_id,omitempty"`
	AccountCode string              `json:"account_code,omitempty"`
	AccountName string              `json:"account_name"`
	Class       domain.AccountClass `json:"class"`
	Amount      decimal.Decimal     `json:"amount"`
}

func (s *ReportStore) IncomeStatementTx(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]IncomeStatementRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.code, a.name, a.class, a.normal_balance,
		       COALESCE(SUM(l.debit - l.credit), 0) AS net
		FROM chart_of_accounts a
		LEFT JOIN journal_lines l   ON l.account_id = a.id
		LEFT JOIN journal_entries je ON je.id = l.entry_id
		                            AND je.status = 'posted'
		                            AND je.entry_date BETWEEN $1 AND $2
		WHERE a.class IN ('income', 'expense')
		  AND a.is_active = true
		GROUP BY a.id, a.code, a.name, a.class, a.normal_balance
		ORDER BY a.code
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IncomeStatementRow{}
	for rows.Next() {
		var (
			id       uuid.UUID
			code, nm string
			cls, nb  string
			net      decimal.Decimal
		)
		if err := rows.Scan(&id, &code, &nm, &cls, &nb, &net); err != nil {
			return nil, err
		}
		amount := net
		if nb == "credit" {
			amount = amount.Neg()
		}
		if amount.IsZero() {
			continue
		}
		out = append(out, IncomeStatementRow{
			AccountID:   &id,
			AccountCode: code,
			AccountName: nm,
			Class:       domain.AccountClass(cls),
			Amount:      amount,
		})
	}
	return out, rows.Err()
}

func (s *ReportStore) GLDetailTx(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, from, to time.Time, limit int) ([]GLDetailRow, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := tx.Query(ctx, `
		WITH opening AS (
		    SELECT COALESCE(SUM(l.debit - l.credit), 0) AS bal
		    FROM journal_lines l
		    JOIN journal_entries je ON je.id = l.entry_id AND je.status = 'posted'
		    WHERE l.account_id = $1 AND je.entry_date < $2
		),
		lines AS (
		    SELECT je.id, je.entry_no, je.entry_date, je.narration,
		           je.source_module, je.source_ref,
		           l.debit, l.credit, l.narration AS line_narr
		    FROM journal_lines l
		    JOIN journal_entries je ON je.id = l.entry_id AND je.status = 'posted'
		    WHERE l.account_id = $1 AND je.entry_date BETWEEN $2 AND $3
		    ORDER BY je.entry_date, je.created_at
		    LIMIT ` + strconv.Itoa(limit) + `
		)
		SELECT (SELECT bal FROM opening) AS opening,
		       id, entry_no, entry_date, narration, line_narr,
		       debit, credit, source_module, source_ref
		FROM lines
	`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GLDetailRow{}
	var running decimal.Decimal
	first := true
	for rows.Next() {
		var r GLDetailRow
		var opening decimal.Decimal
		if err := rows.Scan(
			&opening, &r.EntryID, &r.EntryNo, &r.EntryDate, &r.Narration, &r.LineNarr,
			&r.Debit, &r.Credit, &r.SourceMod, &r.SourceRef,
		); err != nil {
			return nil, err
		}
		if first {
			running = opening
			first = false
		}
		running = running.Add(r.Debit).Sub(r.Credit)
		r.RunningBal = running
		out = append(out, r)
	}
	return out, rows.Err()
}
