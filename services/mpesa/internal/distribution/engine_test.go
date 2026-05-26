// Table-driven engine tests.
//
// The engine is a pure function over a Balances port, so every test
// case wires a mock that returns the exact balances + accounts the
// scenario needs. We verify the Plan: the ordered list of Splits + the
// leftover. No DB interaction here — the integration tests in
// distributor_test.go exercise the real read path.
//
// Cases (14 total):
//   1. Acceptance — 200 fees, 500 interest, 2000 principal, 0 BOSA target,
//      4000 inbound → 200 fees / 500 interest / 2000 principal / 1300 BOSA
//   2. FOSA-absent fallback — no FOSA account → 100% leftover to BOSA
//   3. Loan-only — no deposit accounts → leftover stays as Leftover
//   4. Direct loan_no — full amount waterfalls inside the named loan
//   5. Direct loan_no with overpayment → extra lands on principal
//   6. Direct deposit_account_no → entire amount to that account
//   7. Direct deposit_account_no FOSA-classified → FOSA leg, not BOSA
//   8. resolved_via=unallocated → Plan with empty splits, Leftover=amount
//   9. Loan-only legs with no live loan → all legs zero, leftover at end
//  10. Empty waterfall → DefaultWaterfall is used
//  11. Zero-balance loan + BOSA-only fallback → all to BOSA
//  12. Penalty zero, interest non-zero → penalty leg skipped silently
//  13. Whole amount absorbed by fees alone → no other splits
//  14. Non-positive amount → ErrInputInvalid

package distribution

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/mpesa/internal/domain"
	"github.com/nexussacco/mpesa/internal/store"
)

// mockBalances is a single fixture for each test row — populated
// once, queried by the engine, never modified during the test.
type mockBalances struct {
	loan     *store.LoanComponents
	loanByNo map[string]*store.LoanComponents
	accounts []store.DepositAccount
	depByNo  map[string]*store.DepositAccount
	fees     decimal.Decimal
	loanErr  error
}

func (m *mockBalances) PrimaryActiveLoanTx(_ context.Context, _ pgx.Tx, _ uuid.UUID) (*store.LoanComponents, error) {
	if m.loanErr != nil {
		return nil, m.loanErr
	}
	if m.loan == nil {
		return nil, store.ErrNotFound
	}
	return m.loan, nil
}
func (m *mockBalances) LoanByNoTx(_ context.Context, _ pgx.Tx, no string) (*store.LoanComponents, error) {
	if l, ok := m.loanByNo[no]; ok {
		return l, nil
	}
	return nil, store.ErrNotFound
}
func (m *mockBalances) DepositAccountsTx(_ context.Context, _ pgx.Tx, _ uuid.UUID) ([]store.DepositAccount, error) {
	return m.accounts, nil
}
func (m *mockBalances) DepositAccountByNoTx(_ context.Context, _ pgx.Tx, no string) (*store.DepositAccount, error) {
	if d, ok := m.depByNo[no]; ok {
		return d, nil
	}
	return nil, store.ErrNotFound
}
func (m *mockBalances) FeesDueTx(_ context.Context, _ pgx.Tx, _ uuid.UUID) (decimal.Decimal, error) {
	return m.fees, nil
}

// dec is a small helper so test rows can write KES amounts as
// strings without dragging decimal.NewFromString into every line.
func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic("test fixture: bad decimal " + s)
	}
	return d
}

func loan(no string, penalty, interest, principal, fees string) *store.LoanComponents {
	id := uuid.New()
	return &store.LoanComponents{
		LoanID: id, LoanNo: no,
		Penalty:   dec(penalty),
		Interest:  dec(interest),
		Principal: dec(principal),
		Fees:      dec(fees),
	}
}

func bosa(no string) store.DepositAccount {
	return store.DepositAccount{ID: uuid.New(), AccountNo: no, ProductCode: "BOSA-STD"}
}

func fosa(no string) store.DepositAccount {
	return store.DepositAccount{ID: uuid.New(), AccountNo: no, ProductCode: "FOSA-STD"}
}

// makeEvent builds an InboundEvent fixture with the supplied
// resolution. `via=""` means the resolver landed on unallocated.
func makeEvent(amount, billRef string, memberID uuid.UUID, via domain.ResolvedVia) *domain.InboundEvent {
	e := &domain.InboundEvent{
		ID:      uuid.New(),
		Amount:  amount,
		BillRef: billRef,
	}
	if memberID != uuid.Nil {
		mid := memberID
		e.ResolvedMemberID = &mid
	}
	if via != "" {
		v := via
		e.ResolvedVia = &v
	}
	return e
}

func TestEngine_TableCases(t *testing.T) {
	memberID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	cases := []struct {
		name      string
		event     *domain.InboundEvent
		balances  *mockBalances
		waterfall []LegTarget
		wantSplits []Split
		wantLeftover string
		wantErr   error
	}{
		{
			name: "Acceptance — 200 fees / 500 interest / 2000 principal / 1300 BOSA",
			event: makeEvent("4000.00", "M-2025-00001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				loan:     loan("L-001", "0", "500", "2000", "0"),
				accounts: []store.DepositAccount{bosa("DA-101")},
				fees:     dec("200"),
			},
			// Note: spec acceptance says "200 fees ... 1300 BOSA" with no
			// FOSA. Our DefaultWaterfall has BOSA before FOSA; without a
			// FOSA account the BOSA leg picks up the leftover.
			wantSplits: []Split{
				{Leg: LegFeesDue, Amount: dec("200")},
				{Leg: LegLoanInterestDue, Amount: dec("500")},
				{Leg: LegLoanPrincipalDue, Amount: dec("2000")},
				{Leg: LegBOSATopUp, Amount: dec("1300")},
			},
			wantLeftover: "0",
		},
		{
			name: "FOSA-absent fallback — 100% leftover to BOSA",
			event: makeEvent("1000.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				accounts: []store.DepositAccount{bosa("DA-101")},
			},
			wantSplits: []Split{
				{Leg: LegBOSATopUp, Amount: dec("1000")},
			},
			wantLeftover: "0",
		},
		{
			name: "Loan-only — no deposit accounts → leftover stays as Leftover",
			event: makeEvent("3000.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				loan: loan("L-001", "0", "0", "1000", "0"),
			},
			wantSplits: []Split{
				{Leg: LegLoanPrincipalDue, Amount: dec("1000")},
			},
			wantLeftover: "2000",
		},
		{
			name: "Direct loan_no — waterfall inside the named loan",
			event: makeEvent("1500.00", "L-077", memberID, domain.ViaLoanNo),
			balances: &mockBalances{
				loanByNo: map[string]*store.LoanComponents{
					"L-077": loan("L-077", "100", "200", "1200", "0"),
				},
			},
			wantSplits: []Split{
				{Leg: LegLoanPenaltyDue, Amount: dec("100")},
				{Leg: LegLoanInterestDue, Amount: dec("200")},
				{Leg: LegLoanPrincipalDue, Amount: dec("1200")},
			},
			wantLeftover: "0",
		},
		{
			name: "Direct loan_no with overpayment → extra to principal",
			event: makeEvent("5000.00", "L-077", memberID, domain.ViaLoanNo),
			balances: &mockBalances{
				loanByNo: map[string]*store.LoanComponents{
					"L-077": loan("L-077", "0", "0", "1000", "0"),
				},
			},
			wantSplits: []Split{
				{Leg: LegLoanPrincipalDue, Amount: dec("1000")},
				// 4000 overpayment also flows to principal (the
				// applier in 3.5 handles this as a prepayment).
				{Leg: LegLoanPrincipalDue, Amount: dec("4000")},
			},
			wantLeftover: "0",
		},
		{
			name: "Direct deposit_account_no → entire amount to that account",
			event: makeEvent("700.00", "DA-501", memberID, domain.ViaDepositAccountNo),
			balances: &mockBalances{
				depByNo: map[string]*store.DepositAccount{
					"DA-501": {ID: uuid.New(), AccountNo: "DA-501", ProductCode: "BOSA-STD"},
				},
			},
			wantSplits: []Split{
				{Leg: LegBOSATopUp, Amount: dec("700")},
			},
			wantLeftover: "0",
		},
		{
			name: "Direct deposit_account_no — FOSA leg, not BOSA",
			event: makeEvent("700.00", "DA-501", memberID, domain.ViaDepositAccountNo),
			balances: &mockBalances{
				depByNo: map[string]*store.DepositAccount{
					"DA-501": {ID: uuid.New(), AccountNo: "DA-501", ProductCode: "FOSA-STD"},
				},
			},
			wantSplits: []Split{
				{Leg: LegFOSATopUp, Amount: dec("700")},
			},
			wantLeftover: "0",
		},
		{
			name: "Unallocated → empty splits, Leftover=amount",
			event: makeEvent("400.00", "??garbage", uuid.Nil, domain.ViaUnallocated),
			balances: &mockBalances{},
			wantSplits: []Split{},
			wantLeftover: "400",
		},
		{
			name: "Loan legs without a live loan → all zero, leftover at end",
			event: makeEvent("100.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{},
			wantSplits: []Split{},
			wantLeftover: "100",
		},
		{
			name: "Empty waterfall → DefaultWaterfall is used",
			event: makeEvent("500.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				accounts: []store.DepositAccount{bosa("DA-101")},
			},
			waterfall: []LegTarget{}, // explicitly empty
			wantSplits: []Split{
				{Leg: LegBOSATopUp, Amount: dec("500")},
			},
			wantLeftover: "0",
		},
		{
			name: "Zero-balance loan + BOSA-only fallback → all to BOSA",
			event: makeEvent("1000.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				loan:     loan("L-001", "0", "0", "0", "0"),
				accounts: []store.DepositAccount{bosa("DA-101")},
			},
			wantSplits: []Split{
				{Leg: LegBOSATopUp, Amount: dec("1000")},
			},
			wantLeftover: "0",
		},
		{
			name: "Penalty zero, interest non-zero → penalty leg silently skipped",
			event: makeEvent("100.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				loan: loan("L-001", "0", "50", "0", "0"),
				accounts: []store.DepositAccount{bosa("DA-101")},
			},
			wantSplits: []Split{
				{Leg: LegLoanInterestDue, Amount: dec("50")},
				{Leg: LegBOSATopUp, Amount: dec("50")},
			},
			wantLeftover: "0",
		},
		{
			name: "Whole amount absorbed by fees alone",
			event: makeEvent("200.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{
				fees: dec("300"),
			},
			wantSplits: []Split{
				{Leg: LegFeesDue, Amount: dec("200")},
			},
			wantLeftover: "0",
		},
		{
			name: "Non-positive amount → ErrInputInvalid",
			event: makeEvent("0.00", "M-001", memberID, domain.ViaMemberNo),
			balances: &mockBalances{},
			wantErr: ErrInputInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Run(context.Background(), nil, tc.balances, tc.event, tc.waterfall)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want err %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(plan.Splits) != len(tc.wantSplits) {
				t.Fatalf("splits len: want %d, got %d (%+v)", len(tc.wantSplits), len(plan.Splits), plan.Splits)
			}
			for i, want := range tc.wantSplits {
				got := plan.Splits[i]
				if got.Leg != want.Leg || !got.Amount.Equal(want.Amount) {
					t.Errorf("split #%d: want leg=%s amount=%s, got leg=%s amount=%s",
						i, want.Leg, want.Amount, got.Leg, got.Amount)
				}
			}
			if !plan.Leftover.Equal(dec(tc.wantLeftover)) {
				t.Errorf("leftover: want %s, got %s", tc.wantLeftover, plan.Leftover)
			}
		})
	}
}
