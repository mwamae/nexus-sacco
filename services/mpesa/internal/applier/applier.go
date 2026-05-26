// Applier — turns a finished distribution.Plan into the savings-side
// writes that make the receipt visible on the member's profile.
//
// One ApplyPlanTx call per Plan, runs inside the orchestrator's
// existing tenant tx. Each Split routes to the right finance
// executor; the executor's table write + GL outbox row commit
// atomically with the rest of the orchestrator's bookkeeping.
//
// external_validation_ref is set to the Safaricom MpesaReceiptNumber
// on every executor call. The downstream collection-desk router
// uses that to skip the approval gate; the savings handlers that
// don't go through the router (deposit / loan-repayment direct
// endpoints) ignore the field — it's just an audit breadcrumb on
// the row.

package applier

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	financeexec "github.com/nexussacco/finance/executor"
	financetypes "github.com/nexussacco/finance/types"

	"github.com/nexussacco/mpesa/internal/distribution"
	"github.com/nexussacco/mpesa/internal/domain"
)

// ApplyInput / ApplyResult / ApplySplitResult are re-exports of
// the orchestrator-facing types defined in the distribution
// package. Re-exporting keeps callers from chasing two import paths
// for the same shapes.
type ApplyInput = distribution.ApplyInput
type ApplyResult = distribution.ApplyResult
type ApplySplitResult = distribution.ApplySplitResult

// ApplyPlanTx iterates the plan's splits in order. Each split routes
// to the right executor based on its Leg. A split with an empty
// TargetID is skipped with a warning (the engine should not produce
// those when the apply path is wired correctly; a defensive skip
// keeps a malformed plan from breaking the whole tx).
//
// Signature matches distribution.ApplyFn so cmd/distributor/main.go
// can wire it in directly as orch.Apply.
func ApplyPlanTx(ctx context.Context, tx pgx.Tx, in ApplyInput) (*ApplyResult, error) {
	if in.Plan == nil {
		return nil, errors.New("applier: nil plan")
	}
	if in.ExternalValidationRef == "" {
		return nil, errors.New("applier: ExternalValidationRef is required (Safaricom receipt id)")
	}
	results := make([]ApplySplitResult, 0, len(in.Plan.Splits))
	loanAlloc := financetypes.Allocation{} // accumulated per-loan allocation
	var loanID uuid.UUID

	// Group consecutive loan splits into ONE RepayLoanTx call so
	// the executor sees the full allocation in a single hit (fewer
	// loan_transactions rows; matches savings's contract that one
	// repayment = one loan_transactions row with all components).
	flushLoan := func() error {
		if loanID == uuid.Nil || loanAlloc.Total().IsZero() {
			loanAlloc = financetypes.Allocation{}
			loanID = uuid.Nil
			return nil
		}
		res, err := financeexec.RepayLoanTx(ctx, tx, financetypes.RepayLoanInput{
			TenantID:              in.TenantID,
			LoanID:                loanID,
			Allocation:            loanAlloc,
			Channel:               in.Channel,
			ChannelRef:            in.ChannelRef,
			Narration:             "M-PESA repayment · " + in.ExternalValidationRef,
			ValueDate:             in.ValueDate,
			InitiatedBy:           in.InitiatedBy,
			ExternalValidationRef: in.ExternalValidationRef,
		})
		if err != nil {
			return fmt.Errorf("repay loan %s: %w", loanID, err)
		}
		results = append(results, ApplySplitResult{
			Leg:      distribution.LegLoanPrincipalDue, // representative leg
			Amount:   loanAlloc.Total(),
			TxnID:    res.TxnID,
			OutboxID: res.OutboxID,
		})
		loanAlloc = financetypes.Allocation{}
		loanID = uuid.Nil
		return nil
	}

	for _, sp := range in.Plan.Splits {
		switch sp.Leg {
		case distribution.LegLoanPenaltyDue,
			distribution.LegLoanInterestDue,
			distribution.LegLoanPrincipalDue,
			distribution.LegLoanFeesDue:
			if sp.TargetID == nil {
				return nil, fmt.Errorf("applier: loan split missing target_id (leg=%s)", sp.Leg)
			}
			if loanID != uuid.Nil && loanID != *sp.TargetID {
				// Different loan — flush the previous group first.
				if err := flushLoan(); err != nil {
					return nil, err
				}
			}
			loanID = *sp.TargetID
			switch sp.Leg {
			case distribution.LegLoanPenaltyDue:
				loanAlloc.Penalty = loanAlloc.Penalty.Add(sp.Amount)
			case distribution.LegLoanInterestDue:
				loanAlloc.Interest = loanAlloc.Interest.Add(sp.Amount)
			case distribution.LegLoanPrincipalDue:
				loanAlloc.Principal = loanAlloc.Principal.Add(sp.Amount)
			case distribution.LegLoanFeesDue:
				loanAlloc.Fees = loanAlloc.Fees.Add(sp.Amount)
			}

		case distribution.LegBOSATopUp, distribution.LegFOSATopUp:
			// Flush any pending loan allocation first to keep the
			// receipt ordering predictable in the GL.
			if err := flushLoan(); err != nil {
				return nil, err
			}
			if sp.TargetID == nil {
				return nil, fmt.Errorf("applier: deposit split missing target_id (leg=%s)", sp.Leg)
			}
			res, err := financeexec.DepositTx(ctx, tx, financetypes.DepositInput{
				TenantID:              in.TenantID,
				AccountID:             *sp.TargetID,
				Amount:                sp.Amount,
				Channel:               "mpesa",
				ChannelRef:            in.ChannelRef,
				Narration:             "M-PESA " + string(sp.Leg) + " · " + in.ExternalValidationRef,
				ValueDate:             in.ValueDate,
				InitiatedBy:           in.InitiatedBy,
				ExternalValidationRef: in.ExternalValidationRef,
			})
			if err != nil {
				return nil, fmt.Errorf("deposit %s: %w", sp.TargetRef, err)
			}
			results = append(results, ApplySplitResult{
				Leg: sp.Leg, Amount: sp.Amount, TxnID: res.TxnID, OutboxID: res.OutboxID,
			})

		case distribution.LegFeesDue:
			if err := flushLoan(); err != nil {
				return nil, err
			}
			// The engine's fees_due leg sums all open fees; we
			// apply against the catalog by code. Phase 3.5 passes
			// a single "MPESA" generic code; phase 4 lets policies
			// nominate which fee codes get the cash and routes
			// per-row.
			res, err := financeexec.PostFeeTx(ctx, tx, financetypes.PostFeeInput{
				TenantID:              in.TenantID,
				FeeCode:               feeCodeForSplit(sp),
				Amount:                sp.Amount,
				Channel:               "mpesa",
				ChannelRef:            in.ChannelRef,
				Narration:             "M-PESA fees · " + in.ExternalValidationRef,
				ValueDate:             in.ValueDate,
				InitiatedBy:           in.InitiatedBy,
				ExternalValidationRef: in.ExternalValidationRef,
			})
			if err != nil {
				return nil, fmt.Errorf("fee %s: %w", sp.TargetRef, err)
			}
			results = append(results, ApplySplitResult{
				Leg: sp.Leg, Amount: sp.Amount, TxnID: res.TxnID, OutboxID: res.OutboxID,
			})

		default:
			// Unknown leg — log + skip rather than fail the whole
			// apply. The orchestrator captures this in audit via
			// the leftover metric.
			continue
		}
	}
	if err := flushLoan(); err != nil {
		return nil, err
	}
	return &ApplyResult{SplitResults: results}, nil
}

// feeCodeForSplit picks the fee code to charge the split against.
// Phase 3.5 routes all generic "fees_due" splits to a single
// MPESA_INCOMING fee code (callers may have seeded the catalog
// with that ahead of time); phase 4 lets policies override.
func feeCodeForSplit(sp distribution.Split) string {
	if sp.TargetRef != "" && sp.TargetRef != "fees_due" {
		return sp.TargetRef
	}
	return "MPESA_INCOMING"
}

// Compile-time guard that the domain.ResolvedVia constants are
// still in lockstep with what the orchestrator passes here.
var _ = domain.ViaUnallocated
