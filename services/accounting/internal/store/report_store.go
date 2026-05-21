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
	// The CASE WHEN guard is load-bearing: LEFT JOIN journal_lines matches
	// every line for the account, then LEFT JOIN journal_entries with the
	// date filter only sets je.id when the filter passes. Without the
	// guard, SUM(l.debit - l.credit) would include lines whose entry was
	// filtered out (since l is still non-NULL), corrupting the balance.
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.code, a.name, a.class, a.normal_balance,
		       COALESCE(SUM(CASE WHEN je.id IS NOT NULL THEN l.debit - l.credit ELSE 0 END), 0) AS net
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
	// CASE WHEN guard — see BalanceSheetTx for the rationale.
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.code, a.name, a.class, a.normal_balance,
		       COALESCE(SUM(CASE WHEN je.id IS NOT NULL THEN l.debit - l.credit ELSE 0 END), 0) AS net
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

// ─────────── Statement of Changes in Equity ───────────

// ChangesInEquityRow — one equity account's movement over a period:
// opening balance (carried in), period increases, period decreases,
// closing balance. Amounts are projected to the equity natural side
// (credit positive).
type ChangesInEquityRow struct {
	AccountID   *uuid.UUID      `json:"account_id,omitempty"`
	AccountCode string          `json:"account_code,omitempty"`
	AccountName string          `json:"account_name"`
	AccountType string          `json:"account_type,omitempty"`
	Opening     decimal.Decimal `json:"opening"`
	Increase    decimal.Decimal `json:"increase"`
	Decrease    decimal.Decimal `json:"decrease"`
	Closing     decimal.Decimal `json:"closing"`
}

// ChangesInEquityTx — per-equity-account movement over [from, to].
//
// Opening balance = projected balance just before `from`.
// Period activity uses credits as increases and debits as decreases
// (consistent with equity's natural side).
func (s *ReportStore) ChangesInEquityTx(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]ChangesInEquityRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.code, a.name, a.type,
		  COALESCE(SUM(
		    CASE WHEN je.entry_date < $1 THEN l.credit - l.debit ELSE 0 END
		  ), 0) AS opening,
		  COALESCE(SUM(
		    CASE WHEN je.entry_date BETWEEN $1 AND $2 THEN l.credit ELSE 0 END
		  ), 0) AS increase,
		  COALESCE(SUM(
		    CASE WHEN je.entry_date BETWEEN $1 AND $2 THEN l.debit  ELSE 0 END
		  ), 0) AS decrease
		FROM chart_of_accounts a
		LEFT JOIN journal_lines l   ON l.account_id = a.id
		LEFT JOIN journal_entries je ON je.id = l.entry_id AND je.status = 'posted'
		WHERE a.class = 'equity' AND a.is_active = true
		GROUP BY a.id, a.code, a.name, a.type
		ORDER BY a.code
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangesInEquityRow{}
	for rows.Next() {
		var (
			id                          uuid.UUID
			code, name, typ             string
			opening, increase, decrease decimal.Decimal
		)
		if err := rows.Scan(&id, &code, &name, &typ, &opening, &increase, &decrease); err != nil {
			return nil, err
		}
		closing := opening.Add(increase).Sub(decrease)
		// Suppress accounts that are zero throughout the window.
		if opening.IsZero() && increase.IsZero() && decrease.IsZero() {
			continue
		}
		out = append(out, ChangesInEquityRow{
			AccountID:   &id,
			AccountCode: code,
			AccountName: name,
			AccountType: typ,
			Opening:     opening,
			Increase:    increase,
			Decrease:    decrease,
			Closing:     closing,
		})
	}
	return out, rows.Err()
}

// NetSurplusInWindowTx — income − expense for [from, to]. Used by
// Changes-in-Equity to surface "Net surplus for period" as a derived
// equity line (before it's closed into retained earnings).
func (s *ReportStore) NetSurplusInWindowTx(ctx context.Context, tx pgx.Tx, from, to time.Time) (decimal.Decimal, error) {
	var income, expense decimal.Decimal
	err := tx.QueryRow(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN a.class = 'income'  THEN l.credit - l.debit ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN a.class = 'expense' THEN l.debit  - l.credit ELSE 0 END), 0)
		FROM journal_entries je
		JOIN journal_lines l   ON l.entry_id = je.id
		JOIN chart_of_accounts a ON a.id = l.account_id
		WHERE je.status = 'posted'
		  AND je.entry_date BETWEEN $1 AND $2
		  AND a.class IN ('income', 'expense')
	`, from, to).Scan(&income, &expense)
	if err != nil {
		return decimal.Zero, err
	}
	return income.Sub(expense), nil
}

// ─────────── Cash Flow Statement (indirect method) ───────────

// CashFlowRow — one labelled cash-flow line.
type CashFlowRow struct {
	Label    string          `json:"label"`
	Amount   decimal.Decimal `json:"amount"`
	// AccountCodes — accounts that aggregated into this row, for traceability.
	AccountCodes []string `json:"account_codes,omitempty"`
}

// CashFlowSection — operating / investing / financing.
type CashFlowSection struct {
	Name    string          `json:"name"`
	Rows    []CashFlowRow   `json:"rows"`
	Subtotal decimal.Decimal `json:"subtotal"`
}

// CashFlowReport — full statement.
type CashFlowReport struct {
	From            time.Time         `json:"from"`
	To              time.Time         `json:"to"`
	NetSurplus      decimal.Decimal   `json:"net_surplus"`
	Sections        []CashFlowSection `json:"sections"`
	NetChangeInCash decimal.Decimal   `json:"net_change_in_cash"`
	OpeningCash     decimal.Decimal   `json:"opening_cash"`
	ClosingCash     decimal.Decimal   `json:"closing_cash"`
	Reconciles      bool              `json:"reconciles"`
}

// accountChange — closing − opening over the window, in account's
// natural projection (debit balance for asset/expense, credit for the
// rest). Used internally by CashFlowTx to bucket account movements.
type accountChange struct {
	Code        string
	Name        string
	Class       string
	Type        string
	Opening     decimal.Decimal
	Closing     decimal.Decimal
	Delta       decimal.Decimal
}

// CashFlowTx builds an indirect-method cash flow statement.
//
// Mechanics: we read opening + closing balances for every account in
// natural-side projection, then categorize their *deltas* into the
// three sections. For an asset account (natural=debit), a positive
// delta means more was "spent" → cash outflow; for a liability
// (natural=credit), a positive delta means cash came in.
//
// Sign convention used in section rows:
//   Operating add-backs  (depreciation, provisioning):  +Δ(expense)
//   Δ in operating assets:                              -Δ (asset went up = cash out)
//   Δ in operating liabilities:                         +Δ (liability went up = cash in)
//   Investing — fixed asset purchases:                  -Δ(fixed asset)
//   Financing — new capital/deposits:                   +Δ(liability or equity)
//   Financing — dividends paid:                         already in expense, treated as add-back + actual cash out
func (s *ReportStore) CashFlowTx(ctx context.Context, tx pgx.Tx, from, to time.Time) (*CashFlowReport, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.code, a.name, a.class, a.type, a.normal_balance,
		  COALESCE(SUM(CASE WHEN je.entry_date <  $1 THEN l.debit - l.credit ELSE 0 END), 0) AS net_before,
		  COALESCE(SUM(CASE WHEN je.entry_date <= $2 THEN l.debit - l.credit ELSE 0 END), 0) AS net_through
		FROM chart_of_accounts a
		LEFT JOIN journal_lines l ON l.account_id = a.id
		LEFT JOIN journal_entries je ON je.id = l.entry_id AND je.status = 'posted'
		WHERE a.is_active = true
		GROUP BY a.code, a.name, a.class, a.type, a.normal_balance
		ORDER BY a.code
	`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	changes := make([]accountChange, 0, 64)
	for rows.Next() {
		var (
			code, name, class, typ, nb string
			netBefore, netThrough      decimal.Decimal
		)
		if err := rows.Scan(&code, &name, &class, &typ, &nb, &netBefore, &netThrough); err != nil {
			return nil, err
		}
		// Project both balances onto the account's natural side so
		// "positive opening" / "positive closing" both mean the
		// account is in its normal state.
		opening := netBefore
		closing := netThrough
		if nb == "credit" {
			opening = opening.Neg()
			closing = closing.Neg()
		}
		changes = append(changes, accountChange{
			Code:    code,
			Name:    name,
			Class:   class,
			Type:    typ,
			Opening: opening,
			Closing: closing,
			Delta:   closing.Sub(opening),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// ─────── Net surplus from P&L over the window ───────
	netSurplus, err := s.NetSurplusInWindowTx(ctx, tx, from, to)
	if err != nil {
		return nil, err
	}

	// Helpers — look up a change by code.
	byCode := map[string]accountChange{}
	for _, c := range changes {
		byCode[c.Code] = c
	}

	// Cash accounts: 1000, 1010, 1020, 1030, 1040 — the recon check.
	var openingCash, closingCash decimal.Decimal
	cashCodes := []string{"1000", "1010", "1020", "1030", "1040"}
	for _, code := range cashCodes {
		if c, ok := byCode[code]; ok {
			openingCash = openingCash.Add(c.Opening)
			closingCash = closingCash.Add(c.Closing)
		}
	}
	netChangeInCash := closingCash.Sub(openingCash)

	// ─────── Operating activities ───────
	operating := CashFlowSection{Name: "Operating activities", Rows: []CashFlowRow{}}
	operating.Rows = append(operating.Rows, CashFlowRow{
		Label: "Net surplus / (deficit) for the period", Amount: netSurplus,
	})
	operating.Subtotal = netSurplus

	// Non-cash add-backs from expense accounts of type 'non_cash_expense'.
	// These already reduced net surplus; add them back since no cash moved.
	var nonCashAdjustments decimal.Decimal
	var nonCashCodes []string
	for _, c := range changes {
		if c.Class == "expense" && c.Type == "non_cash_expense" {
			// Δ in expense over the window = period activity. We use the
			// closing-only value because expenses reset each fiscal year
			// — for an open year, opening was 0 (or the prior fiscal-year
			// balance not yet closed, which we honour).
			activity := c.Closing.Sub(c.Opening)
			if !activity.IsZero() {
				operating.Rows = append(operating.Rows, CashFlowRow{
					Label:        "Add: " + c.Name,
					Amount:       activity,
					AccountCodes: []string{c.Code},
				})
				nonCashAdjustments = nonCashAdjustments.Add(activity)
				nonCashCodes = append(nonCashCodes, c.Code)
			}
		}
	}
	operating.Subtotal = operating.Subtotal.Add(nonCashAdjustments)

	// Working capital changes — current operating assets/liabilities
	// excluding cash, loans (treated as financing for the SACCO), and
	// fixed assets.
	//   Operating receivables: 1110 (interest), 1200 (other), 1210 (prepay)
	//   Operating payables:    2200 (WHT), 2210 (accrued), 2230 (other), 2240 (unearned), 2250 (variance)
	wcAssets := []string{"1110", "1200", "1210"}
	wcLiab := []string{"2200", "2210", "2220", "2230", "2240", "2250"}

	for _, code := range wcAssets {
		c, ok := byCode[code]
		if !ok || c.Delta.IsZero() {
			continue
		}
		// Asset delta: a positive delta = cash spent → subtract.
		amt := c.Delta.Neg()
		operating.Rows = append(operating.Rows, CashFlowRow{
			Label:        deltaLabel(c.Name, c.Delta, true),
			Amount:       amt,
			AccountCodes: []string{code},
		})
		operating.Subtotal = operating.Subtotal.Add(amt)
	}
	for _, code := range wcLiab {
		c, ok := byCode[code]
		if !ok || c.Delta.IsZero() {
			continue
		}
		// Liability delta: a positive delta = more owed → cash inflow.
		amt := c.Delta
		operating.Rows = append(operating.Rows, CashFlowRow{
			Label:        deltaLabel(c.Name, c.Delta, false),
			Amount:       amt,
			AccountCodes: []string{code},
		})
		operating.Subtotal = operating.Subtotal.Add(amt)
	}

	// ─────── Investing activities ───────
	investing := CashFlowSection{Name: "Investing activities", Rows: []CashFlowRow{}}
	for _, c := range changes {
		if c.Class == "asset" && (c.Type == "fixed_asset" || c.Type == "non_current_asset") {
			if c.Delta.IsZero() {
				continue
			}
			amt := c.Delta.Neg() // asset up = cash out
			investing.Rows = append(investing.Rows, CashFlowRow{
				Label:        deltaLabel(c.Name, c.Delta, true),
				Amount:       amt,
				AccountCodes: []string{c.Code},
			})
			investing.Subtotal = investing.Subtotal.Add(amt)
		}
	}

	// ─────── Financing activities ───────
	financing := CashFlowSection{Name: "Financing activities", Rows: []CashFlowRow{}}
	// Member savings + deposits + long-term liabilities + share capital
	// — a positive delta = funding received → cash inflow.
	financingLiabTypes := map[string]bool{
		"member_savings":       true,
		"member_deposits":      true,
		"long_term_liability":  true,
	}
	for _, c := range changes {
		if c.Class == "liability" && financingLiabTypes[c.Type] && !c.Delta.IsZero() {
			amt := c.Delta
			financing.Rows = append(financing.Rows, CashFlowRow{
				Label:        deltaLabel(c.Name, c.Delta, false),
				Amount:       amt,
				AccountCodes: []string{c.Code},
			})
			financing.Subtotal = financing.Subtotal.Add(amt)
		}
	}
	// Equity movements (excluding retained earnings — that's driven by P&L
	// already counted above). Share capital increases are cash in.
	for _, c := range changes {
		if c.Class == "equity" && c.Type != "retained_earnings" && !c.Delta.IsZero() {
			amt := c.Delta
			financing.Rows = append(financing.Rows, CashFlowRow{
				Label:        deltaLabel(c.Name, c.Delta, false),
				Amount:       amt,
				AccountCodes: []string{c.Code},
			})
			financing.Subtotal = financing.Subtotal.Add(amt)
		}
	}
	// Member-loan portfolio (1100, 1110) — for a SACCO, lending is the
	// core business so changes here belong in financing/investing
	// depending on view. We slot it in financing because loans are
	// funded by member savings (already in financing), making the
	// section self-contained.
	if c, ok := byCode["1100"]; ok && !c.Delta.IsZero() {
		amt := c.Delta.Neg() // more loans receivable = cash out
		financing.Rows = append(financing.Rows, CashFlowRow{
			Label:        deltaLabel(c.Name, c.Delta, true),
			Amount:       amt,
			AccountCodes: []string{"1100"},
		})
		financing.Subtotal = financing.Subtotal.Add(amt)
	}

	totalChange := operating.Subtotal.Add(investing.Subtotal).Add(financing.Subtotal)

	return &CashFlowReport{
		From:            from,
		To:              to,
		NetSurplus:      netSurplus,
		Sections:        []CashFlowSection{operating, investing, financing},
		NetChangeInCash: totalChange,
		OpeningCash:     openingCash,
		ClosingCash:     closingCash,
		Reconciles:      totalChange.Equal(netChangeInCash),
	}, nil
}

// deltaLabel — "Increase in X" vs "Decrease in X" based on direction.
// For assets, "Increase" is a cash outflow (the sign on the amount
// reflects this). For liabilities, "Increase" is a cash inflow.
func deltaLabel(name string, delta decimal.Decimal, isAsset bool) string {
	dir := "Increase"
	if delta.IsNegative() {
		dir = "Decrease"
	}
	_ = isAsset
	return dir + " in " + name
}
