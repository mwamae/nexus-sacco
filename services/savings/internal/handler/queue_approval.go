// queueApproval — shared seam every Approvals.QueueTx call site uses
// during the unified-approvals migration window. Decides between two
// paths inside the caller's tx:
//
//   1. NEW: workflow engine. When the tenant has an active
//      wf_definition for the matching process_kind, create a
//      wf_instance via workflowclient. Returns a synthetic
//      *domain.PendingApproval pointing at the instance — the
//      response shape stays stable for the HTTP handlers above
//      that still encode this as their 202 body. The wf instance
//      carries the callback URL pointing at savings'
//      /internal/v1/workflow-terminal-action; on terminal transition
//      the callback-dispatcher fires the matching wf_callbacks/<kind>.go
//      executor.
//
//   2. LEGACY: today's pending_approvals row. Falls through here
//      when no wf_definition is active for the kind. After the
//      legacy-approvals-migrate backfill runs (Part 4) every kind
//      will have an active definition and this branch becomes dead;
//      Part 5 removes it together with Approvals.QueueTx +
//      executePayloadTx.

package handler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/domain"
	"github.com/nexussacco/savings/internal/store"
	"github.com/nexussacco/savings/internal/workflowclient"
)

// QueueApprovalDeps is the bag of handles every cash-handling
// handler already carries — passed into queueApproval so the
// helper stays a pure function (no method receiver).
type QueueApprovalDeps struct {
	Workflow       *workflowclient.Client
	Approvals      *store.ApprovalsStore
	SavingsSelfURL string
}

// QueueApprovalInput is the per-call payload. Kind drives both the
// legacy QueueInput.Kind and the wf process_kind lookup via
// processKindForApprovalKind.
type QueueApprovalInput struct {
	TenantID  uuid.UUID
	Kind      domain.ApprovalKind
	Title     string
	SubjectID uuid.UUID // primary subject id, also used for the wf SourceURL

	// Optional subject identifiers — populated for the legacy
	// QueueInput shape and surfaced in the wf SourceURL helper.
	SubjectMemberID  *uuid.UUID
	SubjectAccountID *uuid.UUID
	SubjectLoanID    *uuid.UUID

	Amount      *decimal.Decimal
	Payload     any // original request body; serialised into wf context.payload + legacy QueueInput.Payload
	MakerUserID uuid.UUID

	// SummarySuffix is appended to Title for the wf instance summary
	// (e.g. " — 1,500.00"). Optional; empty means no suffix.
	SummarySuffix string
}

// queueApproval performs the wf-vs-legacy branch. Returns the synthetic
// or real *domain.PendingApproval so callers can keep the existing
// response shape unchanged through the migration window.
func queueApproval(
	ctx context.Context, tx pgx.Tx,
	deps QueueApprovalDeps, in QueueApprovalInput,
) (*domain.PendingApproval, error) {
	processKind := ProcessKindForApprovalKind(in.Kind)

	// Workflow path — active wf_definition wins.
	if deps.Workflow != nil && processKind != "" &&
		deps.Workflow.HasActiveDefinitionTx(ctx, tx, in.TenantID, processKind) {

		subjectID := in.SubjectID
		if subjectID == uuid.Nil {
			// Fall back to the legacy subject pointers in priority
			// order so callers that only populate one of them still
			// produce a valid wf_instance.subject_id.
			switch {
			case in.SubjectAccountID != nil:
				subjectID = *in.SubjectAccountID
			case in.SubjectLoanID != nil:
				subjectID = *in.SubjectLoanID
			case in.SubjectMemberID != nil:
				subjectID = *in.SubjectMemberID
			default:
				return nil, fmt.Errorf("queueApproval: no subject id for kind %s", in.Kind)
			}
		}

		summary := in.Title
		if in.SummarySuffix != "" {
			summary = in.Title + in.SummarySuffix
		}

		instanceID, err := deps.Workflow.CreateInstanceTx(ctx, tx, workflowclient.CreateInstanceInput{
			TenantID:    in.TenantID,
			ProcessKind: processKind,
			SubjectKind: SubjectKindFor(in.Kind),
			SubjectID:   subjectID,
			Context:     map[string]any{"payload": in.Payload},
			Summary:     summary,
			SourceURL:   SourceURLFor(in.Kind, subjectID),
			CallbackURL: deps.SavingsSelfURL + "/internal/v1/workflow-terminal-action",
			MakerUserID: in.MakerUserID,
		})
		if err != nil {
			return nil, err
		}
		// Synthesize a PendingApproval response so HTTP responders
		// that still encode this struct keep working unchanged.
		return &domain.PendingApproval{
			ID:               instanceID,
			TenantID:         in.TenantID,
			Kind:             in.Kind,
			Status:           domain.ApprovalStatusPending,
			Title:            in.Title,
			SubjectMemberID:  in.SubjectMemberID,
			SubjectAccountID: in.SubjectAccountID,
			SubjectLoanID:    in.SubjectLoanID,
			Amount:           in.Amount,
			MakerUserID:      in.MakerUserID,
		}, nil
	}

	// Legacy fallback — original pending_approvals path. Removed
	// in P5 once the backfill (P4) has migrated every in-flight row.
	return deps.Approvals.QueueTx(ctx, tx, store.QueueInput{
		Kind:             in.Kind,
		Title:            in.Title,
		SubjectMemberID:  in.SubjectMemberID,
		SubjectAccountID: in.SubjectAccountID,
		SubjectLoanID:    in.SubjectLoanID,
		Amount:           in.Amount,
		Payload:          in.Payload,
		MakerUserID:      in.MakerUserID,
	})
}
