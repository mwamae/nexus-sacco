// Minimal posting client — mirrors services/savings/internal/posting
// just enough for the mpesa distributor to write into the shared
// posting_outbox without dragging the full savings package in.
//
// The phase-3 (defer-apply) distributor only ever posts ONE GL
// entry per inbound event: the cash leg that records "money landed
// in M-PESA clearing". Two lines, two account codes — that's it.
// Phase 3.5 fans out per-split GL entries; that's still a
// posting_outbox write, just N rows instead of one.
//
// The Source* fields are what make this idempotent: the accounting
// service dedups on (source_module, source_ref). The distributor
// uses the inbound_event.id as source_ref so a worker crash + retry
// produces zero duplicates.

package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// ErrOutboxInsert wraps any failure of the in-tx outbox INSERT.
// Distributor surfaces this as a retryable error (the row stays
// 'received' with attempts++).
var ErrOutboxInsert = errors.New("mpesa: posting outbox insert failed")

type postLine struct {
	AccountCode string `json:"account_code"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Narration   string `json:"narration,omitempty"`
}

type postPayload struct {
	TenantID     uuid.UUID  `json:"tenant_id"`
	EntryDate    string     `json:"entry_date,omitempty"`
	ValueDate    string     `json:"value_date,omitempty"`
	SourceModule string     `json:"source_module"`
	SourceRef    string     `json:"source_ref"`
	Narration    string     `json:"narration"`
	Lines        []postLine `json:"lines"`
}

// PostCashLegTx writes the inbound event's cash leg into
// posting_outbox. Returns the row's id so the distributor can
// stamp posting_journal_id on the matching mpesa_distribution_runs
// row.
//
// Lines:
//   DR  cashAccountCode       amount (the paybill's Daraja clearing GL)
//   CR  clearingAccountCode   amount (1099 M-PESA unallocated by default)
//
// Phase 3.5 will add the CR-side reallocation entries as additional
// posting_outbox rows when each split is applied to its target.
func PostCashLegTx(
	ctx context.Context, tx pgx.Tx,
	tenantID, eventID uuid.UUID,
	amount decimal.Decimal,
	cashAccount, clearingAccount string,
	valueDate time.Time,
) (uuid.UUID, error) {
	if cashAccount == "" || clearingAccount == "" {
		return uuid.Nil, fmt.Errorf("%w: missing account code", ErrOutboxInsert)
	}
	if amount.LessThanOrEqual(decimal.Zero) {
		return uuid.Nil, fmt.Errorf("%w: non-positive amount %s", ErrOutboxInsert, amount)
	}
	payload := postPayload{
		TenantID:     tenantID,
		SourceModule: "mpesa.distribution.cash_leg",
		SourceRef:    eventID.String(),
		Narration:    "M-PESA C2B inbound — parked in clearing",
		Lines: []postLine{
			{AccountCode: cashAccount, Debit: amount.StringFixed(2), Narration: "M-PESA receipts"},
			{AccountCode: clearingAccount, Credit: amount.StringFixed(2), Narration: "M-PESA clearing"},
		},
	}
	if !valueDate.IsZero() {
		payload.ValueDate = valueDate.Format("2006-01-02")
		payload.EntryDate = payload.ValueDate
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: marshal: %v", ErrOutboxInsert, err)
	}
	var outboxID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO posting_outbox (tenant_id, payload)
		VALUES ($1, $2::jsonb)
		RETURNING id
	`, tenantID, buf).Scan(&outboxID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrOutboxInsert, err)
	}
	return outboxID, nil
}
