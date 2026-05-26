// Member resolver for C2B confirmation events (§3.3).
//
// The resolver tries six branches in priority order and returns the
// counterparty.id of the first match, or a "no match" verdict that
// the confirmation handler turns into an mpesa_unallocated reconciliation
// task. Each branch carries its own resolved_via value so the audit
// log makes the decision replayable.
//
// Priority order (locked by spec):
//   1. BillRefNumber == members.member_no              → member_no
//   2. BillRefNumber == counterparties.cp_number       → cp_number
//   3. BillRefNumber == loans.loan_no                  → loan_no
//   4. BillRefNumber == deposit_accounts.account_no    → deposit_account_no
//   5. MSISDN ≈ counterparties.contact->>'phone'       → msisdn
//      (opt-in per paybill via mpesa_paybills.allow_msisdn_fallback)
//   6. None of the above                               → unallocated
//
// First match wins. The order is deliberate: explicit account
// identifiers beat phone-number guesses, and the most generic (cp_number)
// loses to the most-specific (loan_no / deposit_account_no) when both
// could match (in practice they never numerically collide because each
// identifier carries its own prefix — `M-…`, `CP-…`, `L-…`, `DA-…` — but
// the ordering also handles intentional sabotage where a member tries
// to nudge funds toward a particular account by mistyping their
// member_no into the loan_no field).
//
// The resolver is a pure function over an injected `Lookups` port so
// it can be table-tested without standing up the DB. The handler
// wires the real implementation backed by ResolverLookups in store/.

package distribution

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
)

// Lookups is the resolver's data port. Each method either returns a
// counterparty.id on a successful match or store.ErrNotFound. Any
// other error (DB down, etc.) is propagated as-is to the caller so
// the handler can decide whether to fail the request or persist with
// resolver_status='failed' for replay.
type Lookups interface {
	ByMemberNoTx(ctx context.Context, tx pgx.Tx, memberNo string) (uuid.UUID, error)
	ByCPNumberTx(ctx context.Context, tx pgx.Tx, cpNo string) (uuid.UUID, error)
	ByLoanNoTx(ctx context.Context, tx pgx.Tx, loanNo string) (uuid.UUID, error)
	ByDepositAccountNoTx(ctx context.Context, tx pgx.Tx, accountNo string) (uuid.UUID, error)
	ByMSISDNTx(ctx context.Context, tx pgx.Tx, msisdn string) (uuid.UUID, error)
}

// Decision is the resolver's verdict. MemberID is uuid.Nil on
// Via=='unallocated'; non-nil for every other Via.
type Decision struct {
	MemberID uuid.UUID
	Via      domain.ResolvedVia
}

// Input bundles what the resolver actually needs. The handler picks
// these out of the inbound event + the paybill row.
type Input struct {
	BillRef             string
	MSISDN              string
	AllowMSISDNFallback bool
}

// Resolve runs the six branches against the supplied Lookups. The
// caller passes a tx so the lookups stay inside the same
// tenant-scoped transaction the confirmation handler opened — that
// guarantees RLS isolates the resolver's reads to the correct tenant.
func Resolve(ctx context.Context, tx pgx.Tx, look Lookups, in Input) (Decision, error) {
	if id, err := tryLookup(look.ByMemberNoTx, ctx, tx, in.BillRef); !isMiss(err) {
		if err != nil {
			return Decision{}, err
		}
		return Decision{MemberID: id, Via: domain.ViaMemberNo}, nil
	}
	if id, err := tryLookup(look.ByCPNumberTx, ctx, tx, in.BillRef); !isMiss(err) {
		if err != nil {
			return Decision{}, err
		}
		return Decision{MemberID: id, Via: domain.ViaCPNumber}, nil
	}
	if id, err := tryLookup(look.ByLoanNoTx, ctx, tx, in.BillRef); !isMiss(err) {
		if err != nil {
			return Decision{}, err
		}
		return Decision{MemberID: id, Via: domain.ViaLoanNo}, nil
	}
	if id, err := tryLookup(look.ByDepositAccountNoTx, ctx, tx, in.BillRef); !isMiss(err) {
		if err != nil {
			return Decision{}, err
		}
		return Decision{MemberID: id, Via: domain.ViaDepositAccountNo}, nil
	}
	if in.AllowMSISDNFallback {
		if id, err := tryLookup(look.ByMSISDNTx, ctx, tx, in.MSISDN); !isMiss(err) {
			if err != nil {
				return Decision{}, err
			}
			return Decision{MemberID: id, Via: domain.ViaMSISDN}, nil
		}
	}
	return Decision{Via: domain.ViaUnallocated}, nil
}

// tryLookup is a tiny adapter so each branch above reads the same.
// Returns the same (uuid, err) the lookup itself returned, with a
// short-circuit on empty input that mimics ErrNotFound.
func tryLookup(
	fn func(context.Context, pgx.Tx, string) (uuid.UUID, error),
	ctx context.Context, tx pgx.Tx, key string,
) (uuid.UUID, error) {
	if key == "" {
		return uuid.Nil, store.ErrNotFound
	}
	return fn(ctx, tx, key)
}

// isMiss is true when the error is store.ErrNotFound (a clean "no
// match" — try the next branch). Real errors propagate.
func isMiss(err error) bool {
	if err == nil {
		return false
	}
	return err == store.ErrNotFound
}
