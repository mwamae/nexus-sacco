// Posting-outbox writer — shared by the finance executors so both
// savings and mpesa generate identical jsonb shapes for the GL
// dispatcher. Mirrors services/savings/internal/posting/client.go's
// PostTx path; the HTTP-direct Post() entry isn't needed here
// because the finance package only writes to the outbox.

package posting

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

// ErrOutboxInsert wraps any failure of the in-tx outbox INSERT. The
// caller's business write should roll back on a non-nil return.
var ErrOutboxInsert = errors.New("finance/posting: outbox insert failed")

// Line is one debit OR credit leg.
type Line struct {
	AccountCode string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	Narration   string
}

// Input is one journal entry to enqueue.
type Input struct {
	TenantID     uuid.UUID
	EntryDate    time.Time
	ValueDate    time.Time
	SourceModule string
	SourceRef    string
	Narration    string
	Lines        []Line
}

type lineDTO struct {
	AccountCode string `json:"account_code"`
	Debit       string `json:"debit,omitempty"`
	Credit      string `json:"credit,omitempty"`
	Narration   string `json:"narration,omitempty"`
}

type payload struct {
	TenantID     uuid.UUID `json:"tenant_id"`
	EntryDate    string    `json:"entry_date,omitempty"`
	ValueDate    string    `json:"value_date,omitempty"`
	SourceModule string    `json:"source_module"`
	SourceRef    string    `json:"source_ref"`
	Narration    string    `json:"narration"`
	Lines        []lineDTO `json:"lines"`
}

// PostTx writes a posting_outbox row inside the caller's tx. The
// accounting dispatcher drains the outbox on its own cadence. Idempotent
// on (source_module, source_ref) — duplicate inserts are caught downstream.
func PostTx(ctx context.Context, tx pgx.Tx, in Input) (uuid.UUID, error) {
	if len(in.Lines) < 2 {
		return uuid.Nil, fmt.Errorf("finance/posting: at least two lines required")
	}
	if in.SourceModule == "" || in.SourceRef == "" {
		return uuid.Nil, fmt.Errorf("finance/posting: source_module and source_ref required")
	}
	lines := make([]lineDTO, 0, len(in.Lines))
	for _, ln := range in.Lines {
		l := lineDTO{AccountCode: ln.AccountCode, Narration: ln.Narration}
		if !ln.Debit.IsZero() {
			l.Debit = ln.Debit.StringFixed(2)
		}
		if !ln.Credit.IsZero() {
			l.Credit = ln.Credit.StringFixed(2)
		}
		lines = append(lines, l)
	}
	body := payload{
		TenantID: in.TenantID, SourceModule: in.SourceModule, SourceRef: in.SourceRef,
		Narration: in.Narration, Lines: lines,
	}
	if !in.EntryDate.IsZero() {
		body.EntryDate = in.EntryDate.Format("2006-01-02")
	}
	if !in.ValueDate.IsZero() {
		body.ValueDate = in.ValueDate.Format("2006-01-02")
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: marshal: %v", ErrOutboxInsert, err)
	}
	var outboxID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO posting_outbox (tenant_id, payload)
		VALUES ($1, $2::jsonb)
		RETURNING id
	`, in.TenantID, buf).Scan(&outboxID); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrOutboxInsert, err)
	}
	return outboxID, nil
}
