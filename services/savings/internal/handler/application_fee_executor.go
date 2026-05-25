// Executor for queued application-fee approvals.
//
// When approval_application_fee is ON (the safe default), the member
// service inserts the application_fee_payments row with
// journal_entry_id NULL and queues a pending_approvals row of kind
// 'application_fee'. The dispatcher in pending_approvals.go calls
// PostApprovedTx on approve.
//
// The executor lives in the savings package — it owns the posting
// client — and reaches across to the member-owned
// application_fee_payments table via direct shared-DB writes. Same
// pattern as the receipt-stamp executor (services/member's
// ApplicationFeeReceiptStamper); the FK between the two tables is
// the natural seam.

package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/savings/internal/posting"
)

// ApplicationFeePayload is what the member-side queue handler
// stores on the pending_approvals row. The executor re-loads the
// payment row to honour any concurrent void during the pending
// window — the payload is just the join key + the data needed to
// post the JE without another SELECT.
type ApplicationFeePayload struct {
	ApplicationID    uuid.UUID       `json:"application_id"`
	ApplicationNo    string          `json:"application_no"`
	PaymentID        uuid.UUID       `json:"payment_id"`
	Amount           decimal.Decimal `json:"amount"`
	Channel          string          `json:"channel"`
	ChannelReference *string         `json:"channel_reference,omitempty"`
	ValueDate        time.Time       `json:"value_date"`
}

// ApplicationFeeExecutor — slim wrapper around the posting client +
// a Logger so the dispatcher can call it without growing a new
// concrete handler dep.
type ApplicationFeeExecutor struct {
	Posting *posting.Client
	Logger  *slog.Logger
}

// PostApprovedTx is invoked from the dispatcher on approve. It
// posts the GL entry (DR channel-cash / CR 4080) then stamps the
// application_fee_payments row's journal_entry_id + posted_at.
// Voided payments short-circuit with a typed error so the
// dispatcher records an execution failure rather than silently
// no-oping.
//
// Returns the JE id stamped on the payment row. Returns uuid.Nil
// when posting is disabled (dev): a synthetic id is still stamped
// on the payment so the row's posted_at fires, mirroring the
// behaviour of the at-create-time inline path.
func (e *ApplicationFeeExecutor) PostApprovedTx(
	ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p ApplicationFeePayload,
) (uuid.UUID, error) {
	if e == nil {
		return uuid.Nil, errors.New("application fee executor not configured")
	}

	// Pre-check: is the payment still live?
	var (
		voidedAt       *time.Time
		alreadyJEStr   *string
		liveAmount     decimal.Decimal
	)
	if err := tx.QueryRow(ctx,
		`SELECT voided_at, journal_entry_id::text, amount
		   FROM application_fee_payments WHERE id = $1`, p.PaymentID,
	).Scan(&voidedAt, &alreadyJEStr, &liveAmount); err != nil {
		return uuid.Nil, fmt.Errorf("load application_fee_payment %s: %w", p.PaymentID, err)
	}
	if voidedAt != nil {
		return uuid.Nil, errors.New("application fee payment is voided")
	}
	if alreadyJEStr != nil && *alreadyJEStr != "" {
		// Idempotency: dispatcher already posted once. Return the
		// existing id so the pending_approvals row gets the same
		// result it would have produced first time.
		var existing uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT journal_entry_id FROM application_fee_payments WHERE id = $1`, p.PaymentID,
		).Scan(&existing); err != nil {
			return uuid.Nil, err
		}
		return existing, nil
	}

	cashAcct := registrationChannelCashAccountSavings(p.Channel)
	narration := fmt.Sprintf("Application fee · %s · %s",
		p.ApplicationNo, liveAmount.StringFixed(2))
	// Savings's posting client doesn't return the JE id; the
	// accounting service dedupes on (source_module, source_ref) so
	// we generate the source_ref upfront and stamp it on the
	// payment as the journal_entry_id handle (matches the pattern
	// in postFeeLineTx + provisioning.go).
	jeID := uuid.New()
	if e.Posting != nil && !e.Posting.Disabled {
		if perr := e.Posting.Post(ctx, posting.PostInput{
			TenantID:     tenantID,
			EntryDate:    time.Now(),
			ValueDate:    p.ValueDate,
			SourceModule: "member.application.fee",
			SourceRef:    jeID.String(),
			Narration:    narration,
			Lines: []posting.Line{
				{AccountCode: cashAcct, Debit: liveAmount, Narration: "Cash in via " + p.Channel},
				{AccountCode: "4080", Credit: liveAmount, Narration: "Registration fee income"},
			},
		}); perr != nil {
			if !errors.Is(perr, posting.ErrPostingDisabled) {
				if e.Logger != nil {
					e.Logger.Error("application fee approval — GL post failed",
						"payment", p.PaymentID, "err", perr)
				}
				return uuid.Nil, perr
			}
			// Posting disabled — fall through with the synthetic id.
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE application_fee_payments
		   SET journal_entry_id = $2, posted_at = now()
		 WHERE id = $1
	`, p.PaymentID, jeID); err != nil {
		return uuid.Nil, fmt.Errorf("stamp journal_entry_id: %w", err)
	}

	// Recompute the parent application's denormalised aggregates so
	// fee_amount_paid / fee_status reflect this approval landing.
	if _, err := tx.Exec(ctx, `
		UPDATE membership_applications a
		   SET fee_amount_paid       = COALESCE((
		         SELECT SUM(amount) FROM application_fee_payments
		          WHERE application_id = a.id AND voided_at IS NULL
		       ), 0),
		       fee_status            = CASE
		         WHEN a.fee_amount_due = 0 THEN 'not_required'
		         WHEN COALESCE((SELECT SUM(amount) FROM application_fee_payments WHERE application_id = a.id AND voided_at IS NULL), 0) >= a.fee_amount_due THEN 'paid'
		         WHEN COALESCE((SELECT SUM(amount) FROM application_fee_payments WHERE application_id = a.id AND voided_at IS NULL), 0) > 0 THEN 'shortfall'
		         ELSE 'not_paid'
		       END,
		       updated_at            = now()
		 WHERE a.id = $1
	`, p.ApplicationID); err != nil {
		return uuid.Nil, fmt.Errorf("recompute application fee aggregates: %w", err)
	}

	if e.Logger != nil {
		e.Logger.Info("application fee approval — posted",
			"application", p.ApplicationNo, "payment", p.PaymentID,
			"amount", liveAmount.StringFixed(2), "je", jeID)
	}
	return jeID, nil
}

// registrationChannelCashAccountSavings mirrors the member-service
// helper of the same name. Duplicated rather than imported because
// the member package's handler is not importable from savings (and
// the mapping is a stable two-line table).
func registrationChannelCashAccountSavings(channel string) string {
	switch channel {
	case "mpesa":
		return "1030"
	case "airtel_money":
		return "1040"
	case "bank_transfer", "cheque":
		return "1020"
	default: // cash + unknown fallback
		return "1000"
	}
}
