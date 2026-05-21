// Posting engine — the single public surface that every transactional
// module (manual entries, savings, loans, shares, …) uses to write a
// balanced double-entry to the General Ledger.
//
// The engine enforces three invariants on every post:
//   1. The accounting period for the entry_date must exist and be open.
//   2. Total debits must equal total credits (delegated to the store).
//   3. Every referenced account must exist + be active + belong to
//      the calling tenant (RLS handles the tenant check; we explicitly
//      reject inactive accounts here so a typo in a posting rule
//      surfaces immediately instead of silently slipping in).
//
// The engine takes a pgx.Tx so the caller can compose posting with
// their own business writes — e.g. the savings handler inserts a
// deposit_transactions row AND posts to the GL in the same tx, so a
// failure on either side rolls both back.

package posting

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/accounting/internal/domain"
	"github.com/nexussacco/accounting/internal/store"
)

type Engine struct {
	CoA      *store.CoAStore
	Periods  *store.PeriodStore
	Journals *store.JournalStore
}

// Line — caller-facing representation of one leg of an entry. Use the
// account code (not the UUID) so callers don't have to look up accounts
// themselves; the engine resolves codes to ids and rejects unknowns.
type Line struct {
	AccountCode string
	Debit       decimal.Decimal
	Credit      decimal.Decimal
	Narration   string
}

type PostInput struct {
	EntryDate    time.Time
	ValueDate    time.Time
	EntryType    domain.JournalEntryType
	SourceModule string // e.g. "savings", "loans", "shares"
	SourceRef    string // upstream transaction id (free-form)
	Narration    string
	Lines        []Line
	PostedBy     *uuid.UUID
}

var (
	ErrUnknownAccount  = errors.New("posting: account code not found in chart of accounts")
	ErrInactiveAccount = errors.New("posting: account is inactive")
)

// PostTx writes the entry within the caller's pgx.Tx (which must
// already be tenant-scoped via db.WithTenantTx). Returns the posted
// journal entry on success.
func (e *Engine) PostTx(ctx context.Context, tx pgx.Tx, in PostInput) (*domain.JournalEntry, error) {
	if in.EntryDate.IsZero() {
		in.EntryDate = time.Now()
	}
	if in.ValueDate.IsZero() {
		in.ValueDate = in.EntryDate
	}
	if in.EntryType == "" {
		in.EntryType = domain.TypeAuto
	}

	// Confirm (or open) the period for the entry date. Bounces with
	// ErrPeriodClosed if the period was previously closed.
	if _, err := e.Periods.EnsureOpenForDateTx(ctx, tx, in.EntryDate); err != nil {
		return nil, err
	}

	// Resolve every account code to an id. Cache within the call so a
	// 5-line entry that touches one account twice only hits the DB once.
	storeLines := make([]store.EntryLineInput, 0, len(in.Lines))
	accountCache := map[string]uuid.UUID{}
	for i, ln := range in.Lines {
		id, ok := accountCache[ln.AccountCode]
		if !ok {
			a, err := e.CoA.GetByCodeTx(ctx, tx, ln.AccountCode)
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("%w: %q (line %d)", ErrUnknownAccount, ln.AccountCode, i+1)
			}
			if err != nil {
				return nil, err
			}
			if !a.IsActive {
				return nil, fmt.Errorf("%w: %q (line %d)", ErrInactiveAccount, ln.AccountCode, i+1)
			}
			id = a.ID
			accountCache[ln.AccountCode] = id
		}
		storeLines = append(storeLines, store.EntryLineInput{
			AccountID: id,
			Debit:     ln.Debit,
			Credit:    ln.Credit,
			Narration: ln.Narration,
		})
	}

	return e.Journals.InsertPostedTx(ctx, tx, store.InsertPostedInput{
		EntryDate:    in.EntryDate,
		ValueDate:    in.ValueDate,
		EntryType:    in.EntryType,
		SourceModule: in.SourceModule,
		SourceRef:    in.SourceRef,
		Narration:    in.Narration,
		Lines:        storeLines,
		PostedBy:     in.PostedBy,
	})
}
