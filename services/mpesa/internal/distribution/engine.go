// Distribution engine. Phase 3 (defer-apply variant).
//
// Run takes a tenant-scoped tx + a resolved mpesa_inbound_events row
// and produces a Plan — an ordered list of Splits that say "send
// this much of the inbound amount to this leg, targeting this
// member-side artefact". This PR persists the Plan to
// mpesa_distribution_runs.splits jsonb and posts the cash leg to
// posting_outbox. Phase 3.5 turns each Split into the matching
// savings-side write (deposit_transactions, loan_repayments, etc).
//
// Two execution shapes:
//   1. resolved_via='loan_no' or 'deposit_account_no' — direct
//      target. The whole inbound amount goes to that one target;
//      the waterfall is skipped.
//   2. Anything else (member_no / cp_number / msisdn / unallocated
//      that staff resolved manually) — walk the waterfall, asking
//      each leg's balance source for its outstanding amount,
//      reducing remaining as we go. Leftover lands in the final leg.
//
// Determinism: the engine is pure. Given the same inbound amount,
// the same paybill, the same policy, and the same DB state, Run
// produces an identical Plan every time. That property is what
// makes idempotent retries safe: a worker can crash mid-Run and
// the next attempt computes the same splits without observing the
// previous attempt's partial work.

package distribution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
)

// Balances is the read-only port the engine reaches through to ask
// "how much is owing?". Pure-table-test friendly — test files
// supply a mock that returns whatever balances the case needs.
type Balances interface {
	PrimaryActiveLoanTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (*store.LoanComponents, error)
	LoanByNoTx(ctx context.Context, tx pgx.Tx, loanNo string) (*store.LoanComponents, error)
	DepositAccountsTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) ([]store.DepositAccount, error)
	DepositAccountByNoTx(ctx context.Context, tx pgx.Tx, accountNo string) (*store.DepositAccount, error)
	FeesDueTx(ctx context.Context, tx pgx.Tx, cpID uuid.UUID) (decimal.Decimal, error)
}

// LegTarget is the canonical name for a waterfall leg. The set is
// finite + locked at engine compile time — a policy that lists an
// unknown target gets a skipped + warned-about leg.
type LegTarget string

const (
	LegFeesDue          LegTarget = "fees_due"
	LegLoanPenaltyDue   LegTarget = "loan_penalty_due"
	LegLoanInterestDue  LegTarget = "loan_interest_due"
	LegLoanPrincipalDue LegTarget = "loan_principal_due"
	LegLoanFeesDue      LegTarget = "loan_fees_due"
	LegBOSATopUp        LegTarget = "bosa_top_up"
	LegFOSATopUp        LegTarget = "fosa_top_up"
)

// DefaultWaterfall is what the engine uses when a paybill has no
// distribution_policy attached. Mirrors the spec's documented order.
var DefaultWaterfall = []LegTarget{
	LegFeesDue,
	LegLoanPenaltyDue,
	LegLoanInterestDue,
	LegLoanPrincipalDue,
	LegLoanFeesDue,
	LegBOSATopUp,
	LegFOSATopUp,
}

// Split is one entry in the Plan — the engine's verdict that
// `Amount` of the inbound payment should land against `TargetRef`
// via the `Leg` mechanism. The applier (phase 3.5) turns this into
// the matching savings-side write.
type Split struct {
	Leg       LegTarget       `json:"leg"`
	Amount    decimal.Decimal `json:"amount"`
	TargetRef string          `json:"target_ref"`
	// TargetID is the uuid behind TargetRef (loan_id, deposit_id, fee_id).
	// Populated when the engine knows; left zero for fees_due (which
	// would point at a fee schedule item that doesn't exist yet).
	TargetID *uuid.UUID `json:"target_id,omitempty"`
}

// Plan is the engine's complete output for one inbound event.
type Plan struct {
	EventID  uuid.UUID
	Amount   decimal.Decimal
	Splits   []Split
	// Leftover is the remainder if every leg's balance + capacity is
	// exhausted before the inbound amount runs out. The engine
	// always routes leftover to the LAST leg's TargetRef when one
	// exists; this field is the audit breadcrumb in case the last
	// leg also had zero capacity (e.g. no deposit account exists).
	Leftover decimal.Decimal
}

// ErrInputInvalid is returned when the engine refuses to plan
// because the inputs themselves are nonsensical (zero amount,
// missing resolved_member when waterfall is needed, etc).
var ErrInputInvalid = errors.New("distribution engine: invalid inputs")

// Run is the single entry point. The caller has already:
//   - WithTenantTx'd into the row's tenant.
//   - Locked the inbound_event row (SELECT … FOR UPDATE SKIP LOCKED).
//   - Confirmed the row's status is 'received' (anything else is a
//     replay attempt and the distributor returns early).
// Run does NOT mutate the inbound_event row; that's the
// distributor's job after Run returns successfully.
func Run(
	ctx context.Context,
	tx pgx.Tx,
	balances Balances,
	event *domain.InboundEvent,
	waterfall []LegTarget,
) (*Plan, error) {
	if event == nil {
		return nil, fmt.Errorf("%w: nil event", ErrInputInvalid)
	}
	amount, err := decimal.NewFromString(event.Amount)
	if err != nil {
		return nil, fmt.Errorf("%w: amount %q: %v", ErrInputInvalid, event.Amount, err)
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil, fmt.Errorf("%w: non-positive amount %s", ErrInputInvalid, amount)
	}
	if event.ResolvedMemberID == nil && event.ResolvedVia != nil && *event.ResolvedVia != domain.ViaUnallocated {
		return nil, fmt.Errorf("%w: resolved_via=%s but resolved_member_id is null",
			ErrInputInvalid, *event.ResolvedVia)
	}

	plan := &Plan{EventID: event.ID, Amount: amount}
	via := domain.ViaUnallocated
	if event.ResolvedVia != nil {
		via = *event.ResolvedVia
	}

	// Direct-target shortcut for explicit loan_no / deposit_account_no.
	// We don't consult the policy waterfall in these cases — the
	// payer's bill_ref named a specific target and the engine
	// honours that intent.
	switch via {
	case domain.ViaLoanNo:
		return runDirectLoan(ctx, tx, balances, event, amount, plan)
	case domain.ViaDepositAccountNo:
		return runDirectDeposit(ctx, tx, balances, event, amount, plan)
	}

	// Waterfall path. resolved_member_id might still be nil when
	// resolved_via='unallocated' — the engine produces an empty
	// plan with Leftover=amount, which the distributor turns into
	// "money parked in clearing, awaiting manual reconciliation".
	if event.ResolvedMemberID == nil {
		plan.Leftover = amount
		return plan, nil
	}
	return runWaterfall(ctx, tx, balances, *event.ResolvedMemberID, amount, waterfall, plan)
}

func runDirectLoan(
	ctx context.Context, tx pgx.Tx, balances Balances,
	event *domain.InboundEvent, amount decimal.Decimal, plan *Plan,
) (*Plan, error) {
	lc, err := balances.LoanByNoTx(ctx, tx, event.BillRef)
	if err != nil {
		// Resolver matched it but the loan vanished between resolve
		// and distribute — treat as unallocated. Run is not the
		// place to recover; the distributor records the failure +
		// retries.
		if errors.Is(err, store.ErrNotFound) {
			plan.Leftover = amount
			return plan, nil
		}
		return nil, err
	}
	remaining := amount
	for _, leg := range []struct {
		name LegTarget
		bal  decimal.Decimal
	}{
		{LegLoanPenaltyDue, lc.Penalty},
		{LegLoanInterestDue, lc.Interest},
		{LegLoanPrincipalDue, lc.Principal},
		{LegLoanFeesDue, lc.Fees},
	} {
		take := decMin(remaining, leg.bal)
		if take.IsZero() {
			continue
		}
		id := lc.LoanID
		plan.Splits = append(plan.Splits, Split{
			Leg: leg.name, Amount: take, TargetRef: lc.LoanNo, TargetID: &id,
		})
		remaining = remaining.Sub(take)
		if remaining.IsZero() {
			return plan, nil
		}
	}
	// Overpayment beyond the loan's balance — direct target said
	// "this loan", so we keep stacking onto principal as a customer-
	// service nicety. The applier in 3.5 handles the overpayment as
	// a prepayment.
	if remaining.GreaterThan(decimal.Zero) {
		id := lc.LoanID
		plan.Splits = append(plan.Splits, Split{
			Leg: LegLoanPrincipalDue, Amount: remaining,
			TargetRef: lc.LoanNo, TargetID: &id,
		})
	}
	return plan, nil
}

func runDirectDeposit(
	ctx context.Context, tx pgx.Tx, balances Balances,
	event *domain.InboundEvent, amount decimal.Decimal, plan *Plan,
) (*Plan, error) {
	da, err := balances.DepositAccountByNoTx(ctx, tx, event.BillRef)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			plan.Leftover = amount
			return plan, nil
		}
		return nil, err
	}
	leg := LegBOSATopUp
	if isFOSA(da.ProductCode) {
		leg = LegFOSATopUp
	}
	id := da.ID
	plan.Splits = append(plan.Splits, Split{
		Leg: leg, Amount: amount, TargetRef: da.AccountNo, TargetID: &id,
	})
	return plan, nil
}

func runWaterfall(
	ctx context.Context, tx pgx.Tx, balances Balances,
	cpID uuid.UUID, amount decimal.Decimal,
	waterfall []LegTarget, plan *Plan,
) (*Plan, error) {
	if len(waterfall) == 0 {
		waterfall = DefaultWaterfall
	}

	// Pre-fetch the loan + deposit shape ONCE; each waterfall leg
	// peeks at the right field rather than re-querying.
	var loan *store.LoanComponents
	if l, err := balances.PrimaryActiveLoanTx(ctx, tx, cpID); err == nil {
		loan = l
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	deposits, err := balances.DepositAccountsTx(ctx, tx, cpID)
	if err != nil {
		return nil, err
	}
	fees, err := balances.FeesDueTx(ctx, tx, cpID)
	if err != nil {
		return nil, err
	}

	bosa, fosa := pickDepositTargets(deposits)

	remaining := amount
	for i, leg := range waterfall {
		isLast := i == len(waterfall)-1
		take, ref, targetID := planLeg(leg, remaining, isLast, fees, loan, bosa, fosa)
		if take.IsZero() {
			continue
		}
		plan.Splits = append(plan.Splits, Split{
			Leg: leg, Amount: take, TargetRef: ref, TargetID: targetID,
		})
		remaining = remaining.Sub(take)
		if remaining.IsZero() {
			break
		}
	}
	plan.Leftover = remaining
	return plan, nil
}

// planLeg returns (amount-to-route, target-ref, target-id) for one
// waterfall leg. The `isLast` flag opts the leg into "take whatever's
// left" semantics — that's how leftover from upstream legs reaches
// BOSA/FOSA even when the leg's own "due" balance is zero.
func planLeg(
	leg LegTarget, remaining decimal.Decimal, isLast bool,
	fees decimal.Decimal,
	loan *store.LoanComponents,
	bosa, fosa *store.DepositAccount,
) (decimal.Decimal, string, *uuid.UUID) {
	switch leg {
	case LegFeesDue:
		take := decMin(remaining, fees)
		return take, "fees_due", nil
	case LegLoanPenaltyDue:
		if loan == nil {
			return decimal.Zero, "", nil
		}
		take := decMin(remaining, loan.Penalty)
		id := loan.LoanID
		return take, loan.LoanNo, &id
	case LegLoanInterestDue:
		if loan == nil {
			return decimal.Zero, "", nil
		}
		take := decMin(remaining, loan.Interest)
		id := loan.LoanID
		return take, loan.LoanNo, &id
	case LegLoanPrincipalDue:
		if loan == nil {
			return decimal.Zero, "", nil
		}
		take := decMin(remaining, loan.Principal)
		id := loan.LoanID
		return take, loan.LoanNo, &id
	case LegLoanFeesDue:
		if loan == nil {
			return decimal.Zero, "", nil
		}
		take := decMin(remaining, loan.Fees)
		id := loan.LoanID
		return take, loan.LoanNo, &id
	case LegBOSATopUp:
		if bosa == nil {
			return decimal.Zero, "", nil
		}
		// BOSA always accepts whatever's left when it's the last
		// leg, or when no FOSA exists later in the chain.
		take := remaining
		if !isLast && fosa != nil {
			// Phase 3 keeps the "all leftover to BOSA when no FOSA"
			// behaviour the spec calls out. When FOSA exists we
			// route nothing to BOSA from THIS leg — the policy can
			// still put BOSA first if the tenant wants that.
			take = decimal.Zero
		}
		id := bosa.ID
		return take, bosa.AccountNo, &id
	case LegFOSATopUp:
		if fosa == nil {
			// No FOSA account: spec says "100% leftover into BOSA".
			// If we've already passed the BOSA leg this is moot;
			// if BOSA didn't take (because FOSA was expected) we
			// route the leftover to BOSA here.
			if bosa != nil && isLast {
				id := bosa.ID
				return remaining, bosa.AccountNo, &id
			}
			return decimal.Zero, "", nil
		}
		id := fosa.ID
		return remaining, fosa.AccountNo, &id
	}
	// Unknown leg target — engine skips silently. Phase 3.5 may
	// emit a warning to a structured log, but the engine itself
	// stays panic-free on policy data it doesn't recognise.
	return decimal.Zero, "", nil
}

func pickDepositTargets(accounts []store.DepositAccount) (bosa, fosa *store.DepositAccount) {
	for i := range accounts {
		if isFOSA(accounts[i].ProductCode) && fosa == nil {
			fosa = &accounts[i]
		} else if bosa == nil {
			bosa = &accounts[i]
		}
	}
	return
}

func isFOSA(productCode string) bool {
	// FOSA = front-office savings; tenants tag the product code
	// with a "FOSA" prefix by convention. Phase 3.5 introduces a
	// typed product classification column that supersedes this.
	if len(productCode) < 4 {
		return false
	}
	return productCode[:4] == "FOSA" || productCode[:4] == "fosa"
}

func decMin(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}
