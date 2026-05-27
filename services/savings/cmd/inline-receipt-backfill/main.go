// Inline-receipt backfill — synthesises receipts + receipt_lines for
// historical subledger txns (share_transactions / deposit_transactions /
// loan_transactions) that pre-date the inline-receipt fix.
//
// Why: before the receiptops PR, inline panels (Member 360 → Buy
// shares / Deposit / Withdraw / Repay) wrote subledger rows but no
// receipt row. "Today's receipts" therefore missed every inline
// transaction. This tool walks historic txns and creates the missing
// receipts so the desk view + reconciliation reports become complete.
//
// What this tool does NOT do:
//
//   - Post to the GL. Backfilling journal entries would shift the
//     trial balance on every report — that needs auditor sign-off,
//     not a script. The dry-run count surfaces unallocated journal
//     entries so operators have the number to take to their auditor.
//
//   - Touch cash-channel txns. The receipts table requires a
//     till_session_id for cash; there's no historic till to attribute
//     to. Cash txns are counted + skipped.
//
//   - Touch txns whose receipt already exists (joined via
//     receipt_lines.posted_txn_id). Idempotent on repeated runs.
//
// Usage:
//
//   inline-receipt-backfill                     # dry-run: counts only
//   inline-receipt-backfill --apply             # insert missing receipts
//   inline-receipt-backfill --apply --gl-backfill  # ALSO queue missing GL outbox rows for opening_balance txns
//   inline-receipt-backfill --since 2026-01-01  # cutoff date
//   inline-receipt-backfill --tenant <slug>     # narrow to one tenant
//
// Safety:
//   • --apply is the explicit opt-in. Default exit is dry-run with
//     per-tenant counts printed to stdout.
//   • --gl-backfill defaults to OFF. Auditor signs off before
//     flipping it on a tenant — replaying historical opening_balance
//     txns through the GL outbox shifts the trial balance.
//   • The opening_balance dedup keys on
//     (source_module='savings.deposits.opening', source_ref=<txn_id>);
//     accounting's UNIQUE constraint makes re-running safe.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/config"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/receiptops"
	"github.com/nexussacco/savings/internal/store"
)

type counts struct {
	shareMissingReceipt   int
	depositMissingReceipt int
	loanMissingReceipt    int
	openingMissingReceipt int
	openingMissingGL      int
	openingGLQueued       int
	cashSkipped           int
	createdReceipts       int
	missingJEs            int
}

func main() {
	apply := flag.Bool("apply", false, "actually insert synthetic receipts (default: dry-run, count only)")
	glBackfill := flag.Bool("gl-backfill", false,
		"ALSO queue missing GL outbox rows for opening_balance txns. Default OFF — auditor signs off before flipping. Requires --apply.")
	sinceStr := flag.String("since", "2025-01-01", "ignore txns created before this date (YYYY-MM-DD)")
	tenantSlug := flag.String("tenant", "", "narrow to a single tenant slug (default: all tenants)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	// Bypass nexus_app role so the cross-tenant walk can SELECT
	// without per-tenant RLS gymnastics. Matches the migration
	// runner's pattern.
	_ = os.Setenv("DB_SKIP_SET_ROLE", "1")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	since, err := time.Parse("2006-01-02", *sinceStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "since must be YYYY-MM-DD:", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect db:", err)
		os.Exit(1)
	}
	defer pool.Close()

	tenants, err := listTenants(ctx, pool, *tenantSlug)
	if err != nil {
		fmt.Fprintln(os.Stderr, "list tenants:", err)
		os.Exit(1)
	}
	if len(tenants) == 0 {
		fmt.Println("no tenants matched the filter")
		return
	}

	deps := receiptops.Deps{
		Receipts:     store.NewReceiptStore(pool.Pool),
		VirtualTills: store.NewVirtualTillStore(pool.Pool),
	}

	grand := counts{}
	for _, t := range tenants {
		log := logger.With("tenant_slug", t.slug, "tenant_id", t.id)
		c, err := runForTenant(ctx, pool, deps, t.id, since, *apply, *glBackfill, log)
		if err != nil {
			log.Error("tenant backfill failed", "err", err)
			continue
		}
		fmt.Printf("\n── %s ──────────────────────────────\n", t.slug)
		fmt.Printf("  share_transactions missing receipts:   %d\n", c.shareMissingReceipt)
		fmt.Printf("  deposit_transactions missing receipts: %d\n", c.depositMissingReceipt)
		fmt.Printf("  loan_transactions missing receipts:    %d\n", c.loanMissingReceipt)
		fmt.Printf("  cash-channel txns skipped (no till):   %d\n", c.cashSkipped)
		fmt.Printf("  subledger txns missing journal_entry:  %d  (NOT backfilled — needs auditor sign-off)\n", c.missingJEs)
		fmt.Printf("  opening_balance txns missing receipts: %d\n", c.openingMissingReceipt)
		fmt.Printf("  opening_balance txns missing GL row:   %d  (gl-backfill needed)\n", c.openingMissingGL)
		if *apply {
			fmt.Printf("  RECEIPTS CREATED:                      %d\n", c.createdReceipts)
			if *glBackfill {
				fmt.Printf("  OPENING GL ROWS QUEUED:                %d\n", c.openingGLQueued)
			}
		} else {
			fmt.Printf("  (dry-run — pass --apply to insert receipts; add --gl-backfill to queue opening GL rows)\n")
		}
		grand.shareMissingReceipt += c.shareMissingReceipt
		grand.depositMissingReceipt += c.depositMissingReceipt
		grand.loanMissingReceipt += c.loanMissingReceipt
		grand.openingMissingReceipt += c.openingMissingReceipt
		grand.openingMissingGL += c.openingMissingGL
		grand.openingGLQueued += c.openingGLQueued
		grand.cashSkipped += c.cashSkipped
		grand.createdReceipts += c.createdReceipts
		grand.missingJEs += c.missingJEs
	}
	fmt.Println("\n────────────── totals ──────────────")
	fmt.Printf("share missing receipts:    %d\n", grand.shareMissingReceipt)
	fmt.Printf("deposit missing receipts:  %d\n", grand.depositMissingReceipt)
	fmt.Printf("loan missing receipts:     %d\n", grand.loanMissingReceipt)
	fmt.Printf("opening missing receipts:  %d\n", grand.openingMissingReceipt)
	fmt.Printf("opening missing GL rows:   %d\n", grand.openingMissingGL)
	fmt.Printf("cash skipped:              %d\n", grand.cashSkipped)
	fmt.Printf("missing journal entries:   %d\n", grand.missingJEs)
	if *apply {
		fmt.Printf("RECEIPTS CREATED:          %d\n", grand.createdReceipts)
		if *glBackfill {
			fmt.Printf("OPENING GL ROWS QUEUED:    %d\n", grand.openingGLQueued)
		}
	}
}

type tenantRow struct {
	id   uuid.UUID
	slug string
}

func listTenants(ctx context.Context, pool *db.Pool, slug string) ([]tenantRow, error) {
	query := `SELECT id, slug FROM tenants WHERE status='active'`
	args := []any{}
	if slug != "" {
		query += ` AND slug = $1`
		args = append(args, slug)
	}
	query += ` ORDER BY slug`
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tenantRow
	for rows.Next() {
		var t tenantRow
		if err := rows.Scan(&t.id, &t.slug); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func runForTenant(ctx context.Context, pool *db.Pool, deps receiptops.Deps, tenantID uuid.UUID, since time.Time, apply, glBackfill bool, log *slog.Logger) (counts, error) {
	c := counts{}

	// share_transactions: every 'purchase' txn since the cutoff that
	// has no receipt_line pointing at it.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT st.id, st.counterparty_id, st.amount, st.payment_channel, st.payment_ref, st.posted_at
			  FROM share_transactions st
			 WHERE st.tenant_id = $1
			   AND st.txn_type = 'purchase'
			   AND st.created_at >= $2
			   AND NOT EXISTS (
			       SELECT 1 FROM receipt_lines rl
			        WHERE rl.posted_txn_id = st.id
			   )
		`, tenantID, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				txnID  uuid.UUID
				cpID   uuid.UUID
				amount string
				ch     string
				ref    *string
				ts     time.Time
			)
			if err := rows.Scan(&txnID, &cpID, &amount, &ch, &ref, &ts); err != nil {
				return err
			}
			c.shareMissingReceipt++
			if ch == "cash" {
				c.cashSkipped++
				continue
			}
			if !apply {
				continue
			}
			if err := synthesiseReceipt(ctx, tx, deps, tenantID, cpID, txnID,
				domain.LineSharePurchase, domain.ReceiptChannel(ch), strOrEmpty(ref), amount, ts,
				"backfill_inline_share_purchase", log); err != nil {
				return err
			}
			c.createdReceipts++
		}
		return rows.Err()
	}); err != nil {
		return c, fmt.Errorf("share walk: %w", err)
	}

	// deposit_transactions: 'deposit' txn type, same pattern.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT dt.id, dt.counterparty_id, da.id AS account_id, dt.amount, dt.channel, dt.channel_ref, dt.posted_at
			  FROM deposit_transactions dt
			  JOIN deposit_accounts da ON da.id = dt.account_id
			 WHERE dt.tenant_id = $1
			   AND dt.txn_type = 'deposit'
			   AND dt.created_at >= $2
			   AND NOT EXISTS (
			       SELECT 1 FROM receipt_lines rl WHERE rl.posted_txn_id = dt.id
			   )
		`, tenantID, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				txnID  uuid.UUID
				cpID   uuid.UUID
				acctID uuid.UUID
				amount string
				ch     string
				ref    *string
				ts     time.Time
			)
			if err := rows.Scan(&txnID, &cpID, &acctID, &amount, &ch, &ref, &ts); err != nil {
				return err
			}
			c.depositMissingReceipt++
			if ch == "cash" {
				c.cashSkipped++
				continue
			}
			if !apply {
				continue
			}
			if err := synthesiseReceipt(ctx, tx, deps, tenantID, cpID, txnID,
				domain.LineSavingsDeposit, domain.ReceiptChannel(ch), strOrEmpty(ref), amount, ts,
				"backfill_inline_deposit", log); err != nil {
				return err
			}
			c.createdReceipts++
		}
		return rows.Err()
	}); err != nil {
		return c, fmt.Errorf("deposit walk: %w", err)
	}

	// loan_transactions: 'repayment' txn type.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT lt.id, lt.counterparty_id, lt.loan_id, lt.amount, lt.channel, lt.channel_ref, lt.posted_at
			  FROM loan_transactions lt
			 WHERE lt.tenant_id = $1
			   AND lt.txn_type = 'repayment'
			   AND lt.posted_at >= $2
			   AND NOT EXISTS (
			       SELECT 1 FROM receipt_lines rl WHERE rl.posted_txn_id = lt.id
			   )
		`, tenantID, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				txnID  uuid.UUID
				cpID   uuid.UUID
				loanID uuid.UUID
				amount string
				ch     *string
				ref    *string
				ts     time.Time
			)
			if err := rows.Scan(&txnID, &cpID, &loanID, &amount, &ch, &ref, &ts); err != nil {
				return err
			}
			c.loanMissingReceipt++
			chStr := strOrEmpty(ch)
			if chStr == "cash" || chStr == "teller" || chStr == "auto_savings" || chStr == "" {
				c.cashSkipped++
				continue
			}
			if !apply {
				continue
			}
			// Map repayment channel string to ReceiptChannel.
			rc := mapRepaymentChannel(chStr)
			if rc == "" {
				c.cashSkipped++
				continue
			}
			if err := synthesiseReceipt(ctx, tx, deps, tenantID, cpID, txnID,
				domain.LineLoanRepayment, rc, strOrEmpty(ref), amount, ts,
				"backfill_inline_loan_repayment", log); err != nil {
				return err
			}
			c.createdReceipts++
		}
		return rows.Err()
	}); err != nil {
		return c, fmt.Errorf("loan walk: %w", err)
	}

	// Count subledger txns with NULL journal_entry_id (share_transactions
	// is the only table with this column today). Surface only — never
	// auto-post.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM share_transactions
			 WHERE tenant_id = $1
			   AND journal_entry_id IS NULL
			   AND created_at >= $2
		`, tenantID, since).Scan(&c.missingJEs)
	}); err != nil {
		return c, fmt.Errorf("missing-JE count: %w", err)
	}

	// ─── opening_balance walk ───
	// deposit_transactions WHERE txn_type='opening_balance' that have
	// no receipt + (optionally) no posting_outbox row. The pre-fix
	// Open handler + the application_store BOSA path both produced
	// these — neither wrote a receipt or queued the GL.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT dt.id, dt.counterparty_id, dt.account_id, dt.amount,
			       dt.channel, dt.channel_ref, dt.posted_at
			  FROM deposit_transactions dt
			 WHERE dt.tenant_id = $1
			   AND dt.txn_type = 'opening_balance'
			   AND dt.posted_at >= $2
			   AND NOT EXISTS (
			       SELECT 1 FROM receipt_lines rl WHERE rl.posted_txn_id = dt.id
			   )
		`, tenantID, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				txnID  uuid.UUID
				cpID   uuid.UUID
				acctID uuid.UUID
				amount string
				ch     *string
				ref    *string
				ts     time.Time
			)
			if err := rows.Scan(&txnID, &cpID, &acctID, &amount, &ch, &ref, &ts); err != nil {
				return err
			}
			c.openingMissingReceipt++
			chStr := strOrEmpty(ch)
			rc := domain.ReceiptChannel("")
			if chStr != "" {
				rc = domain.ReceiptChannel(chStr)
			}
			if chStr == "cash" {
				c.cashSkipped++
				continue
			}
			if !apply {
				continue
			}
			// Receipt write — best-effort. Channels not representable
			// on the receipts table (internal/payroll/empty) silently
			// skip via receiptops.ErrUnsupportedChannel.
			if rc != "" {
				if err := synthesiseReceipt(ctx, tx, deps, tenantID, cpID, txnID,
					domain.LineSavingsDeposit, rc, strOrEmpty(ref), amount, ts,
					"backfill_opening_balance", log); err != nil {
					return err
				}
				c.createdReceipts++
			}
		}
		return rows.Err()
	}); err != nil {
		return c, fmt.Errorf("opening_balance walk: %w", err)
	}

	// ─── opening_balance GL count + (optionally) backfill ───
	// Reads opening_balance txns that have no posting_outbox row
	// (source_module='savings.deposits.opening', source_ref=<txn_id>).
	// Counts always; only writes when --gl-backfill is set.
	if err := pool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT dt.id, dt.tenant_id, dt.account_id, dt.counterparty_id,
			       dt.amount, dt.channel::text, dt.narration, dt.posted_at,
			       da.product_id
			  FROM deposit_transactions dt
			  JOIN deposit_accounts     da ON da.id = dt.account_id
			 WHERE dt.tenant_id = $1
			   AND dt.txn_type = 'opening_balance'
			   AND dt.posted_at >= $2
			   AND NOT EXISTS (
			       SELECT 1 FROM posting_outbox po
			        WHERE po.tenant_id    = dt.tenant_id
			          AND po.payload->>'source_module' = 'savings.deposits.opening'
			          AND po.payload->>'source_ref'    = dt.id::text
			   )
		`, tenantID, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				txnID     uuid.UUID
				tID       uuid.UUID
				acctID    uuid.UUID
				cpID      uuid.UUID
				amountStr string
				channel   *string
				narr      *string
				ts        time.Time
				productID uuid.UUID
			)
			if err := rows.Scan(&txnID, &tID, &acctID, &cpID, &amountStr, &channel, &narr, &ts, &productID); err != nil {
				return err
			}
			c.openingMissingGL++
			if !apply || !glBackfill {
				continue
			}
			amt, parseErr := decimal.NewFromString(amountStr)
			if parseErr != nil {
				log.Warn("opening_balance gl-backfill: bad amount", "txn_id", txnID, "amount", amountStr)
				continue
			}
			liab := openingLiabilityCode(strOrEmpty(channel))
			cashAcct := openingCashCode(strOrEmpty(channel))
			n := "Opening balance backfill"
			if narr != nil && *narr != "" {
				n = *narr
			}
			payload := buildOpeningOutboxPayload(tID, ts, txnID, n, cashAcct, liab, amt)
			if _, err := tx.Exec(ctx, `
				INSERT INTO posting_outbox (tenant_id, payload)
				VALUES ($1, $2::jsonb)
			`, tID, payload); err != nil {
				log.Warn("opening_balance gl-backfill: outbox insert failed",
					"txn_id", txnID, "err", err)
				continue
			}
			c.openingGLQueued++
			_ = acctID
			_ = cpID
			_ = productID
		}
		return rows.Err()
	}); err != nil {
		return c, fmt.Errorf("opening_balance GL walk: %w", err)
	}

	return c, nil
}

// openingCashCode mirrors finance/executor's per-channel debit
// account map. "" → 1099 (internal-suspense, for application-
// activation BOSA openings with no teller event).
func openingCashCode(channel string) string {
	switch channel {
	case "":
		return "1099"
	case "mpesa":
		return "1030"
	case "airtel_money":
		return "1040"
	case "bank_transfer", "standing_order", "direct_debit", "payroll":
		return "1020"
	}
	return "1000"
}

// openingLiabilityCode falls back to ordinary savings (2000). The
// backfill doesn't re-derive per-product; the operator can
// post-process the outbox before dispatch if a tenant's BOSA
// segment needs the 2050 code (a one-line UPDATE).
func openingLiabilityCode(channel string) string {
	_ = channel
	return "2000"
}

// buildOpeningOutboxPayload mirrors the JSON shape posting.Client
// writes when queuing an opening-deposit JE: tenant_id +
// source_module + source_ref + lines. Accounting's dedup uses
// (source_module, source_ref).
func buildOpeningOutboxPayload(tenantID uuid.UUID, ts time.Time, txnID uuid.UUID, narr, cashAcct, liabAcct string, amount decimal.Decimal) []byte {
	type line struct {
		AccountCode string `json:"account_code"`
		Debit       string `json:"debit,omitempty"`
		Credit      string `json:"credit,omitempty"`
		Narration   string `json:"narration,omitempty"`
	}
	type payload struct {
		TenantID     string `json:"tenant_id"`
		EntryDate    string `json:"entry_date,omitempty"`
		ValueDate    string `json:"value_date,omitempty"`
		SourceModule string `json:"source_module"`
		SourceRef    string `json:"source_ref"`
		Narration    string `json:"narration"`
		Lines        []line `json:"lines"`
	}
	p := payload{
		TenantID:     tenantID.String(),
		EntryDate:    ts.Format("2006-01-02"),
		ValueDate:    ts.Format("2006-01-02"),
		SourceModule: "savings.deposits.opening",
		SourceRef:    txnID.String(),
		Narration:    narr,
		Lines: []line{
			{AccountCode: cashAcct, Debit: amount.StringFixed(2), Narration: "Opening cash leg (backfill)"},
			{AccountCode: liabAcct, Credit: amount.StringFixed(2), Narration: "Member savings credited (backfill)"},
		},
	}
	b, _ := jsonImportMarshal(p)
	return b
}

// jsonImportMarshal is the actual marshaller — split out so the
// import-encoding boundary is explicit.
func jsonImportMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// synthesiseReceipt is the single-line writer the backfill uses. We
// borrow the inline panels' fake cashier_user_id (a stable per-tenant
// "BACKFILL" UUID) so the rows are recognisable in reports.
func synthesiseReceipt(
	ctx context.Context, tx pgx.Tx, deps receiptops.Deps,
	tenantID, counterpartyID, txnID uuid.UUID,
	kind domain.ReceiptLineKind, channel domain.ReceiptChannel,
	channelRef string, amount string, ts time.Time, source string,
	log *slog.Logger,
) error {
	// Use a fixed UUID per tenant so the backfill rows cluster on the
	// "cashier" axis of the desk view — easy to spot in reports.
	backfillUser := uuid.NewSHA1(uuid.NameSpaceOID, []byte("inline-receipt-backfill:"+tenantID.String()))
	amt, err := parseDecimal(amount)
	if err != nil {
		return err
	}
	_, err = receiptops.WriteTx(ctx, tx, deps, receiptops.WriteInput{
		TenantID:       tenantID,
		CounterpartyID: counterpartyID,
		CashierUserID:  backfillUser,
		Channel:        channel,
		ChannelRef:     channelRef,
		ChannelAmount:  amt,
		ValueDate:      ts,
		Narration:      "Backfill: missing receipt for " + string(kind),
		Source:         source,
		HeaderStatus:   domain.ReceiptPosted,
		Lines: []receiptops.LineInput{{
			Kind:        kind,
			Amount:      amt,
			Status:      domain.LinePosted,
			PostedTxnID: &txnID,
		}},
	})
	if err != nil {
		log.Warn("backfill receipt skipped", "txn_id", txnID, "err", err)
		// Swallow — duplicate-channel-ref + unsupported-channel both
		// surface here; the run continues.
		return nil
	}
	return nil
}

func mapRepaymentChannel(s string) domain.ReceiptChannel {
	switch s {
	case "mpesa":
		return domain.RCMpesa
	case "airtel_money":
		return domain.RCAirtelMoney
	case "bank", "bank_transfer":
		return domain.RCBankTransfer
	case "cheque":
		return domain.RCCheque
	case "standing_order":
		return domain.RCStandingOrder
	}
	return ""
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func parseDecimal(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}
