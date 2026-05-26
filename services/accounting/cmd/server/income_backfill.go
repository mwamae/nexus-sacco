// One-off ops tool: per-transaction backfill of missing GL entries
// for income-bearing handlers that lost their post pre-outbox.
//
// Covers (in order, idempotent on re-run):
//
//   0. Reverse any prior ops.backfill JEs (the net-adjustment
//      backfills from earlier today) so per-tx posts don't
//      double-count.
//   1. Receipt fees (collection desk) — missing source_module
//      'savings.collection_desk.fees'.
//   2. Application registration fees — missing
//      'member.application.fee'.
//   3. Loan disbursements — missing 'savings.loans.disbursement'.
//   4. Loan repayments — missing 'savings.loans.repayment'.
//   5. Interest runs — missing 'savings.interest'.
//
// Each category reconstructs the JE shape from the surviving
// subledger data + mirrors the savings-handler GL helpers so the
// resulting entries are indistinguishable from what the original
// handler WOULD have posted. Engine.PostTx is the post path — same
// dedup, same period gates as production.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/db"
	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/posting"
	"github.com/nexussacco/accounting/internal/store"
)

// runIncomeBackfill orchestrates the categorical backfill. Dry-run
// prints proposed JEs; real run calls engine.PostTx for each.
func runIncomeBackfill(
	ctx context.Context,
	pool *db.Pool,
	tenants *store.TenantStore,
	engine *posting.Engine,
	tenantSlug string,
	dryRun bool,
	logger *slog.Logger,
) {
	mode := "DRY-RUN"
	if !dryRun {
		mode = "EXECUTE"
	}
	logger.Info("income-backfill: starting", "tenant", tenantSlug, "mode", mode)

	t, err := tenants.BySlug(ctx, tenantSlug)
	if err != nil {
		logger.Error("income-backfill: tenant lookup", "err", err)
		os.Exit(1)
	}

	bf := &backfillCtx{
		ctx:     ctx,
		pool:    pool,
		engine:  engine,
		tenant:  t.ID,
		dryRun:  dryRun,
		logger:  logger,
	}

	bf.reverseOpsBackfill()
	bf.receiptFees()
	bf.applicationFees()
	bf.loanDisbursements()
	bf.loanRepayments()
	bf.interestRuns()
	bf.depositTransactions()
	bf.sharePurchases()
	bf.shareBonusIssues()
	bf.shareAdjustments()
	// share redemptions: SKIPPED — R5 explicitly removed the
	// redemption path (share capital is equity per Co-op Societies
	// Act). The 3 historical redemption rows on tujenge are
	// anomalous data; flagged separately rather than auto-backfilled.
	bf.flagAnomalousShareRedemptions()
	// share transfers: NO GL (equity-class-internal, by design).

	fmt.Fprintf(os.Stdout, "\n==== Income backfill %s ====\n", mode)
	fmt.Fprintf(os.Stdout, "Tenant: %s · JEs proposed: %d · JEs posted: %d · skipped (already present): %d\n",
		tenantSlug, bf.proposedCount, bf.postedCount, bf.skippedCount)
	if dryRun {
		fmt.Fprintln(os.Stdout, "DRY-RUN — re-run with -income-backfill-dry-run=false to commit.")
	}
}

type backfillCtx struct {
	ctx     context.Context
	pool    *db.Pool
	engine  *posting.Engine
	tenant  uuid.UUID
	dryRun  bool
	logger  *slog.Logger
	proposedCount, postedCount, skippedCount int
}

// post is the shared end-of-pipeline: log + dry-run skip + engine call.
// fyStart sets entry_date; period gate fires inside engine.
func (b *backfillCtx) post(category, sourceModule, sourceRef, narration string, lines []posting.Line, entryDate time.Time) {
	b.proposedCount++
	// Format line summary
	sum := make([]string, 0, len(lines))
	for _, l := range lines {
		switch {
		case !l.Debit.IsZero():
			sum = append(sum, fmt.Sprintf("DR %s %s", l.AccountCode, l.Debit.StringFixed(2)))
		case !l.Credit.IsZero():
			sum = append(sum, fmt.Sprintf("CR %s %s", l.AccountCode, l.Credit.StringFixed(2)))
		}
	}
	b.logger.Info(category,
		"source", sourceModule+"/"+sourceRef,
		"entry_date", entryDate.Format("2006-01-02"),
		"lines", strings.Join(sum, " · "))
	if b.dryRun {
		return
	}
	if err := b.pool.WithTenantTx(b.ctx, b.tenant, func(tx pgx.Tx) error {
		_, perr := b.engine.PostTx(b.ctx, tx, posting.PostInput{
			EntryDate:    entryDate,
			ValueDate:    entryDate,
			EntryType:    domain.TypeAuto,
			SourceModule: sourceModule,
			SourceRef:    sourceRef,
			Narration:    narration,
			Lines:        lines,
		})
		return perr
	}); err != nil {
		// Dedup-aware: if the post returns "already exists" (because
		// a prior run posted the same source_ref), skip silently.
		es := err.Error()
		if strings.Contains(es, "duplicate") || strings.Contains(es, "already") {
			b.skippedCount++
			b.proposedCount--
			return
		}
		b.logger.Error(category+": post failed", "ref", sourceRef, "err", err)
		os.Exit(1)
	}
	b.postedCount++
}

// alreadyPosted returns true if a JE with (source_module, source_ref)
// already exists. Cheap dedup check — runs inside a tenant-scoped tx
// so RLS doesn't hide existing rows.
func (b *backfillCtx) alreadyPosted(sourceModule, sourceRef string) bool {
	var n int
	_ = b.pool.WithTenantTx(b.ctx, b.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(b.ctx, `
			SELECT count(*) FROM journal_entries
			 WHERE source_module = $1 AND source_ref = $2 AND status = 'posted'
		`, sourceModule, sourceRef).Scan(&n)
	})
	return n > 0
}

// scoped runs a query function inside a tenant-scoped tx so RLS
// returns the right rows. Use for every read-side query.
func (b *backfillCtx) scoped(fn func(tx pgx.Tx) error) {
	if err := b.pool.WithTenantTx(b.ctx, b.tenant, fn); err != nil {
		b.logger.Error("scoped query failed", "err", err)
		os.Exit(1)
	}
}

// ─────────── Phase 0: reverse prior ops.backfill ───────────

func (b *backfillCtx) reverseOpsBackfill() {
	type reverseLine struct {
		AccountCode string
		Debit, Credit decimal.Decimal
		LineNo int
	}
	type origJE struct {
		ID uuid.UUID
		EntryNo, SourceRef string
		EntryDate time.Time
		Lines []reverseLine
	}
	byJE := map[uuid.UUID]*origJE{}
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT je.id, je.entry_no, je.source_ref, je.entry_date,
			       l.account_id, ca.code, l.debit, l.credit, l.line_no
			  FROM journal_entries je
			  JOIN journal_lines l ON l.entry_id = je.id
			  JOIN chart_of_accounts ca ON ca.id = l.account_id
			 WHERE je.source_module = 'ops.backfill'
			   AND je.status = 'posted'
			 ORDER BY je.entry_no, l.line_no
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var jid uuid.UUID
			var entryNo, sref, code string
			var entryDate time.Time
			var aid uuid.UUID
			var debit, credit decimal.Decimal
			var lineNo int
			if err := rows.Scan(&jid, &entryNo, &sref, &entryDate, &aid, &code, &debit, &credit, &lineNo); err != nil {
				return err
			}
			o := byJE[jid]
			if o == nil {
				o = &origJE{ID: jid, EntryNo: entryNo, SourceRef: sref, EntryDate: entryDate}
				byJE[jid] = o
			}
			o.Lines = append(o.Lines, reverseLine{
				AccountCode: code, Debit: debit, Credit: credit, LineNo: lineNo,
			})
		}
		return rows.Err()
	})

	for _, o := range byJE {
		sourceRef := "reverse-" + o.SourceRef
		if b.alreadyPosted("ops.backfill.reverse", sourceRef) {
			b.skippedCount++
			continue
		}
		lines := make([]posting.Line, 0, len(o.Lines))
		for _, l := range o.Lines {
			// Swap DR/CR
			lines = append(lines, posting.Line{
				AccountCode: l.AccountCode,
				Debit:       l.Credit,
				Credit:      l.Debit,
				Narration:   "Reverse of " + o.EntryNo,
			})
		}
		b.post("reverse", "ops.backfill.reverse", sourceRef,
			"Reverse of "+o.EntryNo+" ("+o.SourceRef+") — replaced by per-transaction backfill",
			lines, o.EntryDate)
	}
}

// ─────────── Helpers: mirror savings handler GL mappings ───────────

// channelCashAccount mirrors deposit.go::channelCashAccount.
func channelCashAccount(channel string) string {
	switch strings.ToLower(channel) {
	case "mpesa":
		return "1030"
	case "airtel_money", "airtel money":
		return "1040"
	case "bank_transfer", "bank", "payroll", "standing_order":
		return "1020"
	default:
		return "1000"
	}
}

// loanRepaymentCashAccount mirrors loan_repayment.go::repaymentCashAccount.
func loanRepaymentCashAccount(channel string) string {
	switch strings.ToLower(channel) {
	case "mpesa":
		return "1030"
	case "bank", "bank_transfer":
		return "1020"
	case "auto_savings":
		return "2000" // member's savings — credited liability goes down via DR
	case "payroll":
		return "1020"
	default:
		return "1000"
	}
}

// loanDisbursementCashAccount mirrors loan.go pre-R5 default mapping
// (channel only, no segment-aware resolution since historical loans
// didn't have product context captured for internal disbursements).
func loanDisbursementCashAccount(channel string) string {
	switch strings.ToLower(channel) {
	case "mpesa":
		return "1030"
	case "bank", "bank_transfer":
		return "1020"
	case "internal", "savings":
		return "2000" // member's savings receives the disbursement
	default:
		return "1000"
	}
}

// depositLiabilityCode mirrors deposit.go::depositLiabilityCode.
func depositLiabilityCode(segment, productType string) string {
	if segment == "bosa" {
		return "2050"
	}
	switch productType {
	case "ordinary":
		return "2000"
	case "holiday":
		return "2010"
	case "emergency":
		return "2020"
	case "goal":
		return "2030"
	case "junior":
		return "2040"
	case "fixed":
		return "2100"
	case "group":
		return "2000"
	case "member_deposit":
		return "2050"
	}
	return "2000"
}

// ─────────── Category 1: receipt fees ───────────

func (b *backfillCtx) receiptFees() {
	type feeRow struct {
		LineID, PostedTxn uuid.UUID
		Serial, Channel, FeeCode string
		GLCredit, Label *string
		Amount decimal.Decimal
		ValueDate time.Time
		TillSession, VirtualTill *uuid.UUID
		DebitAccount string
	}
	var items []feeRow
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT rl.id, rl.posted_txn_id, r.serial, r.channel::text, rl.amount,
			       rl.fee_code, fc.gl_credit_code, fc.label,
			       r.value_date,
			       r.till_session_id, r.virtual_till_id
			  FROM receipt_lines rl
			  JOIN receipts r ON r.id = rl.receipt_id
			  LEFT JOIN fee_catalog fc ON fc.tenant_id = r.tenant_id AND fc.code = rl.fee_code
			 WHERE rl.kind IN ('fee','welfare')
			   AND r.status = 'posted'
			   AND rl.voided_at IS NULL
			   AND rl.posted_txn_id IS NOT NULL
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.collection_desk.fees'
			        AND je.source_ref = rl.posted_txn_id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f feeRow
			if err := rows.Scan(&f.LineID, &f.PostedTxn, &f.Serial, &f.Channel, &f.Amount,
				&f.FeeCode, &f.GLCredit, &f.Label, &f.ValueDate, &f.TillSession, &f.VirtualTill); err != nil {
				return err
			}
			// Resolve cash-side account inside the same scoped tx so the
			// till_session / virtual_till lookups RLS-pass.
			f.DebitAccount = channelCashAccount(f.Channel)
			if f.Channel == "cash" && f.TillSession != nil {
				var code string
				if err := tx.QueryRow(b.ctx,
					`SELECT t.gl_account_code FROM till_sessions s JOIN tills t ON t.id=s.till_id WHERE s.id=$1`,
					*f.TillSession,
				).Scan(&code); err == nil && code != "" {
					f.DebitAccount = code
				}
			} else if f.VirtualTill != nil {
				var code string
				if err := tx.QueryRow(b.ctx,
					`SELECT gl_account_code FROM virtual_tills WHERE id=$1`, *f.VirtualTill,
				).Scan(&code); err == nil && code != "" {
					f.DebitAccount = code
				}
			}
			items = append(items, f)
		}
		return rows.Err()
	})
	for _, f := range items {
		if f.GLCredit == nil || *f.GLCredit == "" {
			b.logger.Warn("receipt fees: no gl_credit_code on fee_catalog, skipping",
				"line", f.LineID, "fee_code", f.FeeCode)
			continue
		}
		labelStr := f.FeeCode
		if f.Label != nil && *f.Label != "" {
			labelStr = *f.Label
		}
		b.post("receipt-fees",
			"savings.collection_desk.fees", f.PostedTxn.String(),
			fmt.Sprintf("Backfill receipt %s · fee %s — original post lost pre-collection-desk-outbox", f.Serial, f.FeeCode),
			[]posting.Line{
				{AccountCode: f.DebitAccount, Debit: f.Amount, Narration: "Cash in via " + f.Channel},
				{AccountCode: *f.GLCredit, Credit: f.Amount, Narration: labelStr},
			}, f.ValueDate)
	}
}

// ─────────── Category 2: application registration fees ───────────

func (b *backfillCtx) applicationFees() {
	type appFee struct {
		ID       uuid.UUID
		Amount   decimal.Decimal
		Channel  string
		ValueDate time.Time
		AppNo    string
	}
	var fees []appFee
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT afp.id, afp.amount, afp.channel, afp.value_date, ma.application_no
			  FROM application_fee_payments afp
			  JOIN membership_applications ma ON ma.id = afp.application_id
			 WHERE afp.voided_at IS NULL
			   AND afp.journal_entry_id IS NULL
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f appFee
			if err := rows.Scan(&f.ID, &f.Amount, &f.Channel, &f.ValueDate, &f.AppNo); err != nil {
				return err
			}
			fees = append(fees, f)
		}
		return rows.Err()
	})
	for _, f := range fees {
		// jeID derived from payment id so re-runs dedupe.
		jeID := fmt.Sprintf("backfill-app-fee-%s", f.ID)
		if b.alreadyPosted("member.application.fee", jeID) {
			b.skippedCount++
			continue
		}
		cashAcct := channelCashAccount(f.Channel)
		b.post("application-fees",
			"member.application.fee", jeID,
			fmt.Sprintf("Backfill registration fee for application %s — original post lost pre-outbox", f.AppNo),
			[]posting.Line{
				{AccountCode: cashAcct, Debit: f.Amount, Narration: "Cash in via " + f.Channel},
				{AccountCode: "4080", Credit: f.Amount, Narration: "Registration fee income"},
			}, f.ValueDate)

		// Stamp journal_entry_id on the payment row so it stops
		// showing up as "missing" next time.
		if !b.dryRun {
			_ = b.pool.WithTenantTx(b.ctx, b.tenant, func(tx pgx.Tx) error {
				var jeUUID uuid.UUID
				if err := tx.QueryRow(b.ctx, `
					SELECT id FROM journal_entries
					 WHERE source_module='member.application.fee' AND source_ref=$1
				`, jeID).Scan(&jeUUID); err != nil {
					return err
				}
				_, err := tx.Exec(b.ctx, `UPDATE application_fee_payments SET journal_entry_id=$2 WHERE id=$1`, f.ID, jeUUID)
				return err
			})
		}
	}
}

// ─────────── Category 3: loan disbursements ───────────

func (b *backfillCtx) loanDisbursements() {
	type disb struct {
		LoanID, TxnID uuid.UUID
		LoanNo        string
		Principal, Fees, Net decimal.Decimal
		Channel string
		DisbursedAt time.Time
	}
	var dlist []disb
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT l.id, l.loan_no, lt.id AS disbursement_txn_id,
			       l.principal, l.total_fees_deducted, l.net_disbursed,
			       l.disbursement_channel, l.disbursed_at
			  FROM loans l
			  JOIN loan_transactions lt ON lt.loan_id = l.id AND lt.txn_type = 'disbursement'
			 WHERE l.disbursed_at IS NOT NULL
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.loans.disbursement'
			        AND je.source_ref = lt.id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d disb
			var channel *string
			if err := rows.Scan(&d.LoanID, &d.LoanNo, &d.TxnID,
				&d.Principal, &d.Fees, &d.Net, &channel, &d.DisbursedAt); err != nil {
				return err
			}
			if channel != nil {
				d.Channel = *channel
			}
			dlist = append(dlist, d)
		}
		return rows.Err()
	})
	for _, d := range dlist {
		cashAcct := loanDisbursementCashAccount(d.Channel)
		// Aggregate fees go to 4010 (Loan Processing Fee Income) by
		// default — historical disbursements don't preserve per-fee
		// gl_credit_code mapping. Note in narration for audit.
		lines := []posting.Line{
			{AccountCode: "1100", Debit: d.Principal, Narration: "Loan receivable created"},
			{AccountCode: cashAcct, Credit: d.Net, Narration: "Net cash disbursed via " + d.Channel},
		}
		if d.Fees.GreaterThan(decimal.Zero) {
			lines = append(lines, posting.Line{
				AccountCode: "4010", Credit: d.Fees,
				Narration: "Upfront fees (aggregated — pre-fee-coa backfill)",
			})
		}
		b.post("loan-disburse",
			"savings.loans.disbursement", d.TxnID.String(),
			fmt.Sprintf("Backfill loan %s disbursement — original post lost pre-R5", d.LoanNo),
			lines, d.DisbursedAt)
	}
}

// ─────────── Category 4: loan repayments ───────────

func (b *backfillCtx) loanRepayments() {
	type repay struct {
		ID uuid.UUID
		Amount, Principal, Interest, Fee, Penalty decimal.Decimal
		Channel string
		PostedAt time.Time
		LoanNo string
	}
	var rlist []repay
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT lt.id, lt.amount, lt.principal_component, lt.interest_component,
			       lt.fee_component, lt.penalty_component,
			       lt.channel, lt.posted_at, l.loan_no
			  FROM loan_transactions lt
			  JOIN loans l ON l.id = lt.loan_id
			 WHERE lt.txn_type = 'repayment'
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.loans.repayment'
			        AND je.source_ref = lt.id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r repay
			var channel *string
			if err := rows.Scan(&r.ID, &r.Amount, &r.Principal, &r.Interest,
				&r.Fee, &r.Penalty, &channel, &r.PostedAt, &r.LoanNo); err != nil {
				return err
			}
			if channel != nil {
				r.Channel = *channel
			}
			rlist = append(rlist, r)
		}
		return rows.Err()
	})
	for _, r := range rlist {
		cashAcct := loanRepaymentCashAccount(r.Channel)
		// loan_transactions.amount is signed (negative for outflows that
		// reduce the asset). For the JE we need absolute amounts.
		amt := r.Amount.Abs()
		principal := r.Principal.Abs()
		interest := r.Interest.Abs()
		penalty := r.Penalty.Abs()
		fee := r.Fee.Abs()
		lines := []posting.Line{
			{AccountCode: cashAcct, Debit: amt, Narration: "Cash received via " + r.Channel},
		}
		if !principal.IsZero() {
			lines = append(lines, posting.Line{AccountCode: "1100", Credit: principal, Narration: "Principal repaid"})
		}
		if !interest.IsZero() {
			lines = append(lines, posting.Line{AccountCode: "4000", Credit: interest, Narration: "Loan interest income"})
		}
		if !penalty.IsZero() {
			lines = append(lines, posting.Line{AccountCode: "4030", Credit: penalty, Narration: "Penalty income"})
		}
		if !fee.IsZero() {
			lines = append(lines, posting.Line{AccountCode: "4010", Credit: fee, Narration: "Loan fees income"})
		}
		b.post("loan-repay",
			"savings.loans.repayment", r.ID.String(),
			fmt.Sprintf("Backfill loan %s repayment — original post lost pre-R6", r.LoanNo),
			lines, r.PostedAt)
	}
}

// ─────────── Category 6: deposit transactions ───────────
//
// Covers opening_balance, deposit, withdrawal, reversal,
// deposit_adjustment. SKIPS interest_credit (the per-member
// component of an interest run — the batched run JE already
// captures that aggregate; per-tx would double-count).

func (b *backfillCtx) depositTransactions() {
	type dtxn struct {
		ID, AccountID uuid.UUID
		TxnNo, TxnType, Channel string
		Amount decimal.Decimal
		Segment, ProductType string
		PostedAt time.Time
	}
	var items []dtxn
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT dt.id, dt.account_id, dt.txn_no, dt.txn_type::text,
			       COALESCE(dt.channel::text, ''),
			       dt.amount,
			       dp.segment::text, dp.product_type::text,
			       dt.posted_at
			  FROM deposit_transactions dt
			  JOIN deposit_accounts da ON da.id = dt.account_id
			  JOIN deposit_products dp ON dp.id = da.product_id
			 WHERE dt.txn_type IN ('opening_balance','deposit','withdrawal','reversal','adjustment')
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.deposits'
			        AND je.source_ref = dt.id::text
			        AND je.status = 'posted'
			   )
			 ORDER BY dt.posted_at
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d dtxn
			if err := rows.Scan(&d.ID, &d.AccountID, &d.TxnNo, &d.TxnType, &d.Channel,
				&d.Amount, &d.Segment, &d.ProductType, &d.PostedAt); err != nil {
				return err
			}
			items = append(items, d)
		}
		return rows.Err()
	})
	for _, d := range items {
		cashAcct := channelCashAccount(d.Channel)
		liabAcct := depositLiabilityCode(d.Segment, d.ProductType)
		amt := d.Amount.Abs()
		if amt.IsZero() {
			continue
		}
		// Signed amount on the column: positive = inflow to member
		// (we owe more), negative = outflow. Use the SIGN to pick
		// JE polarity rather than the txn_type alone (covers reversal
		// which can go either way).
		var dr, cr string
		if d.Amount.IsPositive() {
			dr, cr = cashAcct, liabAcct
		} else {
			dr, cr = liabAcct, cashAcct
		}
		b.post("deposit-tx",
			"savings.deposits", d.ID.String(),
			fmt.Sprintf("Backfill deposit txn %s (%s) — original post lost pre-R2", d.TxnNo, d.TxnType),
			[]posting.Line{
				{AccountCode: dr, Debit: amt, Narration: "Cash leg via " + d.Channel},
				{AccountCode: cr, Credit: amt, Narration: "Member savings (" + d.ProductType + ")"},
			}, d.PostedAt)
	}
}

// ─────────── Category 7: share purchases ───────────

func (b *backfillCtx) sharePurchases() {
	type stxn struct {
		ID uuid.UUID
		TxnNo, Channel string
		Amount decimal.Decimal
		PostedAt time.Time
	}
	var items []stxn
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT st.id, st.txn_no, COALESCE(st.payment_channel::text, ''),
			       st.amount, st.posted_at
			  FROM share_transactions st
			 WHERE st.txn_type = 'purchase'
			   AND st.journal_entry_id IS NULL
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.shares.purchase'
			        AND je.source_ref = st.id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s stxn
			if err := rows.Scan(&s.ID, &s.TxnNo, &s.Channel, &s.Amount, &s.PostedAt); err != nil {
				return err
			}
			items = append(items, s)
		}
		return rows.Err()
	})
	for _, s := range items {
		amt := s.Amount.Abs()
		if amt.IsZero() {
			continue
		}
		cashAcct := channelCashAccount(s.Channel)
		// "internal" channel: paid from member's savings — debit the
		// liability (savings down). channelCashAccount returns 1000
		// for "internal"; remap to 2000 like the share handler does.
		if strings.ToLower(s.Channel) == "internal" {
			cashAcct = "2000"
		}
		b.post("share-purchase",
			"savings.shares.purchase", s.ID.String(),
			fmt.Sprintf("Backfill share purchase %s — original post lost pre-R5", s.TxnNo),
			[]posting.Line{
				{AccountCode: cashAcct, Debit: amt, Narration: "Payment received via " + s.Channel},
				{AccountCode: "3000", Credit: amt, Narration: "Member share capital"},
			}, s.PostedAt)
	}
}

// ─────────── Category 8: share bonus issues ───────────
//
// Group bonus_issue rows by (posted_at, narration) — each bonus run
// posted a per-member row but the GL effect is ONE batched DR 3010 /
// CR 3000. Sum amounts across the run.

func (b *backfillCtx) shareBonusIssues() {
	type btxn struct {
		ID uuid.UUID
		TxnNo string
		Amount decimal.Decimal
		PostedAt time.Time
	}
	var items []btxn
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT id, txn_no, amount, posted_at
			  FROM share_transactions
			 WHERE txn_type = 'bonus_issue'
			   AND journal_entry_id IS NULL
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.shares.bonus'
			        AND je.source_ref = share_transactions.id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v btxn
			if err := rows.Scan(&v.ID, &v.TxnNo, &v.Amount, &v.PostedAt); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	for _, v := range items {
		amt := v.Amount.Abs()
		if amt.IsZero() {
			continue
		}
		b.post("share-bonus",
			"savings.shares.bonus", v.ID.String(),
			fmt.Sprintf("Backfill bonus share issue %s — original post lost pre-R5", v.TxnNo),
			[]posting.Line{
				{AccountCode: "3010", Debit: amt, Narration: "Bonus issue appropriation"},
				{AccountCode: "3000", Credit: amt, Narration: "Member share capital · bonus"},
			}, v.PostedAt)
	}
}

// ─────────── Category 9: share adjustments ───────────
//
// Post-R5 adjusts take an operator-supplied offsetting_account_code.
// Historical adjustments don't have that — default to 3010 (the
// standard prior-period-adjustment convention used elsewhere in this
// backfill).

func (b *backfillCtx) shareAdjustments() {
	type atxn struct {
		ID uuid.UUID
		TxnNo string
		SharesDelta int
		Amount decimal.Decimal
		PostedAt time.Time
	}
	var items []atxn
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT id, txn_no, shares_delta, amount, posted_at
			  FROM share_transactions
			 WHERE txn_type = 'adjustment'
			   AND journal_entry_id IS NULL
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.shares.adjust'
			        AND je.source_ref = share_transactions.id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a atxn
			if err := rows.Scan(&a.ID, &a.TxnNo, &a.SharesDelta, &a.Amount, &a.PostedAt); err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	for _, a := range items {
		amt := a.Amount.Abs()
		if amt.IsZero() {
			continue
		}
		// Increase: DR 3010 / CR 3000 ; Decrease: DR 3000 / CR 3010.
		// Mirror the R5 handler logic for sign.
		var dr, cr string
		if a.SharesDelta > 0 {
			dr, cr = "3010", "3000"
		} else {
			dr, cr = "3000", "3010"
		}
		b.post("share-adjust",
			"savings.shares.adjust", a.ID.String(),
			fmt.Sprintf("Backfill share adjustment %s — original post lost pre-R5 (offset to 3010 by default)", a.TxnNo),
			[]posting.Line{
				{AccountCode: dr, Debit: amt, Narration: "Adjustment offset"},
				{AccountCode: cr, Credit: amt, Narration: "Member share capital"},
			}, a.PostedAt)
	}
}

// ─────────── Anomalous share redemptions — flag, don't post ───────────

func (b *backfillCtx) flagAnomalousShareRedemptions() {
	var count int
	var total decimal.Decimal
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT id, txn_no, amount, posted_at
			  FROM share_transactions
			 WHERE txn_type = 'redemption'
			   AND journal_entry_id IS NULL
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var no string
			var amt decimal.Decimal
			var posted time.Time
			if err := rows.Scan(&id, &no, &amt, &posted); err != nil {
				return err
			}
			count++
			total = total.Add(amt.Abs())
			b.logger.Warn("anomalous share redemption — NOT backfilled (R5 removed redemption path; investigate as data quality issue)",
				"txn_no", no, "amount", amt.StringFixed(2), "posted", posted.Format("2006-01-02"))
		}
		return rows.Err()
	})
	if count > 0 {
		b.logger.Warn("share redemption anomaly summary",
			"count", count, "total_amount", total.StringFixed(2),
			"action", "left in place; flag to ops for separate decision (cancel? convert to transfer? reverse?)")
	}
}

// ─────────── Category 5: posted interest runs ───────────
//
// Replays the R3 postBatchedRunGLTx shape from interest_run_lines.
// Aggregates per-product savings credit + WHT + share capital +
// other payables, debits 5000 with gross.

func (b *backfillCtx) interestRuns() {
	type run struct {
		ID uuid.UUID
		RunNo, FYLabel string
	}
	var runs []run
	b.scoped(func(tx pgx.Tx) error {
		rows, err := tx.Query(b.ctx, `
			SELECT ir.id, ir.run_no, ir.financial_year_label
			  FROM interest_runs ir
			 WHERE ir.status = 'posted'
			   AND ir.journal_entry_id IS NULL
			   AND NOT EXISTS (
			     SELECT 1 FROM journal_entries je
			      WHERE je.source_module = 'savings.interest'
			        AND je.source_ref = ir.id::text
			        AND je.status = 'posted'
			   )
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r run
			if err := rows.Scan(&r.ID, &r.RunNo, &r.FYLabel); err != nil {
				return err
			}
			runs = append(runs, r)
		}
		return rows.Err()
	})
	for _, r := range runs {
		var (
			drInterest, crWHT, crShares, crOther decimal.Decimal
		)
		crLiability := map[string]decimal.Decimal{}
		b.scoped(func(tx pgx.Tx) error {
			lineRows, err := tx.Query(b.ctx, `
				SELECT irl.gross_interest, irl.wht_amount, irl.net_interest,
				       irl.payout_method::text,
				       dp.segment::text, dp.product_type::text
				  FROM interest_run_lines irl
				  JOIN deposit_products dp ON dp.id = irl.product_id
				 WHERE irl.run_id = $1
			`, r.ID)
			if err != nil {
				return err
			}
			defer lineRows.Close()
			for lineRows.Next() {
				var gross, wht, net decimal.Decimal
				var method, segment, productType string
				if err := lineRows.Scan(&gross, &wht, &net, &method, &segment, &productType); err != nil {
					return err
				}
				if net.LessThanOrEqual(decimal.Zero) {
					continue
				}
				drInterest = drInterest.Add(gross)
				crWHT = crWHT.Add(wht)
				switch method {
				case "credit_savings":
					code := depositLiabilityCode(segment, productType)
					crLiability[code] = crLiability[code].Add(net)
				case "buy_shares":
					crShares = crShares.Add(net)
				case "external":
					crOther = crOther.Add(net)
				}
			}
			return lineRows.Err()
		})

		if drInterest.LessThanOrEqual(decimal.Zero) {
			continue
		}
		lines := []posting.Line{
			{AccountCode: "5000", Debit: drInterest, Narration: "Interest expense · " + r.RunNo},
		}
		if crWHT.GreaterThan(decimal.Zero) {
			lines = append(lines, posting.Line{AccountCode: "2200", Credit: crWHT, Narration: "Withholding tax payable"})
		}
		if crShares.GreaterThan(decimal.Zero) {
			lines = append(lines, posting.Line{AccountCode: "3000", Credit: crShares, Narration: "Interest applied to shares"})
		}
		if crOther.GreaterThan(decimal.Zero) {
			lines = append(lines, posting.Line{AccountCode: "2230", Credit: crOther, Narration: "External payout owed"})
		}
		for code, amt := range crLiability {
			lines = append(lines, posting.Line{AccountCode: code, Credit: amt, Narration: "Interest credit to member savings (" + code + ")"})
		}
		// Use today's date — historical interest runs may pre-date the
		// FY start; engine requires an open period.
		entryDate := time.Now()
		b.post("interest-run",
			"savings.interest", r.ID.String(),
			fmt.Sprintf("Backfill interest run %s · %s — original post lost pre-R3", r.RunNo, r.FYLabel),
			lines, entryDate)

		if !b.dryRun {
			_ = b.pool.WithTenantTx(b.ctx, b.tenant, func(tx pgx.Tx) error {
				_, err := tx.Exec(b.ctx, `UPDATE interest_runs SET journal_entry_id=$1 WHERE id=$1`, r.ID)
				return err
			})
		}
	}
}
