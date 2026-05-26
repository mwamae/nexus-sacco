// Table-driven tests for the C2B resolver.
//
// Pinned properties (each is a single sub-test row):
//   - member_no wins over every other branch.
//   - cp_number is tried only when member_no missed.
//   - loan_no is tried only after cp_number.
//   - deposit_account_no is tried only after loan_no.
//   - msisdn fallback runs only when allow_msisdn_fallback is true
//     AND none of the bill_ref branches matched.
//   - With allow_msisdn_fallback=false, an MSISDN match is ignored
//     and the verdict is unallocated.
//   - A genuine miss returns unallocated with a nil member id.
//   - A real DB error from any lookup is propagated (caller decides).
//
// The mockLookups is a simple per-branch hit table — each branch
// reports either a UUID (match) or store.ErrNotFound (miss). Branches
// the test doesn't want to be called fail the test if invoked, which
// is what catches "we leaked past the first match" regressions.

package distribution

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
)

// mockLookups is wired with one outcome per branch. nilUUID + nil err
// means "branch not configured — fail the test if called". This lets
// every test row assert which branches the resolver actually
// queried, not just what verdict it returned.
type mockLookups struct {
	t *testing.T

	memberNo   *result // input key (bill_ref) → result
	cpNumber   *result
	loanNo     *result
	depositNo  *result
	msisdn     *result
}

type result struct {
	id  uuid.UUID
	err error
}

func miss() *result               { return &result{err: store.ErrNotFound} }
func hit(id uuid.UUID) *result    { return &result{id: id} }
func boom(err error) *result      { return &result{err: err} }
func (r *result) unwrap() (uuid.UUID, error) {
	if r == nil {
		return uuid.Nil, errors.New("test setup: unexpected branch call")
	}
	return r.id, r.err
}

func (m *mockLookups) ByMemberNoTx(_ context.Context, _ pgx.Tx, _ string) (uuid.UUID, error) {
	if m.memberNo == nil {
		m.t.Fatal("ByMemberNoTx called but no fixture configured")
	}
	return m.memberNo.unwrap()
}
func (m *mockLookups) ByCPNumberTx(_ context.Context, _ pgx.Tx, _ string) (uuid.UUID, error) {
	if m.cpNumber == nil {
		m.t.Fatal("ByCPNumberTx called but no fixture configured")
	}
	return m.cpNumber.unwrap()
}
func (m *mockLookups) ByLoanNoTx(_ context.Context, _ pgx.Tx, _ string) (uuid.UUID, error) {
	if m.loanNo == nil {
		m.t.Fatal("ByLoanNoTx called but no fixture configured")
	}
	return m.loanNo.unwrap()
}
func (m *mockLookups) ByDepositAccountNoTx(_ context.Context, _ pgx.Tx, _ string) (uuid.UUID, error) {
	if m.depositNo == nil {
		m.t.Fatal("ByDepositAccountNoTx called but no fixture configured")
	}
	return m.depositNo.unwrap()
}
func (m *mockLookups) ByMSISDNTx(_ context.Context, _ pgx.Tx, _ string) (uuid.UUID, error) {
	if m.msisdn == nil {
		m.t.Fatal("ByMSISDNTx called but no fixture configured")
	}
	return m.msisdn.unwrap()
}

// memberHit picks a stable uuid so test assertions can compare on
// `wantVia` (instead of the uuid the resolver happened to receive).
var memberHit = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func TestResolve_BranchTable(t *testing.T) {
	cases := []struct {
		name       string
		input      Input
		fixtures   func(*mockLookups)
		wantVia    domain.ResolvedVia
		wantMember uuid.UUID
		wantErr    bool
	}{
		{
			name:  "member_no wins first",
			input: Input{BillRef: "M-2025-00001"},
			fixtures: func(m *mockLookups) {
				m.memberNo = hit(memberHit)
				// other branches MUST NOT be called
			},
			wantVia:    domain.ViaMemberNo,
			wantMember: memberHit,
		},
		{
			name:  "cp_number when member_no misses",
			input: Input{BillRef: "CP-2025-00042"},
			fixtures: func(m *mockLookups) {
				m.memberNo = miss()
				m.cpNumber = hit(memberHit)
			},
			wantVia:    domain.ViaCPNumber,
			wantMember: memberHit,
		},
		{
			name:  "loan_no when member_no + cp_number both miss",
			input: Input{BillRef: "L-2025-99001"},
			fixtures: func(m *mockLookups) {
				m.memberNo = miss()
				m.cpNumber = miss()
				m.loanNo = hit(memberHit)
			},
			wantVia:    domain.ViaLoanNo,
			wantMember: memberHit,
		},
		{
			name:  "deposit_account_no after the three above miss",
			input: Input{BillRef: "DA-1234567"},
			fixtures: func(m *mockLookups) {
				m.memberNo = miss()
				m.cpNumber = miss()
				m.loanNo = miss()
				m.depositNo = hit(memberHit)
			},
			wantVia:    domain.ViaDepositAccountNo,
			wantMember: memberHit,
		},
		{
			name:  "msisdn fallback only when allow_msisdn_fallback=true",
			input: Input{BillRef: "GARBAGE", MSISDN: "254712345678", AllowMSISDNFallback: true},
			fixtures: func(m *mockLookups) {
				m.memberNo = miss()
				m.cpNumber = miss()
				m.loanNo = miss()
				m.depositNo = miss()
				m.msisdn = hit(memberHit)
			},
			wantVia:    domain.ViaMSISDN,
			wantMember: memberHit,
		},
		{
			name:  "msisdn fallback DISABLED → unallocated even if msisdn matches",
			input: Input{BillRef: "GARBAGE", MSISDN: "254712345678", AllowMSISDNFallback: false},
			fixtures: func(m *mockLookups) {
				m.memberNo = miss()
				m.cpNumber = miss()
				m.loanNo = miss()
				m.depositNo = miss()
				// msisdn fixture intentionally left nil — calling it
				// should NEVER happen because the paybill opted out.
			},
			wantVia:    domain.ViaUnallocated,
			wantMember: uuid.Nil,
		},
		{
			name:  "all branches miss → unallocated",
			input: Input{BillRef: "BADREF", MSISDN: "254712345678", AllowMSISDNFallback: true},
			fixtures: func(m *mockLookups) {
				m.memberNo = miss()
				m.cpNumber = miss()
				m.loanNo = miss()
				m.depositNo = miss()
				m.msisdn = miss()
			},
			wantVia:    domain.ViaUnallocated,
			wantMember: uuid.Nil,
		},
		{
			name:  "empty bill_ref skips all bill_ref branches but msisdn still tried",
			input: Input{BillRef: "", MSISDN: "0712345678", AllowMSISDNFallback: true},
			fixtures: func(m *mockLookups) {
				// All bill_ref branches short-circuit on empty key
				// inside the resolver — they MUST NOT be called.
				m.msisdn = hit(memberHit)
			},
			wantVia:    domain.ViaMSISDN,
			wantMember: memberHit,
		},
		{
			name:  "DB error from any branch propagates",
			input: Input{BillRef: "M-2025-00001"},
			fixtures: func(m *mockLookups) {
				m.memberNo = boom(errors.New("conn closed"))
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockLookups{t: t}
			tc.fixtures(m)
			got, err := Resolve(context.Background(), nil, m, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Via != tc.wantVia {
				t.Errorf("via: want %q, got %q", tc.wantVia, got.Via)
			}
			if got.MemberID != tc.wantMember {
				t.Errorf("member: want %s, got %s", tc.wantMember, got.MemberID)
			}
		})
	}
}

func TestMsisdnDigits(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0712345678", "712345678"},
		{"+254712345678", "712345678"},
		{"254712345678", "712345678"},
		{"0712 345 678", "712345678"},
		{"712345678", "712345678"},
		{"", ""},
		{"123", ""},        // too short
		{"007", ""},        // too short after country-code strip
		{"abc", ""},        // no digits
	}
	for _, c := range cases {
		if got := store.MsisdnDigits(c.in); got != c.want {
			t.Errorf("MsisdnDigits(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
