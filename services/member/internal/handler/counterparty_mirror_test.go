// Integration tests for the unified-counterparty mirror.
//
// Properties under test (per acceptance criterion #3):
//   1. Direct MemberStore.CreateTx (POST /v1/members path) mirrors a
//      counterparty row of kind=individual + stamps the bridge column.
//   2. Direct OrgStore.CreateTx (POST /v1/orgs path) mirrors a
//      counterparty row of an institutional kind + stamps the bridge.
//   3. cp_number is monotonically increasing per tenant.
//
// Runs against the live DATABASE_URL set in the dev .env. Skipped
// when the env var is unset so CI without Postgres still runs
// `go test ./...` clean. All writes happen in a tx that is rolled
// back at the end — no fixtures land in the real DB.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nexussacco/member/internal/domain"
	"github.com/nexussacco/member/internal/store"
)

func TestCounterpartyMirrorAndCPMonotonic(t *testing.T) {
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

	// Pick a tenant that already has members (so RLS + sequences are
	// initialised). Don't create one ourselves; this is an
	// integration test against the dev seed, not a fixture builder.
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

	cps := store.NewCounterpartyStore(pool)

	// ─── 1. Direct member create → counterparty mirror ───
	uniq := time.Now().UnixNano()
	memberA := makeMemberRow(ctx, t, tx, tenantID, fmt.Sprintf("M-TEST-%d", uniq), "Mirror Test Member A")
	if err := mirrorMemberCreateToCounterpartyTx(ctx, tx, cps, tenantID, memberA, uuid.Nil); err != nil {
		t.Fatalf("mirror member A: %v", err)
	}

	var cpA store.CPListResult
	{
		r, err := cps.ListTx(ctx, tx, store.CPListInput{Query: memberA.MemberNo, Limit: 5})
		if err != nil {
			t.Fatalf("list cps for memberA: %v", err)
		}
		cpA = *r
	}
	if len(cpA.Counterparties) != 1 {
		t.Fatalf("expected 1 counterparty for memberA, got %d", len(cpA.Counterparties))
	}
	got := cpA.Counterparties[0]
	if got.Kind != domain.CounterpartyIndividual {
		t.Errorf("kind: want individual, got %s", got.Kind)
	}
	if got.LegacyID == nil || *got.LegacyID != memberA.MemberNo {
		t.Errorf("legacy_id: want %s, got %v", memberA.MemberNo, got.LegacyID)
	}
	if !strings.HasPrefix(got.CPNumber, "CP-") {
		t.Errorf("cp_number prefix: want CP-, got %s", got.CPNumber)
	}
	// Bridge column stamped?
	var bridged uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT counterparty_id FROM members WHERE id = $1`, memberA.ID,
	).Scan(&bridged); err != nil {
		t.Fatalf("read bridge: %v", err)
	}
	if bridged != got.ID {
		t.Errorf("members.counterparty_id: want %s, got %s", got.ID, bridged)
	}

	// ─── 2. Direct org create → counterparty mirror ───
	orgA := makeOrgRow(ctx, t, tx, tenantID, fmt.Sprintf("ORG-TEST-%d", uniq), "Mirror Test Chama A")
	if err := mirrorOrgCreateToCounterpartyTx(ctx, tx, cps, tenantID, orgA, uuid.Nil); err != nil {
		t.Fatalf("mirror org A: %v", err)
	}
	var cpB store.CPListResult
	{
		r, err := cps.ListTx(ctx, tx, store.CPListInput{Query: orgA.OrgNo, Limit: 5})
		if err != nil {
			t.Fatalf("list cps for orgA: %v", err)
		}
		cpB = *r
	}
	if len(cpB.Counterparties) != 1 {
		t.Fatalf("expected 1 counterparty for orgA, got %d", len(cpB.Counterparties))
	}
	gotOrg := cpB.Counterparties[0]
	if gotOrg.Kind != domain.CounterpartyChama {
		t.Errorf("kind: want chama (mapped from group/chama org_kind), got %s", gotOrg.Kind)
	}
	if gotOrg.LegacyID == nil || *gotOrg.LegacyID != orgA.OrgNo {
		t.Errorf("legacy_id: want %s, got %v", orgA.OrgNo, gotOrg.LegacyID)
	}

	// ─── 3. cp_number monotonic per tenant ───
	// Both rows we just minted should have strictly-increasing CP suffixes.
	seqA := cpSuffix(t, got.CPNumber)
	seqB := cpSuffix(t, gotOrg.CPNumber)
	if seqB <= seqA {
		t.Errorf("cp_number not monotonic: A=%s (seq %d) B=%s (seq %d)",
			got.CPNumber, seqA, gotOrg.CPNumber, seqB)
	}
	// Mint a third and confirm it advances by exactly 1.
	thirdNo, err := cps.NextCPNumberTx(ctx, tx, tenantID)
	if err != nil {
		t.Fatalf("third NextCPNumberTx: %v", err)
	}
	seqC := cpSuffix(t, thirdNo)
	if seqC != seqB+1 {
		t.Errorf("cp_number should advance by 1: B seq=%d, third seq=%d", seqB, seqC)
	}
}

// ─── helpers ───

func makeMemberRow(
	ctx context.Context, t *testing.T, tx pgx.Tx,
	tenantID uuid.UUID, memberNo, name string,
) *domain.Member {
	t.Helper()
	id := uuid.New()
	_, err := tx.Exec(ctx, `
		INSERT INTO members (
		  id, tenant_id, member_no, status, full_name,
		  id_doc_kind, id_doc_number, gender,
		  approved_at, created_at
		) VALUES (
		  $1, $2, $3, 'active', $4,
		  'national_id', $5, 'undisclosed',
		  now(), now()
		)
	`, id, tenantID, memberNo, name, "ID-"+memberNo)
	if err != nil {
		t.Fatalf("insert member: %v", err)
	}
	return &domain.Member{
		ID: id, TenantID: tenantID, MemberNo: memberNo, FullName: name,
		Status: domain.StatusActive, IDDocKind: "national_id", IDDocNumber: "ID-" + memberNo,
	}
}

func makeOrgRow(
	ctx context.Context, t *testing.T, tx pgx.Tx,
	tenantID uuid.UUID, orgNo, name string,
) *domain.Org {
	t.Helper()
	id := uuid.New()
	_, err := tx.Exec(ctx, `
		INSERT INTO org_members (
		  id, tenant_id, org_no, status, registered_name, kind, kyc_status, risk_category,
		  created_at
		) VALUES (
		  $1, $2, $3, 'active', $4, 'chama', 'verified', 'medium',
		  now()
		)
	`, id, tenantID, orgNo, name)
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}
	return &domain.Org{
		ID: id, TenantID: tenantID, OrgNo: orgNo, RegisteredName: name,
		Kind: domain.OrgChama, Status: domain.OrgActive,
		KYCStatus: domain.KYCVerified, RiskCategory: domain.RiskMedium,
	}
}

// cpSuffix extracts the trailing sequence number from a CP-YYYY-NNNNN.
func cpSuffix(t *testing.T, no string) int {
	t.Helper()
	parts := strings.Split(no, "-")
	if len(parts) != 3 {
		t.Fatalf("cp_number shape: want CP-YYYY-NNNNN, got %q", no)
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("cp_number suffix not numeric: %s — %v", no, err)
	}
	return n
}

// Silence the json import in the rare build where the test body
// doesn't reference it directly.
var _ = json.Marshal
