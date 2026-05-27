// Package postingops hoists every "post a balanced JE to the outbox"
// helper out of the savings handlers so the inline panels AND the
// approval executor can share one seam. Before this seam, the inline
// handlers had their own postXxxToGLTx methods and the approval
// executor had NO post call at all — that gap is the bug this PR
// closes.
//
// Each Post*Tx function:
//   • Constructs the per-kind JE legs.
//   • Calls posting.Client.PostTx (writes to posting_outbox in tx).
//   • Stamps the resulting txn's journal_entry_id where the schema
//     supports the single-txn-per-JE pattern.
//
// What this package does NOT do:
//   - Execute the subledger write. The caller already did that.
//   - Queue approvals. That's the inline handler's decision.
//   - Persist receipts. That's receiptops's job.
//
// Idempotency: every JE post keys on
//   source_module = "<service>.<module>.<operation>"
//   source_ref    = <subledger_txn_id>
// Accounting's UNIQUE (source_module, source_ref) on posting_outbox
// drops a duplicate post silently, so re-execution (checker
// double-click, retry from outbox dispatcher) is safe.
//
// Input shape: each Post* helper takes a small per-kind input struct
// (ShareBuyInput, DepositInput, etc.). The handler builds the input
// from its Result. This keeps postingops free of any handler-package
// import (no circular dep).

package postingops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

// Deps is the union of stores the post helpers touch. A helper that
// needs a missing dep returns an error rather than silently no-oping.
type Deps struct {
	Posting         *posting.Client
	Shares          *store.ShareStore
	Deposits        *store.DepositStore
	DepositProducts *store.DepositProductStore
}

// ─── Share purchase ──────────────────────────────────────────

type ShareBuyInput struct {
	TenantID       uuid.UUID
	TxnID          uuid.UUID
	Amount         decimal.Decimal
	SharesDelta    int
	AccountNo      string
	PaymentChannel domain.PaymentChannel
}

// PostShareBuyTx:
//
//	DR <channel cash> = amount
//	CR 3000 Member Share Capital
//
// Stamps share_transactions.journal_entry_id = txn id.
func PostShareBuyTx(ctx context.Context, tx pgx.Tx, deps Deps, in ShareBuyInput) error {
	if deps.Posting == nil {
		return nil
	}
	if in.Amount.IsZero() {
		return nil
	}
	cashAcct := shareChannelCashAccount(in.PaymentChannel)
	narration := fmt.Sprintf("Share purchase %d shares · %s", in.SharesDelta, in.AccountNo)
	if err := deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.shares.purchase",
		SourceRef:    in.TxnID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: cashAcct, Debit: in.Amount, Narration: "Payment received"},
			{AccountCode: "3000", Credit: in.Amount, Narration: "Member share capital"},
		},
	}); err != nil {
		return err
	}
	if deps.Posting.DryRun || deps.Shares == nil {
		return nil
	}
	return deps.Shares.UpdateJournalEntryIDTx(ctx, tx, in.TxnID, in.TxnID)
}

// ─── Deposit / withdrawal ────────────────────────────────────

type DepositInput struct {
	TenantID  uuid.UUID
	TxnID     uuid.UUID
	Amount    decimal.Decimal
	AccountNo string
	ProductID uuid.UUID
	Channel   domain.DepositChannel
}

// PostDepositTx:
//
//	DR <channel cash> = amount
//	CR <product liability>
func PostDepositTx(ctx context.Context, tx pgx.Tx, deps Deps, in DepositInput) error {
	if deps.Posting == nil {
		return nil
	}
	cashAcct := channelCashAccount(in.Channel)
	liabAcct := resolveLiabilityAcctTx(ctx, tx, deps, in.ProductID)
	narration := fmt.Sprintf("Deposit %s to a/c %s", in.Amount.StringFixed(2), in.AccountNo)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.deposits",
		SourceRef:    in.TxnID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: cashAcct, Debit: in.Amount, Narration: "Cash received"},
			{AccountCode: liabAcct, Credit: in.Amount, Narration: "Member savings credited"},
		},
	})
}

// PostWithdrawalTx — inverse of PostDepositTx, same shape.
func PostWithdrawalTx(ctx context.Context, tx pgx.Tx, deps Deps, in DepositInput) error {
	if deps.Posting == nil {
		return nil
	}
	cashAcct := channelCashAccount(in.Channel)
	liabAcct := resolveLiabilityAcctTx(ctx, tx, deps, in.ProductID)
	narration := fmt.Sprintf("Withdrawal %s from a/c %s", in.Amount.StringFixed(2), in.AccountNo)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.deposits",
		SourceRef:    in.TxnID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: liabAcct, Debit: in.Amount, Narration: "Member savings debited"},
			{AccountCode: cashAcct, Credit: in.Amount, Narration: "Cash paid out"},
		},
	})
}

// ─── Deposit transfer (inter-account, no cash leg) ───────────

type DepositTransferInput struct {
	TenantID      uuid.UUID
	FromTxnID     uuid.UUID
	ToTxnID       uuid.UUID
	Amount        decimal.Decimal
	FromAccountNo string
	ToAccountNo   string
	FromProductID uuid.UUID
	ToProductID   uuid.UUID
}

// PostDepositTransferTx posts a balanced two-leg JE between two
// member savings accounts. No external cash movement — debit the
// source liability + credit the destination liability:
//
//	DR <from product liability>
//	CR <to product liability>
//
// Source ref is the from-txn id (one JE per transfer).
func PostDepositTransferTx(ctx context.Context, tx pgx.Tx, deps Deps, in DepositTransferInput) error {
	if deps.Posting == nil {
		return nil
	}
	if in.Amount.IsZero() {
		return nil
	}
	fromLiab := resolveLiabilityAcctTx(ctx, tx, deps, in.FromProductID)
	toLiab := resolveLiabilityAcctTx(ctx, tx, deps, in.ToProductID)
	narration := fmt.Sprintf("Deposit transfer %s → %s · %s",
		in.FromAccountNo, in.ToAccountNo, in.Amount.StringFixed(2))
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.deposits.transfer",
		SourceRef:    in.FromTxnID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: fromLiab, Debit: in.Amount, Narration: "Transfer out (" + in.FromAccountNo + ")"},
			{AccountCode: toLiab, Credit: in.Amount, Narration: "Transfer in (" + in.ToAccountNo + ")"},
		},
	})
}

// ─── Loan repayment / reversal / settle ──────────────────────

type LoanRepaymentInput struct {
	TenantID  uuid.UUID
	TxnID     uuid.UUID
	LoanNo    string
	Amount    decimal.Decimal
	Principal decimal.Decimal
	Interest  decimal.Decimal
	Penalty   decimal.Decimal
	Fees      decimal.Decimal
	Channel   string
}

// PostLoanRepaymentTx:
//
//	DR <channel cash>             = total amount
//	CR 1100 Loans Receivable      = principal portion (if non-zero)
//	CR 4000 Loan Interest Income  = interest portion (if non-zero)
//	CR 4030 Penalty Income        = penalty portion (if non-zero)
//	CR 4010 Loan Fees Income      = fees portion (if non-zero)
func PostLoanRepaymentTx(ctx context.Context, tx pgx.Tx, deps Deps, in LoanRepaymentInput) error {
	if deps.Posting == nil {
		return nil
	}
	cashAcct := repaymentCashAccount(in.Channel)
	lines := []posting.Line{
		{AccountCode: cashAcct, Debit: in.Amount, Narration: "Cash received"},
	}
	if !in.Principal.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "1100", Credit: in.Principal, Narration: "Principal repaid"})
	}
	if !in.Interest.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4000", Credit: in.Interest, Narration: "Loan interest income"})
	}
	if !in.Penalty.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4030", Credit: in.Penalty, Narration: "Penalty income"})
	}
	if !in.Fees.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4010", Credit: in.Fees, Narration: "Loan fees income"})
	}
	narration := fmt.Sprintf("Repayment on loan %s", in.LoanNo)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.repayment",
		SourceRef:    in.TxnID.String(),
		Narration:    narration,
		Lines:        lines,
	})
}

// LoanRepaymentReversalInput is the same shape as LoanRepaymentInput
// but tagged with the original-txn id so reconciliation can stitch
// the pair. The legs are the exact inverse of the forward post.
type LoanRepaymentReversalInput struct {
	TenantID      uuid.UUID
	ReversalTxnID uuid.UUID
	OriginalTxnID uuid.UUID
	LoanNo        string
	Amount        decimal.Decimal
	Principal     decimal.Decimal
	Interest      decimal.Decimal
	Penalty       decimal.Decimal
	Fees          decimal.Decimal
	Channel       string
	Reason        string
}

// PostLoanRepaymentReversalTx undoes a repayment. Debits and credits
// swap from the forward post. Source ref is "reverse:<original_txn_id>"
// to dedup against accidental double-reversals on the same row.
func PostLoanRepaymentReversalTx(ctx context.Context, tx pgx.Tx, deps Deps, in LoanRepaymentReversalInput) error {
	if deps.Posting == nil {
		return nil
	}
	cashAcct := repaymentCashAccount(in.Channel)
	lines := []posting.Line{
		{AccountCode: cashAcct, Credit: in.Amount, Narration: "Cash repaid to member (reversal)"},
	}
	if !in.Principal.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "1100", Debit: in.Principal, Narration: "Principal restored"})
	}
	if !in.Interest.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4000", Debit: in.Interest, Narration: "Loan interest income reversed"})
	}
	if !in.Penalty.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4030", Debit: in.Penalty, Narration: "Penalty income reversed"})
	}
	if !in.Fees.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4010", Debit: in.Fees, Narration: "Loan fees income reversed"})
	}
	narration := fmt.Sprintf("Reverse repayment on loan %s (%s)", in.LoanNo, in.Reason)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.repayment.reversal",
		SourceRef:    "reverse:" + in.OriginalTxnID.String(),
		Narration:    narration,
		Lines:        lines,
	})
}

// LoanSettleInput captures the closing balances at settlement. The
// JE shape mirrors a normal repayment for the cleared components
// but tagged as a settlement so reports can split them out.
type LoanSettleInput struct {
	TenantID  uuid.UUID
	TxnID     uuid.UUID
	LoanNo    string
	Amount    decimal.Decimal
	Principal decimal.Decimal
	Interest  decimal.Decimal
	Penalty   decimal.Decimal
	Fees      decimal.Decimal
	Channel   string
}

// PostLoanSettleTx — same legs as a repayment, distinct
// source_module so the settlement-distinct read view filters
// cleanly.
func PostLoanSettleTx(ctx context.Context, tx pgx.Tx, deps Deps, in LoanSettleInput) error {
	if deps.Posting == nil {
		return nil
	}
	cashAcct := repaymentCashAccount(in.Channel)
	lines := []posting.Line{
		{AccountCode: cashAcct, Debit: in.Amount, Narration: "Settlement cash received"},
	}
	if !in.Principal.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "1100", Credit: in.Principal, Narration: "Principal settled"})
	}
	if !in.Interest.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4000", Credit: in.Interest, Narration: "Interest at settlement"})
	}
	if !in.Penalty.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4030", Credit: in.Penalty, Narration: "Penalty at settlement"})
	}
	if !in.Fees.IsZero() {
		lines = append(lines, posting.Line{AccountCode: "4010", Credit: in.Fees, Narration: "Fees at settlement"})
	}
	narration := fmt.Sprintf("Settlement of loan %s", in.LoanNo)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.settle",
		SourceRef:    in.TxnID.String(),
		Narration:    narration,
		Lines:        lines,
	})
}

// ─── Loan write-off (toggle-driven) ───────────────────────────

type LoanWriteoffInput struct {
	TenantID uuid.UUID
	TxnID    uuid.UUID
	LoanNo   string
	Amount   decimal.Decimal
	Reason   string
	// ThroughProvision flips the DR leg:
	//   false → DR 5020 Provision Expense (direct write-off)
	//   true  → DR 2900 Provision Allowance (consumes accrued allowance)
	// Driven by tenant_operations.writeoff_through_provision.
	ThroughProvision bool
}

// PostLoanWriteoffTx posts the (debit / 1100 credit) leg. Direct
// vs through-provision is per-tenant per the toggle introduced in
// migration 0038.
func PostLoanWriteoffTx(ctx context.Context, tx pgx.Tx, deps Deps, in LoanWriteoffInput) error {
	if deps.Posting == nil {
		return nil
	}
	if in.Amount.IsZero() {
		return nil
	}
	debitCode := "5020"
	debitNarration := "Loan provision expense"
	if in.ThroughProvision {
		debitCode = "2900"
		debitNarration = "Consume provision allowance"
	}
	narration := fmt.Sprintf("Write-off loan %s (%s)", in.LoanNo, in.Reason)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.writeoff",
		SourceRef:    in.TxnID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: debitCode, Debit: in.Amount, Narration: debitNarration},
			{AccountCode: "1100", Credit: in.Amount, Narration: "Loans receivable written off"},
		},
	})
}

// ─── Loan settlement discount (waiver) ───────────────────────

type LoanSettlementDiscountInput struct {
	TenantID        uuid.UUID
	DiscountTxnID   uuid.UUID
	LoanNo          string
	DiscountAmount  decimal.Decimal
	WaivedComponent string // "interest" | "penalty" | "fees" | "principal"
	Reason          string
}

// PostLoanSettlementDiscountTx — a waiver post. The discounted
// component's revenue line is reversed; the receivable comes down
// without a cash leg.
//
//	waived interest:  DR 4000  / CR 1100
//	waived penalty:   DR 4030  / CR 1100
//	waived fees:      DR 4010  / CR 1100
//	waived principal: DR 5020  / CR 1100  (treated like a partial direct write-off)
func PostLoanSettlementDiscountTx(ctx context.Context, tx pgx.Tx, deps Deps, in LoanSettlementDiscountInput) error {
	if deps.Posting == nil {
		return nil
	}
	if in.DiscountAmount.IsZero() {
		return nil
	}
	debitCode := "4000"
	switch strings.ToLower(in.WaivedComponent) {
	case "penalty":
		debitCode = "4030"
	case "fees":
		debitCode = "4010"
	case "principal":
		debitCode = "5020"
	}
	narration := fmt.Sprintf("Settlement discount on loan %s · waived %s · %s",
		in.LoanNo, in.WaivedComponent, in.Reason)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.settlement_discount",
		SourceRef:    in.DiscountTxnID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: debitCode, Debit: in.DiscountAmount, Narration: "Revenue waived"},
			{AccountCode: "1100", Credit: in.DiscountAmount, Narration: "Loans receivable waived"},
		},
	})
}

// ─── Loan disbursement ───────────────────────────────────────

type LoanDisbursementInput struct {
	TenantID          uuid.UUID
	DisbursementTxnID uuid.UUID
	LoanNo            string
	Principal         decimal.Decimal
	NetDisbursed      decimal.Decimal
	FeeGLLines        []posting.Line // pre-aggregated by gl_credit_code
	Channel           string
	// LoanForCashResolve carries the loan row for the
	// disbursement-cash-account resolver. The resolver reads
	// DisbursementTargetAccountID when channel='internal' to look up
	// the destination deposit account's product → liability code.
	LoanForCashResolve domain.Loan
}

func PostLoanDisbursementTx(ctx context.Context, tx pgx.Tx, deps Deps, in LoanDisbursementInput) error {
	if deps.Posting == nil {
		return nil
	}
	principal := in.Principal
	net := in.NetDisbursed
	if net.IsZero() && len(in.FeeGLLines) == 0 {
		net = principal
	}
	cashAcct := resolveDisbursementCashAcctTx(ctx, tx, deps, in.Channel, in.LoanForCashResolve)
	narration := fmt.Sprintf("Loan %s disbursement via %s", in.LoanNo, in.Channel)
	cashNarration := "Cash disbursed"
	if strings.ToLower(in.Channel) == "internal" || strings.ToLower(in.Channel) == "savings" {
		cashNarration = "Credited to member savings (" + cashAcct + ")"
	}
	lines := []posting.Line{
		{AccountCode: "1100", Debit: principal, Narration: "Loan receivable created"},
		{AccountCode: cashAcct, Credit: net, Narration: cashNarration},
	}
	lines = append(lines, in.FeeGLLines...)
	return deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.loans.disbursement",
		SourceRef:    in.DisbursementTxnID.String(),
		Narration:    narration,
		Lines:        lines,
	})
}

// ─── Share bonus issue ──────────────────────────────────────

type ShareBonusInput struct {
	TenantID         uuid.UUID
	ParValue         decimal.Decimal
	TotalBonusShares int
	PctApplied       decimal.Decimal
	Reason           string
	// TxnIDs is the per-member share_transactions rows the bonus
	// run produced. PostShareBonusTx stamps the SAME jeID on every
	// row so reports can recover the per-member breakdown by JE.
	TxnIDs []uuid.UUID
}

func PostShareBonusTx(ctx context.Context, tx pgx.Tx, deps Deps, in ShareBonusInput) error {
	if deps.Posting == nil || deps.Posting.DryRun {
		return nil
	}
	if in.TotalBonusShares <= 0 {
		return nil
	}
	amount := in.ParValue.Mul(decimal.NewFromInt(int64(in.TotalBonusShares)))
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	jeID := uuid.New()
	narration := fmt.Sprintf("Bonus issue · %s%% · %d shares · %s",
		in.PctApplied.String(), in.TotalBonusShares, in.Reason)
	if err := deps.Posting.PostTx(ctx, tx, posting.PostInput{
		TenantID:     in.TenantID,
		EntryDate:    time.Now(),
		SourceModule: "savings.shares.bonus",
		SourceRef:    jeID.String(),
		Narration:    narration,
		Lines: []posting.Line{
			{AccountCode: "3010", Debit: amount, Narration: "Bonus issue appropriation"},
			{AccountCode: "3000", Credit: amount, Narration: "Member share capital - bonus shares"},
		},
	}); err != nil {
		return err
	}
	if deps.Shares == nil {
		return nil
	}
	for _, txnID := range in.TxnIDs {
		if err := deps.Shares.UpdateJournalEntryIDTx(ctx, tx, txnID, jeID); err != nil {
			return err
		}
	}
	return nil
}

// ─── per-channel account-code map (private) ─────────────────

func shareChannelCashAccount(ch domain.PaymentChannel) string {
	switch ch {
	case domain.ChannelMpesa:
		return "1030"
	case domain.ChannelAirtelMoney:
		return "1040"
	case domain.ChannelBankTransfer, domain.ChannelPayroll, domain.ChannelStandingOrder:
		return "1020"
	case domain.ChannelInternal:
		return "2000"
	default:
		return "1000"
	}
}

func channelCashAccount(ch domain.DepositChannel) string {
	switch ch {
	case domain.DepChannelMpesa:
		return "1030"
	case domain.DepChannelAirtelMoney:
		return "1040"
	case domain.DepChannelBankTransfer, domain.DepChannelStandingOrder,
		domain.DepChannelPayroll, domain.DepChannelDirectDebit:
		return "1020"
	case domain.DepChannelInternal:
		return "2000"
	default:
		return "1000"
	}
}

func repaymentCashAccount(channel string) string {
	switch strings.ToLower(channel) {
	case "mpesa":
		return "1030"
	case "bank", "bank_transfer":
		return "1020"
	case "auto_savings":
		return "2000"
	case "payroll":
		return "1020"
	default:
		return "1000"
	}
}

func resolveLiabilityAcctTx(ctx context.Context, tx pgx.Tx, deps Deps, productID uuid.UUID) string {
	if deps.DepositProducts == nil {
		return "2000"
	}
	p, err := deps.DepositProducts.GetTx(ctx, tx, productID)
	if err != nil || p == nil {
		return "2000"
	}
	return depositLiabilityCode(p.Segment, p.ProductType)
}

// depositLiabilityCode mirrors handler.depositLiabilityCode (see
// deposit.go for the per-segment + per-product rationale). Kept in
// sync with that source — when a new product type lands, update
// both. A long-term cleanup would move this single source of truth
// into domain/, but doing that in this PR drags every consumer.
func depositLiabilityCode(segment domain.DepositSegment, productType domain.DepositProductType) string {
	if segment == domain.SegmentBOSA {
		return "2050"
	}
	switch productType {
	case domain.ProductOrdinary:
		return "2000"
	case domain.ProductHoliday:
		return "2010"
	case domain.ProductEmergency:
		return "2020"
	case domain.ProductGoal:
		return "2030"
	case domain.ProductJunior:
		return "2040"
	case domain.ProductFixed:
		return "2100"
	case domain.ProductGroup:
		return "2000"
	case domain.ProductMemberDeposit:
		return "2050"
	}
	return "2000"
}

func resolveDisbursementCashAcctTx(ctx context.Context, tx pgx.Tx, deps Deps, channel string, loan domain.Loan) string {
	ch := strings.ToLower(channel)
	if ch != "internal" && ch != "savings" {
		switch ch {
		case "mpesa":
			return "1015"
		case "bank", "bank_transfer":
			return "1020"
		default:
			return "1000"
		}
	}
	if loan.DisbursementTargetAccountID == nil || deps.Deposits == nil || deps.DepositProducts == nil {
		return "2000"
	}
	acct, err := deps.Deposits.GetAccountTx(ctx, tx, *loan.DisbursementTargetAccountID)
	if err != nil || acct == nil {
		return "2000"
	}
	p, err := deps.DepositProducts.GetTx(ctx, tx, acct.ProductID)
	if err != nil || p == nil {
		return "2000"
	}
	return depositLiabilityCode(p.Segment, p.ProductType)
}
