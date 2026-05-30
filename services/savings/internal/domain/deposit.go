// Deposits domain types + rule evaluators.
//
// Deposits are MEMBER LIABILITIES (savings the SACCO owes back). The
// ledger uses SIGNED amounts: + for credits, − for debits.
//
// Product rules (notice period, lock-in, withdrawal window, frequency
// caps, min balance, max balance) are evaluated by EvaluateWithdrawal /
// EvaluateDeposit before any ledger posting.

package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// ─────────── Enums ───────────

type DepositProductType string

const (
	ProductOrdinary      DepositProductType = "ordinary"
	ProductFixed         DepositProductType = "fixed"
	ProductJunior        DepositProductType = "junior"
	ProductHoliday       DepositProductType = "holiday"
	ProductGoal          DepositProductType = "goal"
	ProductEmergency     DepositProductType = "emergency"
	ProductGroup         DepositProductType = "group"
	ProductMemberDeposit DepositProductType = "member_deposit"
)

func (t DepositProductType) Valid() bool {
	switch t {
	case ProductOrdinary, ProductFixed, ProductJunior, ProductHoliday,
		ProductGoal, ProductEmergency, ProductGroup, ProductMemberDeposit:
		return true
	}
	return false
}

// DepositSegment splits products into the two regulatory buckets that
// SACCO supervision (SASRA) cares about:
//
//	BOSA — non-withdrawable member deposit bond. Secures loans, only
//	       refundable on exit. Drives the loan-multiplier ceiling.
//	FOSA — withdrawable savings. Anything an officer can pay out from
//	       the teller window: ordinary, fixed (term), holiday, goal,
//	       emergency, junior, group.
//
// The split is per-product, not per-account: a product is one or the
// other for its whole life. Existing 7 product types map to FOSA;
// only `member_deposit` is BOSA.
type DepositSegment string

const (
	SegmentBOSA DepositSegment = "bosa"
	SegmentFOSA DepositSegment = "fosa"
)

func (s DepositSegment) Valid() bool {
	switch s {
	case SegmentBOSA, SegmentFOSA:
		return true
	}
	return false
}

type DepositEligibility string

const (
	EligibilityIndividuals DepositEligibility = "individuals"
	EligibilityGroups      DepositEligibility = "groups"
	EligibilityMinors      DepositEligibility = "minors"
	EligibilityAll         DepositEligibility = "all"
)

func (e DepositEligibility) Valid() bool {
	switch e {
	case EligibilityIndividuals, EligibilityGroups, EligibilityMinors, EligibilityAll:
		return true
	}
	return false
}

type MaturityAction string

const (
	MaturityNone               MaturityAction = "none"
	MaturityAutoRenew          MaturityAction = "auto_renew"
	MaturityLiquidateToOrdinary MaturityAction = "liquidate_to_ordinary"
	MaturityNotify             MaturityAction = "notify"
)

type FeeFrequency string

const (
	FeeNone      FeeFrequency = "none"
	FeeMonthly   FeeFrequency = "monthly"
	FeeQuarterly FeeFrequency = "quarterly"
	FeeAnnual    FeeFrequency = "annual"
)

type DepositAccountStatus string

const (
	AcctPending   DepositAccountStatus = "pending"
	AcctActive    DepositAccountStatus = "active"
	AcctDormant   DepositAccountStatus = "dormant"
	AcctSuspended DepositAccountStatus = "suspended"
	AcctMatured   DepositAccountStatus = "matured"
	AcctClosed    DepositAccountStatus = "closed"
)

type DepositTxnType string

const (
	TxnOpeningBalance DepositTxnType = "opening_balance"
	TxnDeposit        DepositTxnType = "deposit"
	TxnWithdrawal     DepositTxnType = "withdrawal"
	TxnDepTransferIn  DepositTxnType = "transfer_in"
	TxnDepTransferOut DepositTxnType = "transfer_out"
	TxnInterestCredit DepositTxnType = "interest_credit"
	TxnFeeDebit       DepositTxnType = "fee_debit"
	TxnReversal       DepositTxnType = "reversal"
	TxnDepAdjustment  DepositTxnType = "adjustment"
	TxnGoalPayout     DepositTxnType = "goal_payout"
)

func (t DepositTxnType) Valid() bool {
	switch t {
	case TxnOpeningBalance, TxnDeposit, TxnWithdrawal, TxnDepTransferIn,
		TxnDepTransferOut, TxnInterestCredit, TxnFeeDebit, TxnReversal,
		TxnDepAdjustment, TxnGoalPayout:
		return true
	}
	return false
}

type DepositChannel string

const (
	DepChannelCash           DepositChannel = "cash"
	DepChannelMpesa          DepositChannel = "mpesa"
	DepChannelAirtelMoney    DepositChannel = "airtel_money"
	DepChannelBankTransfer   DepositChannel = "bank_transfer"
	DepChannelStandingOrder  DepositChannel = "standing_order"
	DepChannelDirectDebit    DepositChannel = "direct_debit"
	DepChannelPayroll        DepositChannel = "payroll"
	DepChannelInternal       DepositChannel = "internal"
)

func (c DepositChannel) Valid() bool {
	switch c {
	case DepChannelCash, DepChannelMpesa, DepChannelAirtelMoney,
		DepChannelBankTransfer, DepChannelStandingOrder,
		DepChannelDirectDebit, DepChannelPayroll, DepChannelInternal:
		return true
	}
	return false
}

// ─────────── Entities ───────────

type DepositProduct struct {
	ID                          uuid.UUID          `json:"id"`
	TenantID                    uuid.UUID          `json:"tenant_id"`
	Code                        string             `json:"code"`
	Name                        string             `json:"name"`
	ProductType                 DepositProductType `json:"product_type"`
	Description                 *string            `json:"description,omitempty"`
	IsActive                    bool               `json:"is_active"`
	MinOpeningBalance           decimal.Decimal    `json:"min_opening_balance"`
	MinOperatingBalance         decimal.Decimal    `json:"min_operating_balance"`
	MaxBalance                  *decimal.Decimal   `json:"max_balance,omitempty"`
	MinDepositAmount            decimal.Decimal    `json:"min_deposit_amount"`
	MaxDepositAmount            *decimal.Decimal   `json:"max_deposit_amount,omitempty"`
	MinWithdrawalAmount         decimal.Decimal    `json:"min_withdrawal_amount"`
	MaxWithdrawalAmount         *decimal.Decimal   `json:"max_withdrawal_amount,omitempty"`
	NoticePeriodDays            int                `json:"notice_period_days"`
	MaxWithdrawalsPerMonth      *int               `json:"max_withdrawals_per_month,omitempty"`
	PartialWithdrawalAllowed    bool               `json:"partial_withdrawal_allowed"`
	LargeWithdrawalThreshold    *decimal.Decimal   `json:"large_withdrawal_threshold,omitempty"`
	LockInMonths                int                `json:"lock_in_months"`
	DefaultTermMonths           *int               `json:"default_term_months,omitempty"`
	MaturityAction              MaturityAction     `json:"maturity_action"`
	Eligibility                 DepositEligibility `json:"eligibility"`
	RequiresApprovalToOpen      bool               `json:"requires_approval_to_open"`
	WithdrawalWindowStartMonth  *int               `json:"withdrawal_window_start_month,omitempty"`
	WithdrawalWindowEndMonth    *int               `json:"withdrawal_window_end_month,omitempty"`
	MaintenanceFee              decimal.Decimal    `json:"maintenance_fee"`
	MaintenanceFeeFrequency     FeeFrequency       `json:"maintenance_fee_frequency"`
	EarlyWithdrawalPenaltyPct   decimal.Decimal    `json:"early_withdrawal_penalty_pct"`
	BelowMinBalanceFee          decimal.Decimal    `json:"below_min_balance_fee"`
	DormancyFeeMonthly          decimal.Decimal    `json:"dormancy_fee_monthly"`
	// BOSA / FOSA segmentation. Segment is required (NOT NULL in the
	// schema); RequiredMonthlyAmount + RequiredDayOfMonth are the
	// recurring-contribution schedule, only meaningful for BOSA.
	Segment                     DepositSegment     `json:"segment"`
	RequiredMonthlyAmount       decimal.Decimal    `json:"required_monthly_amount"`
	RequiredDayOfMonth          *int               `json:"required_day_of_month,omitempty"`
	CreatedAt                   time.Time          `json:"created_at"`
	UpdatedAt                   time.Time          `json:"updated_at"`
	CreatedBy                   *uuid.UUID         `json:"created_by,omitempty"`
}

// ValidateBOSAConstraints enforces the regulatory invariants for BOSA
// products: no partial withdrawals, no notice period. FOSA products
// pass through unchanged. Called from the product create/update
// handler so a misconfigured BOSA never reaches the database.
func (p *DepositProduct) ValidateBOSAConstraints() error {
	if p.Segment != SegmentBOSA {
		return nil
	}
	if p.PartialWithdrawalAllowed {
		return errors.New("BOSA products cannot allow partial withdrawals")
	}
	if p.NoticePeriodDays != 0 {
		return errors.New("BOSA products cannot have a notice period")
	}
	return nil
}

type DepositAccount struct {
	ID                       uuid.UUID            `json:"id"`
	TenantID                 uuid.UUID            `json:"tenant_id"`
	CounterpartyID                 uuid.UUID            `json:"counterparty_id"`
	ProductID                uuid.UUID            `json:"product_id"`
	AccountNo                string               `json:"account_no"`
	Status                   DepositAccountStatus `json:"status"`
	CurrentBalance           decimal.Decimal      `json:"current_balance"`
	AvailableBalance         decimal.Decimal      `json:"available_balance"`
	OpenedAt                 *time.Time           `json:"opened_at,omitempty"`
	MaturesAt                *time.Time           `json:"matures_at,omitempty"`
	ClosedAt                 *time.Time           `json:"closed_at,omitempty"`
	LastActivityAt           *time.Time           `json:"last_activity_at,omitempty"`
	LastDepositAt            *time.Time           `json:"last_deposit_at,omitempty"`
	LastWithdrawalAt         *time.Time           `json:"last_withdrawal_at,omitempty"`
	FixedTermMonths          *int                 `json:"fixed_term_months,omitempty"`
	FixedInterestRatePct     *decimal.Decimal     `json:"fixed_interest_rate_pct,omitempty"`
	GoalTargetAmount         *decimal.Decimal     `json:"goal_target_amount,omitempty"`
	GoalTargetDate           *time.Time           `json:"goal_target_date,omitempty"`
	GoalDescription          *string              `json:"goal_description,omitempty"`
	GuardianMemberID         *uuid.UUID           `json:"guardian_member_id,omitempty"`
	GroupOrgID               *uuid.UUID           `json:"group_org_id,omitempty"`
	WithdrawalNoticeGivenAt  *time.Time           `json:"withdrawal_notice_given_at,omitempty"`
	WithdrawalNoticeAmount   *decimal.Decimal     `json:"withdrawal_notice_amount,omitempty"`
	// DSID Phase 2.2 — joint account fields.
	IsJoint         bool `json:"is_joint"`
	RequiredSigners int  `json:"required_signers"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CreatedBy       *uuid.UUID `json:"created_by,omitempty"`
}

type DepositTransaction struct {
	ID                    uuid.UUID         `json:"id"`
	TenantID              uuid.UUID         `json:"tenant_id"`
	AccountID             uuid.UUID         `json:"account_id"`
	CounterpartyID              uuid.UUID         `json:"counterparty_id"`
	TxnNo                 string            `json:"txn_no"`
	TxnType               DepositTxnType    `json:"txn_type"`
	Amount                decimal.Decimal   `json:"amount"`
	ValueDate             time.Time         `json:"value_date"`
	Channel               *DepositChannel   `json:"channel,omitempty"`
	ChannelRef            *string           `json:"channel_ref,omitempty"`
	Narration             *string           `json:"narration,omitempty"`
	CounterpartyAccountID *uuid.UUID        `json:"counterparty_account_id,omitempty"`
	CounterpartyTxnID     *uuid.UUID        `json:"counterparty_txn_id,omitempty"`
	ReversesTxnID         *uuid.UUID        `json:"reverses_txn_id,omitempty"`
	ReversedByTxnID       *uuid.UUID        `json:"reversed_by_txn_id,omitempty"`
	ReversalReason        *string           `json:"reversal_reason,omitempty"`
	BalanceAfter          decimal.Decimal   `json:"balance_after"`
	InitiatedBy           uuid.UUID         `json:"initiated_by"`
	AuthorizedBy          *uuid.UUID        `json:"authorized_by,omitempty"`
	AuthorizationReason   *string           `json:"authorization_reason,omitempty"`
	WorkflowInstanceID    *uuid.UUID        `json:"workflow_instance_id,omitempty"`
	PostedAt              time.Time         `json:"posted_at"`
	CreatedAt             time.Time         `json:"created_at"`
}

// ─────────── Errors ───────────

var (
	ErrProductInactive            = errors.New("deposit product is not active")
	ErrProductIneligible          = errors.New("member is not eligible for this product")
	ErrMemberIneligibleStatus     = errors.New("member status does not permit deposit operations")
	ErrAccountNotActive           = errors.New("deposit account is not active")
	ErrBelowMinOpeningBalance     = errors.New("opening balance is below the product minimum")
	ErrBelowMinDeposit            = errors.New("deposit is below the product minimum deposit amount")
	ErrAboveMaxDeposit            = errors.New("deposit exceeds the product maximum deposit amount")
	ErrBelowMinWithdrawal         = errors.New("withdrawal is below the product minimum withdrawal amount")
	ErrAboveMaxWithdrawal         = errors.New("withdrawal exceeds the product maximum withdrawal amount")
	ErrWouldBreachMinBalance      = errors.New("withdrawal would breach the minimum operating balance")
	ErrWouldExceedMaxBalance      = errors.New("deposit would exceed the product maximum balance cap")
	ErrInsufficientBalance        = errors.New("insufficient available balance")
	ErrLockInActive               = errors.New("account is within its lock-in period; withdrawals are not permitted")
	ErrOutsideWithdrawalWindow    = errors.New("withdrawals are only permitted during the configured window")
	ErrPartialWithdrawalNotAllowed = errors.New("this product permits full-balance withdrawals only")
	ErrFrequencyCapReached        = errors.New("monthly withdrawal frequency cap has been reached")
	ErrNoticePeriodNotMet         = errors.New("notice period has not elapsed; please give withdrawal notice and try again")
	ErrDuplicateTransaction       = errors.New("a transaction with the same channel reference and amount was posted recently — possible duplicate")
	ErrCannotReverseReversal      = errors.New("a reversal transaction cannot itself be reversed")
	ErrAlreadyReversed            = errors.New("this transaction has already been reversed")
	// BOSA accounts cannot be drained via the normal Withdraw handler.
	// Officers route through the /v1/bosa/exit endpoint, which queues
	// a Board-level approval (kind = member_bosa_exit). The wire-level
	// error code is BOSA_WITHDRAW_FORBIDDEN.
	ErrBOSAWithdrawForbidden     = errors.New("BOSA accounts can only be refunded via the member-exit workflow")
)

// ─────────── Rule evaluation ───────────

// EligibleForProduct decides if a given member status + member kind is
// allowed under a product's eligibility setting. memberKind is one of
// "individual", "minor", "group".
func EligibleForProduct(p *DepositProduct, memberKind, memberStatus string) error {
	switch memberStatus {
	case "blacklisted", "exited", "deceased", "rejected":
		return fmt.Errorf("%w (status=%s)", ErrMemberIneligibleStatus, memberStatus)
	}
	switch p.Eligibility {
	case EligibilityAll:
		return nil
	case EligibilityIndividuals:
		if memberKind != "individual" {
			return ErrProductIneligible
		}
	case EligibilityMinors:
		if memberKind != "minor" {
			return ErrProductIneligible
		}
	case EligibilityGroups:
		if memberKind != "group" {
			return ErrProductIneligible
		}
	}
	return nil
}

// EvaluateDeposit checks an inbound credit against product rules.
// reuses postedToday for the duplicate-detection check (count of
// transactions with same channel + ref in the lookback window).
func EvaluateDeposit(p *DepositProduct, acct *DepositAccount, amount decimal.Decimal) error {
	if !p.IsActive {
		return ErrProductInactive
	}
	if acct.Status != AcctActive && acct.Status != AcctPending {
		return ErrAccountNotActive
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return errors.New("deposit amount must be positive")
	}
	if p.MinDepositAmount.GreaterThan(decimal.Zero) && amount.LessThan(p.MinDepositAmount) {
		return ErrBelowMinDeposit
	}
	if p.MaxDepositAmount != nil && amount.GreaterThan(*p.MaxDepositAmount) {
		return ErrAboveMaxDeposit
	}
	if p.MaxBalance != nil {
		if acct.CurrentBalance.Add(amount).GreaterThan(*p.MaxBalance) {
			return ErrWouldExceedMaxBalance
		}
	}
	return nil
}

// EvaluateWithdrawal checks a debit against product rules. monthlyCount
// is the number of withdrawals already posted in the current calendar
// month (caller supplies via store).
func EvaluateWithdrawal(p *DepositProduct, acct *DepositAccount, amount decimal.Decimal, now time.Time, monthlyCount int) error {
	if !p.IsActive {
		return ErrProductInactive
	}
	if acct.Status != AcctActive && acct.Status != AcctMatured {
		return ErrAccountNotActive
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return errors.New("withdrawal amount must be positive")
	}
	if p.MinWithdrawalAmount.GreaterThan(decimal.Zero) && amount.LessThan(p.MinWithdrawalAmount) {
		return ErrBelowMinWithdrawal
	}
	if p.MaxWithdrawalAmount != nil && amount.GreaterThan(*p.MaxWithdrawalAmount) {
		return ErrAboveMaxWithdrawal
	}
	if amount.GreaterThan(acct.AvailableBalance) {
		return ErrInsufficientBalance
	}
	// Lock-in is the hardest constraint — check it first so the error
	// message matches the user-visible cause rather than masking it
	// with the partial-vs-full check.
	if p.LockInMonths > 0 && acct.OpenedAt != nil {
		unlockAt := acct.OpenedAt.AddDate(0, p.LockInMonths, 0)
		if now.Before(unlockAt) {
			return ErrLockInActive
		}
	}
	// Min operating balance check.
	if p.MinOperatingBalance.GreaterThan(decimal.Zero) {
		if acct.CurrentBalance.Sub(amount).LessThan(p.MinOperatingBalance) {
			// Permit if this would close the account (zero balance) and
			// product allows non-partial withdrawals.
			if !acct.CurrentBalance.Sub(amount).Equal(decimal.Zero) || p.PartialWithdrawalAllowed {
				return ErrWouldBreachMinBalance
			}
		}
	}
	// Partial vs full-only.
	if !p.PartialWithdrawalAllowed && !amount.Equal(acct.CurrentBalance) {
		return ErrPartialWithdrawalNotAllowed
	}
	// Withdrawal window (e.g. Holiday: Nov-Dec).
	if p.WithdrawalWindowStartMonth != nil && p.WithdrawalWindowEndMonth != nil {
		if !monthInWindow(int(now.Month()), *p.WithdrawalWindowStartMonth, *p.WithdrawalWindowEndMonth) {
			return ErrOutsideWithdrawalWindow
		}
	}
	// Monthly frequency cap.
	if p.MaxWithdrawalsPerMonth != nil && monthlyCount >= *p.MaxWithdrawalsPerMonth {
		return ErrFrequencyCapReached
	}
	// Notice period — caller must check that notice was given more than
	// notice_period_days ago. We use the deposit_accounts.withdrawal_notice_*
	// columns from the store.
	if p.NoticePeriodDays > 0 {
		if acct.WithdrawalNoticeGivenAt == nil ||
			acct.WithdrawalNoticeAmount == nil ||
			now.Sub(*acct.WithdrawalNoticeGivenAt) < time.Duration(p.NoticePeriodDays)*24*time.Hour ||
			amount.GreaterThan(*acct.WithdrawalNoticeAmount) {
			return ErrNoticePeriodNotMet
		}
	}
	return nil
}

// IsLargeWithdrawal returns true when amount exceeds the product's
// large_withdrawal_threshold (so the handler should gate via workflow).
func IsLargeWithdrawal(p *DepositProduct, amount decimal.Decimal) bool {
	if p.LargeWithdrawalThreshold == nil {
		return false
	}
	return amount.GreaterThan(*p.LargeWithdrawalThreshold)
}

// monthInWindow handles wrap-around (e.g. window Nov–Feb spans the year boundary).
func monthInWindow(month, start, end int) bool {
	if start <= end {
		return month >= start && month <= end
	}
	// Wrap-around: e.g. Nov–Feb = months in [11,12] OR [1,2]
	return month >= start || month <= end
}

// NormalizeProductCode trims and uppercases the user-supplied code.
func NormalizeProductCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}
