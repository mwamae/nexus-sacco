// Integration tests for the canonical counterparty roll-call source.
//
// Covers:
//   1. counterparty_status_counts SQL function: kind / status / q
//      filter axes, totals and per-bucket breakdowns.
//   2. CountsV2 HTTP handler — happy paths against the same fixture
//      set, asserting the wire shape matches the SQL function.
//   3. Cross-tenant RLS regression: fixtures in tenant A must not
//      bleed into tenant B's counts.
//
// Tests run against the live DATABASE_URL (skipped when unset, same
// convention as the rest of services/member). To avoid colliding with
// existing seed data each test stamps its fixtures with a unique
// `display_name` prefix and scopes every count query to that prefix
// via the p_q filter — that way the assertions are exact regardless
// of whatever else the dev tenant happens to contain.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nexussacco/member/internal/db"
	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/store"
)

// fixture lays out the row plan for one counts-test invocation: the
// kind, the status, and how many of that combination to seed. Kept as
// data so each test reads as a small table of "we expect to see X of
// these" rather than 20 lines of inserts.
type fixture struct {
	kind   string // 'individual' | one of the institutional kinds
	status string // counterparty status enum value
	count  int
}

const fixtureMix = "CSC-TEST" // search-friendly prefix; used for p_q scoping

func TestCounterpartyStatusCounts_Function_Filters(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	ctx := context.Background()
	dbPool := &db.Pool{Pool: pool}

	// 5 individuals (3 active, 1 pending, 1 exited)
	// + 3 institutions (2 chama active, 1 ngo rejected).
	mix := []fixture{
		{"individual", "active", 3},
		{"individual", "pending", 1},
		{"individual", "exited", 1},
		{"chama", "active", 2},
		{"ngo", "rejected", 1},
	}
	prefix := seedCountsFixture(ctx, t, dbPool, tenantID, mix)
	statuses := store.NewStatusChangeStore(pool)

	// All-kinds, all-statuses, scoped to our prefix.
	t.Run("all kinds, no status filter", func(t *testing.T) {
		c := callCounts(ctx, t, dbPool, statuses, tenantID, store.CPKindAll, nil, prefix)
		// 3 active individuals + 2 active chamas = 5 active. 1 pending.
		// 1 exited. 1 rejected. total_directory = 8.
		assertEq(t, "total_directory", c.TotalDirectory, 8)
		assertEq(t, "individuals", c.Individuals, 5)
		assertEq(t, "institutions", c.Institutions, 3)
		assertEq(t, "active", c.Active, 5)
		assertEq(t, "pending", c.Pending, 1)
		assertEq(t, "exited", c.Exited, 1)
		assertEq(t, "rejected", c.Rejected, 1)
		// on register = active + dormant + pending + suspended + blacklisted
		//             = 5 + 0 + 1 + 0 + 0 = 6 (excludes the exited individual
		// and the rejected ngo)
		assertEq(t, "total_on_register", c.TotalOnRegister, 6)
		// servicing = active + dormant
		assertEq(t, "total_active_servicing", c.TotalActiveServicing, 5)
	})

	t.Run("kind=individual", func(t *testing.T) {
		c := callCounts(ctx, t, dbPool, statuses, tenantID, store.CPKindIndividual, nil, prefix)
		assertEq(t, "total_directory", c.TotalDirectory, 5)
		assertEq(t, "individuals", c.Individuals, 5)
		assertEq(t, "institutions", c.Institutions, 0)
		assertEq(t, "active", c.Active, 3)
		assertEq(t, "pending", c.Pending, 1)
		assertEq(t, "exited", c.Exited, 1)
	})

	t.Run("kind=institutional", func(t *testing.T) {
		c := callCounts(ctx, t, dbPool, statuses, tenantID, store.CPKindInstitutional, nil, prefix)
		assertEq(t, "total_directory", c.TotalDirectory, 3)
		assertEq(t, "individuals", c.Individuals, 0)
		assertEq(t, "institutions", c.Institutions, 3)
		assertEq(t, "active", c.Active, 2)
		assertEq(t, "rejected", c.Rejected, 1)
	})

	t.Run("status=active across all kinds", func(t *testing.T) {
		c := callCounts(ctx, t, dbPool, statuses, tenantID, store.CPKindAll,
			[]domain.MemberStatus{domain.StatusActive}, prefix)
		assertEq(t, "total_directory", c.TotalDirectory, 5)
		assertEq(t, "active", c.Active, 5)
		// All other buckets must be zero — the filter narrows the
		// base CTE so they cannot leak in.
		assertEq(t, "pending", c.Pending, 0)
		assertEq(t, "exited", c.Exited, 0)
	})

	t.Run("q filter narrows to single fixture row", func(t *testing.T) {
		// Search by the display name prefix + a row-specific suffix.
		// Each seeded row's display_name follows
		// "<prefix> <kind> <status> <i>", so this search must match
		// exactly the single chama-active-0 row.
		needle := fmt.Sprintf("%s chama active 0", prefix)
		c := callCounts(ctx, t, dbPool, statuses, tenantID, store.CPKindAll, nil, needle)
		assertEq(t, "total_directory", c.TotalDirectory, 1)
		assertEq(t, "active", c.Active, 1)
		assertEq(t, "individuals", c.Individuals, 0)
		assertEq(t, "institutions", c.Institutions, 1)
	})
}

func TestCounterpartyStatusCounts_HTTPHandler(t *testing.T) {
	pool, tenantID, cleanup := openTestPool(t)
	defer cleanup()
	ctx := context.Background()
	dbPool := &db.Pool{Pool: pool}

	prefix := seedCountsFixture(ctx, t, dbPool, tenantID, []fixture{
		{"individual", "active", 2},
		{"individual", "pending", 1},
		{"chama", "active", 1},
	})

	h := &StatusHandler{
		DB:     dbPool,
		Status: store.NewStatusChangeStore(pool),
	}
	r := chi.NewRouter()
	r.Use(injectAuth(tenantID, uuid.New()))
	r.Get("/v1/counterparties/status/counts", h.CountsV2)
	srv := httptest.NewServer(r)
	defer srv.Close()

	t.Run("all kinds with q scope", func(t *testing.T) {
		url := srv.URL + "/v1/counterparties/status/counts?q=" + prefix
		body := httpGetJSON(t, url)
		assertJSONInt(t, body, "total_directory", 4)
		assertJSONInt(t, body, "individuals", 3)
		assertJSONInt(t, body, "institutions", 1)
		assertJSONInt(t, body, "active", 3)
		assertJSONInt(t, body, "pending", 1)
	})

	t.Run("kind=individual + status=active", func(t *testing.T) {
		url := srv.URL + "/v1/counterparties/status/counts?kind=individual&status=active&q=" + prefix
		body := httpGetJSON(t, url)
		assertJSONInt(t, body, "total_directory", 2)
		assertJSONInt(t, body, "individuals", 2)
		assertJSONInt(t, body, "institutions", 0)
	})

	t.Run("kind=institutional", func(t *testing.T) {
		url := srv.URL + "/v1/counterparties/status/counts?kind=institutional&q=" + prefix
		body := httpGetJSON(t, url)
		assertJSONInt(t, body, "total_directory", 1)
		assertJSONInt(t, body, "individuals", 0)
		assertJSONInt(t, body, "institutions", 1)
	})

	t.Run("invalid kind → 400", func(t *testing.T) {
		url := srv.URL + "/v1/counterparties/status/counts?kind=banana"
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("kind=banana: want 400, got %d", resp.StatusCode)
		}
	})
}

func TestCounterpartyStatusCounts_RLSAcrossTenants(t *testing.T) {
	pool, tenantA, cleanup := openTestPool(t)
	defer cleanup()
	ctx := context.Background()
	dbPool := &db.Pool{Pool: pool}

	// Pick a SECOND tenant. The test is a skip-if-only-one-exists,
	// not a fail — the dev DB may legitimately have just the seed
	// tenant.
	var tenantB uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM tenants WHERE id <> $1 LIMIT 1`, tenantA,
	).Scan(&tenantB); err != nil {
		t.Skipf("only one tenant in DB; cannot exercise cross-tenant RLS: %v", err)
	}

	prefixA := seedCountsFixture(ctx, t, dbPool, tenantA, []fixture{
		{"individual", "active", 3},
	})
	// Use the SAME prefix in tenant B's seed so we know the RLS
	// boundary — not the q filter — is what's separating the rows.
	prefixB := seedCountsFixtureWithLabel(ctx, t, dbPool, tenantB, prefixA, []fixture{
		{"individual", "active", 7},
		{"chama", "active", 2},
	})
	if prefixA != prefixB {
		t.Fatalf("test setup invariant broken: tenants should share prefix %q vs %q", prefixA, prefixB)
	}

	statuses := store.NewStatusChangeStore(pool)

	// Tenant A sees only its 3 actives — tenant B's 9 must be hidden.
	cA := callCounts(ctx, t, dbPool, statuses, tenantA, store.CPKindAll, nil, prefixA)
	assertEq(t, "tenantA total_directory", cA.TotalDirectory, 3)
	assertEq(t, "tenantA individuals", cA.Individuals, 3)
	assertEq(t, "tenantA institutions", cA.Institutions, 0)

	cB := callCounts(ctx, t, dbPool, statuses, tenantB, store.CPKindAll, nil, prefixB)
	assertEq(t, "tenantB total_directory", cB.TotalDirectory, 9)
	assertEq(t, "tenantB individuals", cB.Individuals, 7)
	assertEq(t, "tenantB institutions", cB.Institutions, 2)
}

// ─── helpers ───

func seedCountsFixture(ctx context.Context, t *testing.T, dbPool *db.Pool, tenantID uuid.UUID, mix []fixture) string {
	t.Helper()
	prefix := fmt.Sprintf("%s-%d", fixtureMix, time.Now().UnixNano())
	return seedCountsFixtureWithLabel(ctx, t, dbPool, tenantID, prefix, mix)
}

func seedCountsFixtureWithLabel(ctx context.Context, t *testing.T, dbPool *db.Pool, tenantID uuid.UUID, prefix string, mix []fixture) string {
	t.Helper()
	var inserted []uuid.UUID
	err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		for _, f := range mix {
			payloadCol, payloadJSON := payloadForKind(f.kind, prefix)
			for i := 0; i < f.count; i++ {
				name := fmt.Sprintf("%s %s %s %d", prefix, f.kind, f.status, i)
				cpNo := fmt.Sprintf("%s-%s-%s-%d", prefix, f.kind, f.status, i)
				var id uuid.UUID
				err := tx.QueryRow(ctx, fmt.Sprintf(`
					INSERT INTO counterparties (id, tenant_id, kind, status, cp_number, display_name, %s)
					VALUES (gen_random_uuid(), $1, $2::counterparty_kind, $3::counterparty_status,
					        $4, $5, $6::jsonb)
					RETURNING id
				`, payloadCol),
					tenantID, f.kind, f.status, cpNo, name, payloadJSON,
				).Scan(&id)
				if err != nil {
					return fmt.Errorf("insert %s/%s #%d: %w", f.kind, f.status, i, err)
				}
				inserted = append(inserted, id)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed counts fixture: %v", err)
	}
	// Best-effort cleanup so re-runs don't accumulate.
	t.Cleanup(func() {
		_ = dbPool.WithTenantTx(context.Background(), tenantID, func(tx pgx.Tx) error {
			if len(inserted) == 0 {
				return nil
			}
			_, _ = tx.Exec(context.Background(),
				`DELETE FROM counterparties WHERE id = ANY($1::uuid[])`, inserted)
			return nil
		})
	})
	return prefix
}

func payloadForKind(kind, prefix string) (col, jsonStr string) {
	if kind == "individual" {
		return "individual", fmt.Sprintf(`{"full_name":"%s"}`, prefix)
	}
	return "institution", fmt.Sprintf(`{"registration_no":"%s"}`, prefix)
}

func callCounts(
	ctx context.Context, t *testing.T, dbPool *db.Pool,
	statuses *store.StatusChangeStore,
	tenantID uuid.UUID,
	kind store.CounterpartyKindFilter,
	statusFilter []domain.MemberStatus,
	q string,
) *store.CounterpartyStatusCounts {
	t.Helper()
	var out *store.CounterpartyStatusCounts
	err := dbPool.WithTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, err := statuses.CounterpartyStatusCountsTx(ctx, tx, tenantID, kind, statusFilter, q)
		if err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		t.Fatalf("counterparty_status_counts: %v", err)
	}
	return out
}

func assertEq(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s: want %d, got %d", name, want, got)
	}
}

func httpGetJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("get %s: status %d, body %s", url, resp.StatusCode, b)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

func assertJSONInt(t *testing.T, body map[string]any, key string, want int) {
	t.Helper()
	v, ok := body[key]
	if !ok {
		t.Errorf("missing key %q in body", key)
		return
	}
	// json.Number? float? — depends on decoder settings. Coerce.
	var got int
	switch n := v.(type) {
	case float64:
		got = int(n)
	case int:
		got = n
	default:
		t.Errorf("%s: unexpected type %T (%v)", key, v, v)
		return
	}
	if got != want {
		t.Errorf("%s: want %d, got %d", key, want, got)
	}
}

// silence unused-import warnings when the test body is trimmed in the
// future — these are used elsewhere in the file but Go's blank-block
// import rule doesn't pick that up.
var _ = strings.Contains
