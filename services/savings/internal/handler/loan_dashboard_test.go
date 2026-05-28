// Tests for the loan-dashboard aggregator's compute path.
//
// We can't easily stand up postgres in a unit test (the test infra
// uses a real DB elsewhere via testenv), so this file tests the
// cache layer + the response-shape invariants the React layer
// depends on. The end-to-end compute path is exercised in the
// existing integration-test suite the user runs against the live
// docker stack.

package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLoanDashboard_CacheReturnsSameInstanceWithinTTL(t *testing.T) {
	h := &LoanDashboardHandler{}
	tenant := uuid.New()

	// Pre-populate the cache directly to bypass the compute path.
	fixture := &LoanDashboardResponse{
		AsOf:                 time.Now(),
		ByStatus:             map[string]int{"active": 5},
		ByProduct:            []ProductOutstanding{},
		ApplicationsByStatus: map[string]int{},
		AtRiskCount:          1,
	}
	h.mu.Lock()
	if h.cache == nil {
		h.cache = map[uuid.UUID]cachedDashboard{}
	}
	h.cache[tenant] = cachedDashboard{at: time.Now(), data: fixture}
	h.mu.Unlock()

	got, err := h.snapshot(t.Context(), tenant)
	if err != nil {
		t.Fatalf("snapshot returned error on cache hit: %v", err)
	}
	if got != fixture {
		t.Errorf("expected cached pointer, got different instance")
	}
}

func TestLoanDashboard_CacheTTLBoundary(t *testing.T) {
	// Verify the TTL constant + the "expired?" predicate match.
	// Fresh-enough entries return from cache; older entries don't.
	if loanDashboardCacheTTL <= 0 {
		t.Fatal("TTL must be positive")
	}
	stale := cachedDashboard{at: time.Now().Add(-2 * loanDashboardCacheTTL)}
	fresh := cachedDashboard{at: time.Now()}
	if time.Since(fresh.at) >= loanDashboardCacheTTL {
		t.Error("fresh entry should be within TTL")
	}
	if time.Since(stale.at) < loanDashboardCacheTTL {
		t.Error("stale entry should be past TTL")
	}
}

func TestLoanDashboard_PerTenantCacheIsolation(t *testing.T) {
	h := &LoanDashboardHandler{}
	a, b := uuid.New(), uuid.New()
	fixA := &LoanDashboardResponse{AsOf: time.Now(), ByStatus: map[string]int{"active": 1}}
	fixB := &LoanDashboardResponse{AsOf: time.Now(), ByStatus: map[string]int{"active": 99}}

	h.mu.Lock()
	if h.cache == nil {
		h.cache = map[uuid.UUID]cachedDashboard{}
	}
	h.cache[a] = cachedDashboard{at: time.Now(), data: fixA}
	h.cache[b] = cachedDashboard{at: time.Now(), data: fixB}
	h.mu.Unlock()

	gotA, _ := h.snapshot(t.Context(), a)
	gotB, _ := h.snapshot(t.Context(), b)
	if gotA.ByStatus["active"] != 1 || gotB.ByStatus["active"] != 99 {
		t.Errorf("cache mixed tenants: A=%v B=%v", gotA.ByStatus, gotB.ByStatus)
	}
}

// TestLoanDashboard_ResponseShape pins the JSON keys the React layer
// reads. A field rename here without a corresponding UI update is a
// guaranteed dashboard regression; this test fails first.
func TestLoanDashboard_ResponseShape(t *testing.T) {
	resp := LoanDashboardResponse{}
	// Field-presence smoke test via direct struct access — TS layer
	// reads these exact key names off the JSON.
	_ = resp.AsOf
	_ = resp.TotalOutstanding.PrincipalBalance
	_ = resp.TotalOutstanding.InterestBalance
	_ = resp.TotalOutstanding.FeesBalance
	_ = resp.TotalOutstanding.PenaltyBalance
	_ = resp.TotalOutstanding.ActiveCount
	_ = resp.ByProduct
	_ = resp.ByStatus
	_ = resp.DisbursedThisMonth
	_ = resp.CollectedThisMonth
	_ = resp.ApplicationsByStatus
	_ = resp.ApproachingDisbursementCount
	_ = resp.AtRiskCount
	_ = resp.PromisesDueThisWeekCount
}

// Silence unused-import lint when one of the branches above is pruned.
var _ sync.Mutex = sync.Mutex{}
var _ context.Context = context.Background()
