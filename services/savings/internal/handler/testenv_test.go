// Acceptance-test fixture for the Collection Desk HTTP surface.
//
// The existing store-level integration tests (collections_bridge,
// receipt_propagation, deposit_reverse) all run inside a rolled-back
// transaction. That pattern can't be used here because the real
// CreateReceipt handler owns its own WithTenantTx — once it commits,
// the rows are live. So this fixture takes the other route:
//   • mount a stripped-down chi router around the real handlers
//   • bypass JWT + hostname-based tenant resolution by injecting
//     tenant + admin claims into the request context directly
//   • disable the accounting Posting client (every handler's GL post
//     already short-circuits gracefully on ErrPostingDisabled)
//   • leave the notifier nil — RenderPDF/Send paths are not exercised
//   • cleanup is keyed on a per-run marker tag stamped into every
//     account_no / loan_no / application_no the seeder generates +
//     the random userID used for maker attribution. No row outside
//     the run's own marker scope is touched.
//
// The fixture is `_test.go`-suffixed so it never bloats the prod
// binary. Skipped when DATABASE_URL is unset — same convention as
// the other integration tests.

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexussacco/savings/internal/auth"
	"github.com/nexussacco/savings/internal/db"
	"github.com/nexussacco/savings/internal/middleware"
	"github.com/nexussacco/savings/internal/posting"
	"github.com/nexussacco/savings/internal/store"
)

type testEnv struct {
	t        *testing.T
	Pool     *db.Pool
	Server   *httptest.Server
	TenantID uuid.UUID
	UserID   uuid.UUID

	// MarkerSuffix is stamped into every synthetic row's natural-key
	// column (account_no, loan_no, application_no, channel_ref) so
	// cleanup can target only this run's rows without disturbing the
	// surrounding seed data.
	MarkerSuffix string
}

// newTestEnv boots a real-handler-backed httptest server pointed at the
// tujenge tenant in the dev database. Returns nil + skip if env is
// missing.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping HTTP acceptance test")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.New(ctx, dsn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	var tenantID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE slug='tujenge' LIMIT 1`).Scan(&tenantID); err != nil {
		pool.Close()
		t.Skipf("no tujenge tenant: %v", err)
		return nil
	}

	// ─── stores ────────────────────────────────────────────────────
	counterparties := store.NewCounterpartyStore(pool.Pool)
	tenants := store.NewTenantStore(pool.Pool)
	productStore := store.NewDepositProductStore(pool.Pool)
	depositStore := store.NewDepositStore(pool.Pool)
	loanStore := store.NewLoanStore(pool.Pool)
	loanReportsStore := store.NewLoanReportsStore(pool.Pool)
	shareStore := store.NewShareStore(pool.Pool)
	approvalsStore := store.NewApprovalsStore(pool.Pool)
	receiptStore := store.NewReceiptStore(pool.Pool)
	virtualTillStore := store.NewVirtualTillStore(pool.Pool)
	feeCatalogStore := store.NewFeeCatalogStore(pool.Pool)

	// Disabled posting — every handler's PostXxx-side helper checks
	// for ErrPostingDisabled and continues. This lets us exercise the
	// full receipt flow without standing up the accounting service.
	postingClient := &posting.Client{Disabled: true}

	// ─── handlers ──────────────────────────────────────────────────
	depositH := &DepositHandler{
		DB: pool, Tenants: tenants, Counterparties: counterparties,
		Products: productStore, Deposits: depositStore,
		Approvals: approvalsStore, Posting: postingClient,
	}
	shareH := &ShareHandler{
		DB: pool, Tenants: tenants, Counterparties: counterparties,
		Shares: shareStore, Approvals: approvalsStore, Posting: postingClient,
	}
	loanRepayH := &LoanRepaymentHandler{
		DB: pool, Tenants: tenants, Counterparties: counterparties,
		Deposits: depositStore, Loans: loanStore,
		Approvals: approvalsStore, Posting: postingClient,
	}
	approvalsH := &PendingApprovalsHandler{
		DB: pool, Approvals: approvalsStore,
		Deposit: depositH, Share: shareH, LoanRepay: loanRepayH,
		Receipts: receiptStore,
	}
	collectionH := &CollectionDeskHandler{
		DB: pool, Receipts: receiptStore, VirtualTills: virtualTillStore,
		Approvals: approvalsStore, Loans: loanStore, LoanReports: loanReportsStore,
		Shares: shareStore, Tenants: tenants, Counterparties: counterparties,
		Fees: feeCatalogStore, Posting: postingClient,
		Deposit: depositH, LoanRepay: loanRepayH,
	}

	// ─── server ────────────────────────────────────────────────────
	userID := uuid.New()
	r := chi.NewRouter()
	// Auth/tenant shim: inject what RequireTenant + RequirePermission
	// would have set after JWT + hostname resolution. IsPlatformAdmin
	// short-circuits every permission check so we don't have to wire
	// per-route allowlists.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
			c := rq.Context()
			c = middleware.WithTenant(c, tenantID, "tujenge")
			c = middleware.WithClaims(c, &auth.AccessClaims{
				TenantID:        tenantID.String(),
				TenantSlug:      "tujenge",
				UserID:          userID.String(),
				IsPlatformAdmin: true,
			})
			next.ServeHTTP(w, rq.WithContext(c))
		})
	})
	r.Route("/v1", func(r chi.Router) {
		r.Post("/receipts", collectionH.CreateReceipt)
		r.Get("/receipts/{id}", collectionH.GetReceipt)
		r.Post("/receipts/{id}/lines/{line_id}/void", collectionH.VoidLine)
		r.Post("/pending-approvals/{approval_id}/approve", approvalsH.Approve)
	})
	// Internal service-to-service surface (the Unified Inbox
	// workflow-callback target). Mounted alongside /v1 so PR #3
	// tests can drive it directly.
	r.Route("/internal/v1", func(r chi.Router) {
		r.Post("/pending-approvals/{approval_id}/resolve", approvalsH.ResolveFromWorkflow)
	})

	srv := httptest.NewServer(r)

	return &testEnv{
		t:            t,
		Pool:         pool,
		Server:       srv,
		TenantID:     tenantID,
		UserID:       userID,
		MarkerSuffix: fmt.Sprintf("ACC%d", time.Now().UnixNano()),
	}
}

// close shuts down the test server and best-effort deletes every row
// stamped with this run's MarkerSuffix or made by this run's
// synthetic UserID. Failures are logged but never fail the test —
// leaving stragglers is preferable to a teardown panic.
func (e *testEnv) close() {
	if e.Server != nil {
		e.Server.Close()
	}
	if e.Pool != nil {
		e.cleanup()
		e.Pool.Close()
	}
}

// cleanup is conservative — keyed on this run's MarkerSuffix
// (account_no / loan_no / application_no / channel_ref LIKE
// '%-<marker>%') and on the per-run UserID (maker attribution).
// Order matters: children before parents.
func (e *testEnv) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		e.t.Logf("cleanup begin: %v", err)
		return
	}
	defer func() { _ = tx.Commit(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1::text, true)`, e.TenantID.String()); err != nil {
		e.t.Logf("cleanup set tenant: %v", err)
		return
	}
	exec := func(label, sql string, args ...any) {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			e.t.Logf("cleanup %s: %v", label, err)
		}
	}
	mk := "%" + e.MarkerSuffix + "%"

	// Receipt cascade — keyed on cashier_user_id (per-run synthetic
	// UUID, never collides with real users). receipt_lines has
	// ON DELETE CASCADE from receipts.
	exec("receipts", `DELETE FROM receipts WHERE cashier_user_id = $1`, e.UserID)

	// Pending approvals queued by this run's synthetic user.
	exec("pending_approvals", `DELETE FROM pending_approvals WHERE maker_user_id = $1`, e.UserID)

	// Deposit-side: transactions linked to our marked accounts, then
	// the accounts themselves.
	exec("deposit_transactions", `DELETE FROM deposit_transactions WHERE account_id IN (SELECT id FROM deposit_accounts WHERE account_no LIKE $1)`, mk)
	exec("deposit_accounts", `DELETE FROM deposit_accounts WHERE account_no LIKE $1`, mk)

	// Share-side: any share_transactions our user posted. We don't
	// touch share_accounts (they belong to the real CP and pre-exist).
	exec("share_transactions", `DELETE FROM share_transactions WHERE initiated_by = $1`, e.UserID)

	// Loan-side cascade — txns, schedule, collections-case, loan, app.
	exec("loan_transactions", `DELETE FROM loan_transactions WHERE loan_id IN (SELECT id FROM loans WHERE loan_no LIKE $1)`, mk)
	exec("loan_repayment_schedule", `DELETE FROM loan_repayment_schedule WHERE loan_id IN (SELECT id FROM loans WHERE loan_no LIKE $1)`, mk)
	exec("loan_collection_cases", `DELETE FROM loan_collection_cases WHERE loan_id IN (SELECT id FROM loans WHERE loan_no LIKE $1)`, mk)
	exec("loans", `DELETE FROM loans WHERE loan_no LIKE $1`, mk)
	exec("loan_applications", `DELETE FROM loan_applications WHERE application_no LIKE $1`, mk)
}
