// Regression test for the silent-NULL counterparty_id bug that
// surfaced during Phase D sub-PR 1 cross-checks.
//
// The bug: ApplicationHandler.Approve used to call ActivateApplicationTx
// (which inserts the member row AND the default share + deposit
// accounts) and only THEN created the counterparty + stamped
// members.counterparty_id. The BEFORE INSERT trigger on
// share_accounts/deposit_accounts looks up members.counterparty_id at
// insert time — at that point it was still NULL, so the per-row
// counterparty_id silently dropped to NULL. Once sub-PR 1's reads
// switch to filtering by counterparty_id, those rows become invisible
// to every member-scoped query, which presents as "no accounts" in
// the UI — a screenshot-clean, audit-disastrous failure mode.
//
// The fix splits the activate into three handler-driven phases:
//   (1) MaterialiseIndividualMemberTx — member row only
//   (2) createCounterpartyFromApplicationTx — CP row + bridge stamp
//   (3) OpenDefaultIndividualAccountsTx — share + optional deposit
// This test exercises the three-phase sequence end-to-end and asserts
// that members.counterparty_id, share_accounts.counterparty_id, and
// deposit_accounts.counterparty_id are all non-NULL after step (3).

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/store"
)

func TestApproveFlowStampsBridgeOnAllChildRows(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer pool.Close()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Pick a tenant that has at least one member (so sequences are
	// initialised + RLS context is meaningful). The same fixture
	// strategy as counterparty_mirror_test.go.
	var tenantID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT t.id FROM tenants t
		 WHERE EXISTS (SELECT 1 FROM members m WHERE m.tenant_id = t.id)
		 LIMIT 1
	`).Scan(&tenantID); err != nil {
		t.Skipf("no tenant with members: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	apps := store.NewApplicationStore(pool)
	members := store.NewMemberStore(pool)
	cps := store.NewCounterpartyStore(pool)

	// Fabricate a freshly-inserted application row in
	// reviewed_pending_approval status so the materialise step has
	// something to read. The applicant_payload is the minimum the
	// MaterialiseIndividualMember path needs (gender + id_doc_*).
	uniq := time.Now().UnixNano()
	appID := uuid.New()
	payload := json.RawMessage(fmt.Sprintf(`{
		"id_doc_kind":"national_id",
		"id_doc_number":"ID-INV-%d",
		"gender":"male",
		"kra_pin":"A%dB",
		"county":"Nairobi",
		"sub_county":"Westlands",
		"physical_address":"Test address",
		"occupation":"engineer",
		"employer":"Test Co"
	}`, uniq, uniq))
	submitterID := uuid.New()
	_, err = tx.Exec(ctx, `
		INSERT INTO membership_applications (
		  id, tenant_id, application_no, kind, status,
		  applicant_name, primary_phone, primary_email,
		  applicant_payload, fee_required, fee_amount_paid,
		  submitted_by, created_at, updated_at
		) VALUES (
		  $1, $2, $3, 'individual'::membership_application_kind,
		  'reviewed_pending_approval'::membership_application_status,
		  $4, $5, $6, $7::jsonb, false, 0,
		  $8, now(), now()
		)
	`,
		appID, tenantID, fmt.Sprintf("APP-INV-%d", uniq),
		fmt.Sprintf("Bridge Invariant Test %d", uniq),
		"+254700000000", fmt.Sprintf("inv%d@example.test", uniq),
		string(payload), submitterID,
	)
	if err != nil {
		t.Fatalf("insert application: %v", err)
	}

	// Reload through the store so the in-memory app is fully populated.
	app, err := apps.GetTx(ctx, tx, appID)
	if err != nil {
		t.Fatalf("get application: %v", err)
	}

	// Pick a default deposit product if the tenant has one, so the
	// deposit branch of phase (3) exercises too.
	var depositProductID *uuid.UUID
	var dpid uuid.UUID
	switch err := tx.QueryRow(ctx,
		`SELECT id FROM deposit_products WHERE tenant_id = $1 ORDER BY created_at LIMIT 1`,
		tenantID,
	).Scan(&dpid); err {
	case nil:
		depositProductID = &dpid
	case pgx.ErrNoRows:
		// fine — share-only fixture path
	default:
		t.Fatalf("lookup deposit product: %v", err)
	}

	actorID := uuid.New()
	parValue := decimal.NewFromInt(100)

	// ─── Phase 1: insert member row only ───
	memberID, memberNo, err := apps.MaterialiseIndividualMemberTx(ctx, tx, app, members, actorID)
	if err != nil {
		t.Fatalf("MaterialiseIndividualMemberTx: %v", err)
	}
	if memberID == uuid.Nil || memberNo == "" {
		t.Fatalf("materialise returned nil/empty: id=%s no=%q", memberID, memberNo)
	}

	// Sanity: at this point the bridge is NOT yet set — proving the
	// 3-phase ordering is meaningful (if the test ever fails here it
	// means a future refactor folded the CP stamp into phase 1, which
	// is a fine outcome but invalidates this test's premise).
	var preCP *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT counterparty_id FROM members WHERE id = $1`, memberID).Scan(&preCP); err != nil {
		t.Fatalf("read pre-CP bridge: %v", err)
	}
	if preCP != nil {
		t.Fatalf("expected members.counterparty_id NULL after phase 1, got %s — re-check the 3-phase ordering invariant", *preCP)
	}

	// ─── Phase 2: CP creation + bridge stamp ───
	h := &ApplicationHandler{Counterparties: cps}
	cpID, err := h.createCounterpartyFromApplicationTx(ctx, tx, tenantID, memberID, app, actorID)
	if err != nil {
		t.Fatalf("createCounterpartyFromApplicationTx: %v", err)
	}
	if cpID == uuid.Nil {
		t.Fatalf("createCounterpartyFromApplicationTx returned nil id")
	}

	// ─── Phase 3: open default share + optional deposit ───
	result, err := apps.OpenDefaultIndividualAccountsTx(
		ctx, tx, app, memberID, memberNo, depositProductID, parValue, actorID,
	)
	if err != nil {
		t.Fatalf("OpenDefaultIndividualAccountsTx: %v", err)
	}

	// ─── Invariant assertions ───
	// (a) members.counterparty_id is set to the CP we created.
	var memberCP uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM members WHERE id = $1`, memberID,
	).Scan(&memberCP); err != nil {
		t.Fatalf("read members.counterparty_id: %v", err)
	}
	if memberCP != cpID {
		t.Errorf("members.counterparty_id: want %s, got %s", cpID, memberCP)
	}

	// (b) share_accounts.counterparty_id is non-NULL and matches.
	//     This is the load-bearing assertion: under the old ordering
	//     this column was silently NULL.
	var shareCP *uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM share_accounts WHERE id = $1`, result.ShareAccountID,
	).Scan(&shareCP); err != nil {
		t.Fatalf("read share_accounts.counterparty_id: %v", err)
	}
	if shareCP == nil {
		t.Fatalf("share_accounts.counterparty_id is NULL — the 3-phase ordering invariant has regressed")
	}
	if *shareCP != cpID {
		t.Errorf("share_accounts.counterparty_id: want %s, got %s", cpID, *shareCP)
	}

	// (c) deposit_accounts.counterparty_id is non-NULL and matches
	//     (only when a deposit product was available for the tenant).
	if result.DepositAccountID != nil {
		var depCP *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT counterparty_id FROM deposit_accounts WHERE id = $1`, *result.DepositAccountID,
		).Scan(&depCP); err != nil {
			t.Fatalf("read deposit_accounts.counterparty_id: %v", err)
		}
		if depCP == nil {
			t.Fatalf("deposit_accounts.counterparty_id is NULL — the 3-phase ordering invariant has regressed")
		}
		if *depCP != cpID {
			t.Errorf("deposit_accounts.counterparty_id: want %s, got %s", cpID, *depCP)
		}
	}
}

// Silence unused-import lint when the test body trims.
var _ = domain.ApplicationKindIndividual
