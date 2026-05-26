// Read-only lookups the resolver uses to match a C2B payment's
// bill_ref / msisdn against the SACCO's own data.
//
// All five lookups follow the same pattern: return the
// counterparty.id when something matches, ErrNotFound when nothing
// does. Cross-service reads against shared DB — see the architecture
// memory's "shared-DB / no cross-service FKs" pattern.
//
// Phone numbers in the contact jsonb may have been written in any of
// several formats ("0712345678", "+254712345678", "254712345678").
// The MSISDN branch normalises both sides via msisdnDigits() before
// comparing so the match is robust to that drift.

package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ResolverLookups struct {
	pool *pgxpool.Pool
}

func NewResolverLookups(pool *pgxpool.Pool) *ResolverLookups {
	return &ResolverLookups{pool: pool}
}

// ByMemberNoTx — exact match against members.member_no, returns the
// counterparty.id of the matched member.
func (r *ResolverLookups) ByMemberNoTx(ctx context.Context, tx pgx.Tx, memberNo string) (uuid.UUID, error) {
	if strings.TrimSpace(memberNo) == "" {
		return uuid.Nil, ErrNotFound
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM members WHERE member_no = $1`, memberNo,
	).Scan(&id)
	return mustUUID(id, err)
}

// ByCPNumberTx — exact match against counterparties.cp_number.
func (r *ResolverLookups) ByCPNumberTx(ctx context.Context, tx pgx.Tx, cpNo string) (uuid.UUID, error) {
	if strings.TrimSpace(cpNo) == "" {
		return uuid.Nil, ErrNotFound
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM counterparties WHERE cp_number = $1`, cpNo,
	).Scan(&id)
	return mustUUID(id, err)
}

// ByLoanNoTx — exact match against loans.loan_no. Returns the
// counterparty_id of the loan's owner.
func (r *ResolverLookups) ByLoanNoTx(ctx context.Context, tx pgx.Tx, loanNo string) (uuid.UUID, error) {
	if strings.TrimSpace(loanNo) == "" {
		return uuid.Nil, ErrNotFound
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM loans WHERE loan_no = $1`, loanNo,
	).Scan(&id)
	return mustUUID(id, err)
}

// ByDepositAccountNoTx — exact match against deposit_accounts.account_no.
// Returns the counterparty_id via deposit_account_owners (the
// many-to-one join — primary holder, in practice).
func (r *ResolverLookups) ByDepositAccountNoTx(ctx context.Context, tx pgx.Tx, accountNo string) (uuid.UUID, error) {
	if strings.TrimSpace(accountNo) == "" {
		return uuid.Nil, ErrNotFound
	}
	// deposit_accounts.counterparty_id is the contractual primary
	// holder. If your schema names this differently in a future
	// migration, this query is the one place to update.
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT counterparty_id FROM deposit_accounts
		 WHERE account_no = $1
		 LIMIT 1
	`, accountNo).Scan(&id)
	return mustUUID(id, err)
}

// ByMSISDNTx — phone-number fallback. Normalises both sides to
// E.164-stripped digits before comparing. Disabled per-paybill via
// mpesa_paybills.allow_msisdn_fallback; this method runs only when
// the resolver decides to attempt it.
func (r *ResolverLookups) ByMSISDNTx(ctx context.Context, tx pgx.Tx, msisdn string) (uuid.UUID, error) {
	normalized := MsisdnDigits(msisdn)
	if normalized == "" {
		return uuid.Nil, ErrNotFound
	}
	// Compare against the LAST 9 digits of the stored phone (which
	// covers the trailing 7XXXXXXXX whether the row stored
	// "0712345678", "+254712345678", or "254712345678"). The
	// normalized inbound msisdn is similarly trimmed below.
	tail := normalized
	if len(tail) > 9 {
		tail = tail[len(tail)-9:]
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM counterparties
		 WHERE right(regexp_replace(COALESCE(contact->>'phone',''), '[^0-9]', '', 'g'), 9) = $1
		 LIMIT 1
	`, tail).Scan(&id)
	return mustUUID(id, err)
}

// MsisdnDigits keeps only digits — strips +, leading zeros, spaces,
// and the 254 country code. Returns "" when the input has fewer than
// 9 useful digits, which the resolver treats as "no MSISDN to match".
func MsisdnDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	// Drop the Kenyan country code if present; both representations
	// ("0712…", "254712…") collapse to "712…" + 8 more digits.
	digits = strings.TrimPrefix(digits, "254")
	digits = strings.TrimPrefix(digits, "0")
	if len(digits) < 9 {
		return ""
	}
	return digits
}

func mustUUID(id uuid.UUID, err error) (uuid.UUID, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
