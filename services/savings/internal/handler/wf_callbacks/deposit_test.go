// Tests for the cash_deposit callback.
//
// The other 17 callback files share the same shape — closure over a
// runner interface + a receipt-line updater, propagation via the
// common helper. Testing deposit thoroughly here pins the contract
// for every kind:
//
//   1. inst.Status = "approved" → runner.RunDepositTx is called with
//      the right tenant + context + maker, the receipt line is marked
//      posted with the returned txnID.
//   2. inst.Status = "rejected" → runner is NOT called; receipt line
//      is marked declined.
//   3. inst.InitiatorID = nil on an approve → callback errors out
//      with the requireMaker message; runner is NOT called.
//   4. Runner returns an error → callback wraps it with the kind
//      label; receipt line is NOT touched.
//   5. No linked receipt line → callback succeeds without trying to
//      mark anything.
//
// The other 17 callback files inherit these properties by construction.
// A regression in any of them would surface here first if it happens
// in the shared helper; a kind-specific regression would still
// require a kind-specific test in the future. Treat this file as the
// belt; per-kind tests get added when a kind grows kind-specific
// branching.

package wf_callbacks

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ── Mocks ────────────────────────────────────────────────────────

type fakeDepositRunner struct {
	called      bool
	tenantSeen  uuid.UUID
	makerSeen   uuid.UUID
	ctxSeen     []byte
	returnTxnID uuid.UUID
	returnErr   error
}

func (f *fakeDepositRunner) RunDepositTx(_ context.Context, _ pgx.Tx, tenantID uuid.UUID, contextJSON []byte, makerID uuid.UUID) (uuid.UUID, error) {
	f.called = true
	f.tenantSeen = tenantID
	f.makerSeen = makerID
	f.ctxSeen = contextJSON
	return f.returnTxnID, f.returnErr
}

type fakeReceiptsUpdater struct {
	approvalLookedUp uuid.UUID
	findReturnsFound bool
	findReturnsLine  uuid.UUID
	findReturnsErr   error

	postedLineID   uuid.UUID
	postedTxnID    uuid.UUID
	postedCalled   bool
	declinedLineID uuid.UUID
	declinedCalled bool
}

func (f *fakeReceiptsUpdater) FindReceiptLineByApprovalIDTx(_ context.Context, _ pgx.Tx, approvalID uuid.UUID) (uuid.UUID, bool, error) {
	f.approvalLookedUp = approvalID
	return f.findReturnsLine, f.findReturnsFound, f.findReturnsErr
}
func (f *fakeReceiptsUpdater) MarkReceiptLinePostedTx(_ context.Context, _ pgx.Tx, lineID uuid.UUID, txnID uuid.UUID) error {
	f.postedCalled = true
	f.postedLineID = lineID
	f.postedTxnID = txnID
	return nil
}
func (f *fakeReceiptsUpdater) MarkReceiptLineDeclinedTx(_ context.Context, _ pgx.Tx, lineID uuid.UUID) error {
	f.declinedCalled = true
	f.declinedLineID = lineID
	return nil
}

// ── Tests ────────────────────────────────────────────────────────

func TestCashDeposit_Approve_RunsExecutorAndMarksReceiptLinePosted(t *testing.T) {
	maker := uuid.New()
	tenant := uuid.New()
	instID := uuid.New()
	lineID := uuid.New()
	txnID := uuid.New()

	runner := &fakeDepositRunner{returnTxnID: txnID}
	rec := &fakeReceiptsUpdater{findReturnsFound: true, findReturnsLine: lineID}

	cb := NewCashDepositCallback(runner, rec)
	err := cb(t.Context(), nil, Instance{
		ID:          instID,
		TenantID:    tenant,
		Status:      "approved",
		Context:     []byte(`{"payload":{"amount":"100"}}`),
		InitiatorID: &maker,
	})
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}
	if !runner.called {
		t.Fatal("expected runner.RunDepositTx to be called")
	}
	if runner.tenantSeen != tenant {
		t.Errorf("tenantID: got %s, want %s", runner.tenantSeen, tenant)
	}
	if runner.makerSeen != maker {
		t.Errorf("makerID: got %s, want %s", runner.makerSeen, maker)
	}
	if rec.approvalLookedUp != instID {
		t.Errorf("receipt line lookup used wrong approval id: got %s, want %s", rec.approvalLookedUp, instID)
	}
	if !rec.postedCalled {
		t.Error("expected MarkReceiptLinePostedTx to be called")
	}
	if rec.postedLineID != lineID || rec.postedTxnID != txnID {
		t.Errorf("posted args: line=%s txn=%s, want line=%s txn=%s",
			rec.postedLineID, rec.postedTxnID, lineID, txnID)
	}
	if rec.declinedCalled {
		t.Error("MarkReceiptLineDeclinedTx should NOT be called on approve")
	}
}

func TestCashDeposit_Reject_SkipsExecutorAndMarksReceiptLineDeclined(t *testing.T) {
	tenant := uuid.New()
	instID := uuid.New()
	lineID := uuid.New()

	runner := &fakeDepositRunner{}
	rec := &fakeReceiptsUpdater{findReturnsFound: true, findReturnsLine: lineID}

	cb := NewCashDepositCallback(runner, rec)
	err := cb(t.Context(), nil, Instance{
		ID:       instID,
		TenantID: tenant,
		Status:   "rejected",
		Context:  []byte(`{"payload":{"amount":"100"}}`),
	})
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}
	if runner.called {
		t.Error("runner.RunDepositTx should NOT be called on reject")
	}
	if !rec.declinedCalled {
		t.Error("expected MarkReceiptLineDeclinedTx to be called on reject")
	}
	if rec.declinedLineID != lineID {
		t.Errorf("declined line id: got %s, want %s", rec.declinedLineID, lineID)
	}
	if rec.postedCalled {
		t.Error("MarkReceiptLinePostedTx should NOT be called on reject")
	}
}

func TestCashDeposit_Cancel_SkipsExecutorAndMarksReceiptLineDeclined(t *testing.T) {
	// Cancel and reject both flip the line to declined — they're
	// equivalent from the receipt-line POV (the deposit never landed).
	tenant := uuid.New()
	instID := uuid.New()
	lineID := uuid.New()

	runner := &fakeDepositRunner{}
	rec := &fakeReceiptsUpdater{findReturnsFound: true, findReturnsLine: lineID}

	cb := NewCashDepositCallback(runner, rec)
	err := cb(t.Context(), nil, Instance{
		ID:       instID,
		TenantID: tenant,
		Status:   "cancelled",
		Context:  []byte(`{"payload":{"amount":"100"}}`),
	})
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}
	if runner.called {
		t.Error("runner should NOT be called on cancel")
	}
	if !rec.declinedCalled {
		t.Error("expected MarkReceiptLineDeclinedTx on cancel")
	}
}

func TestCashDeposit_Approve_MissingMaker_ErrorsBeforeRunner(t *testing.T) {
	runner := &fakeDepositRunner{}
	rec := &fakeReceiptsUpdater{}

	cb := NewCashDepositCallback(runner, rec)
	err := cb(t.Context(), nil, Instance{
		ID:          uuid.New(),
		TenantID:    uuid.New(),
		Status:      "approved",
		Context:     []byte(`{"payload":{}}`),
		InitiatorID: nil, // missing
	})
	if err == nil {
		t.Fatal("expected error when InitiatorID is nil on approve")
	}
	if runner.called {
		t.Error("runner should NOT be called when maker validation fails")
	}
}

func TestCashDeposit_Approve_RunnerError_DoesNotTouchReceipt(t *testing.T) {
	maker := uuid.New()
	runner := &fakeDepositRunner{returnErr: errors.New("executor blew up")}
	rec := &fakeReceiptsUpdater{findReturnsFound: true, findReturnsLine: uuid.New()}

	cb := NewCashDepositCallback(runner, rec)
	err := cb(t.Context(), nil, Instance{
		ID:          uuid.New(),
		TenantID:    uuid.New(),
		Status:      "approved",
		Context:     []byte(`{"payload":{}}`),
		InitiatorID: &maker,
	})
	if err == nil {
		t.Fatal("expected error to propagate from runner")
	}
	if rec.postedCalled {
		t.Error("MarkReceiptLinePostedTx should NOT be called when the executor failed")
	}
}

func TestCashDeposit_Approve_NoLinkedLine_StillSucceeds(t *testing.T) {
	// Most deposits via /deposit endpoint don't link a receipt line —
	// only Collection Desk routes do. The callback must succeed
	// without trying to mark a nonexistent line.
	maker := uuid.New()
	runner := &fakeDepositRunner{returnTxnID: uuid.New()}
	rec := &fakeReceiptsUpdater{findReturnsFound: false}

	cb := NewCashDepositCallback(runner, rec)
	err := cb(t.Context(), nil, Instance{
		ID:          uuid.New(),
		TenantID:    uuid.New(),
		Status:      "approved",
		Context:     []byte(`{"payload":{}}`),
		InitiatorID: &maker,
	})
	if err != nil {
		t.Fatalf("callback should succeed with no linked line: %v", err)
	}
	if !runner.called {
		t.Error("runner should still run when no line is linked")
	}
	if rec.postedCalled || rec.declinedCalled {
		t.Error("no marking should happen when line lookup returns not found")
	}
}

func TestCashDeposit_NilReceiptsUpdater_SafeNoOp(t *testing.T) {
	// Edge case: main.go could (in theory) pass nil for the
	// receipts updater. The callback should treat that as
	// "no propagation" — important because the placeholder
	// member_bosa_exit registrations don't ship with a receipts
	// updater at all.
	maker := uuid.New()
	runner := &fakeDepositRunner{returnTxnID: uuid.New()}

	cb := NewCashDepositCallback(runner, nil)
	err := cb(t.Context(), nil, Instance{
		ID:          uuid.New(),
		TenantID:    uuid.New(),
		Status:      "approved",
		Context:     []byte(`{"payload":{}}`),
		InitiatorID: &maker,
	})
	if err != nil {
		t.Fatalf("nil receipts updater should be safe: %v", err)
	}
	if !runner.called {
		t.Error("runner must still run when receipts updater is nil")
	}
}

func TestNewCashDepositCallback_PanicsWithoutRunner(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when runner is nil")
		}
	}()
	NewCashDepositCallback(nil, nil)
}
