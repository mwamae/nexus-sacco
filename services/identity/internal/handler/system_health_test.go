package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/nexussacco/identity/internal/auth"
	"github.com/nexussacco/identity/internal/domain"
	"github.com/nexussacco/identity/internal/middleware"
	"github.com/nexussacco/shared/healthx"
)

func TestParseTargets_EmptyFallsBackToDefaults(t *testing.T) {
	got := parseTargets("")
	if len(got) == 0 {
		t.Fatal("expected default targets when env unset")
	}
	// All 7 services must be present in the default mapping.
	want := map[string]bool{
		"identity": true, "member": true, "workflow": true,
		"savings": true, "notification": true, "accounting": true, "mpesa": true,
	}
	for _, e := range got {
		delete(want, e.Name)
	}
	if len(want) > 0 {
		var missing []string
		for k := range want {
			missing = append(missing, k)
		}
		t.Errorf("default targets missing: %v", missing)
	}
}

func TestParseTargets_CustomFormat(t *testing.T) {
	got := parseTargets("foo=http://foo:1234|core,bar=http://bar:5678,empty=,=http://nope")
	if len(got) != 2 {
		t.Fatalf("expected 2 valid entries (foo + bar), got %d: %+v", len(got), got)
	}
	if got[0].Name != "foo" || got[0].URL != "http://foo:1234" || got[0].Role != "core" {
		t.Errorf("foo entry malformed: %+v", got[0])
	}
	// Bar has no role; defaults to "service".
	if got[1].Name != "bar" || got[1].Role != "service" {
		t.Errorf("bar entry malformed (expected role=service): %+v", got[1])
	}
}

func TestClassifyWorker(t *testing.T) {
	cases := []struct {
		stalenessSec int64
		want         healthx.Status
	}{
		{0, healthx.StatusOK},
		{30, healthx.StatusOK},
		{59, healthx.StatusOK},
		{60, healthx.StatusDegraded},
		{90, healthx.StatusDegraded},
		{119, healthx.StatusDegraded},
		{120, healthx.StatusDown},
		{500, healthx.StatusDown},
	}
	for _, tc := range cases {
		if got := classifyWorker(tc.stalenessSec); got != tc.want {
			t.Errorf("classifyWorker(%d) = %s, want %s", tc.stalenessSec, got, tc.want)
		}
	}
}

func TestWorseStatus_PicksMoreSevere(t *testing.T) {
	if worseStatus(healthx.StatusOK, healthx.StatusDown) != healthx.StatusDown {
		t.Error("down should win over ok")
	}
	if worseStatus(healthx.StatusDegraded, healthx.StatusOK) != healthx.StatusDegraded {
		t.Error("degraded should win over ok")
	}
	if worseStatus(healthx.StatusDown, healthx.StatusDegraded) != healthx.StatusDown {
		t.Error("down should win over degraded")
	}
}

func TestProbeService_ParsesHealthxEnvelope(t *testing.T) {
	// Fake upstream returns the standard healthx.Response shape.
	body := healthx.Response{
		Status:  healthx.StatusDegraded,
		Service: "demo",
		Version: "abc123",
		Dependencies: map[string]healthx.DependencyResult{
			"db": {Reachable: true, LatencyMS: 5},
		},
		Details: map[string]any{"outbox_pending": float64(7)},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/healthz") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable) // degraded → 503
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()

	h := &SystemHealthHandler{HTTPClient: &http.Client{Timeout: 1 * time.Second}}
	got := h.probeService(t.Context(), targetEntry{
		Name: "demo", URL: srv.URL, Role: "core",
	})
	if got.Status != healthx.StatusDegraded {
		t.Errorf("status: got %s, want degraded", got.Status)
	}
	if got.Version != "abc123" {
		t.Errorf("version: got %q", got.Version)
	}
	if got.Dependencies["db"].LatencyMS != 5 {
		t.Errorf("dependencies didn't round-trip: %+v", got.Dependencies)
	}
	if v, _ := got.Details["outbox_pending"].(float64); v != 7 {
		t.Errorf("details didn't round-trip: %+v", got.Details)
	}
	if got.Error != "" {
		t.Errorf("unexpected error field: %q", got.Error)
	}
}

func TestProbeService_TrivialStringResponseStillReadsAsOK(t *testing.T) {
	// Legacy services that haven't been migrated yet may still return
	// {"status":"ok"} — the aggregator should treat the 2xx as ok and
	// surface a soft warning rather than blowing up.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	h := &SystemHealthHandler{HTTPClient: &http.Client{Timeout: 1 * time.Second}}
	got := h.probeService(t.Context(), targetEntry{
		Name: "legacy", URL: srv.URL, Role: "core",
	})
	if got.Status != healthx.StatusOK {
		t.Errorf("legacy service should classify as ok, got %s", got.Status)
	}
}

func TestProbeService_ConnectionRefusedClassifiesAsDown(t *testing.T) {
	h := &SystemHealthHandler{HTTPClient: &http.Client{Timeout: 200 * time.Millisecond}}
	// Use a deliberately closed listener — port 1 is reserved.
	got := h.probeService(t.Context(), targetEntry{
		Name: "dead", URL: "http://127.0.0.1:1", Role: "core",
	})
	if got.Status != healthx.StatusDown {
		t.Errorf("unreachable upstream should be down, got %s", got.Status)
	}
	if got.Error == "" {
		t.Error("expected error field populated for unreachable upstream")
	}
}

// ─────────────── Auth-matrix tests for the route layer ───────────────
//
// These build a chi router that mirrors the same gate stack as
// services/identity/internal/handler/routes.go for the two endpoints
// under test:
//
//   /v1/platform/system-health → RequirePlatform + RequirePermission
//   /v1/platform-status        → no platform gate, no permission gate
//
// The snapshot is mocked via SystemHealthHandler.snapshotOverride so
// the tests don't need a real Postgres pool. Tenant + JWT context is
// injected by tiny test middleware that mimics what ResolveTenant +
// Authenticated would produce in production.

// newTestRouter mirrors the relevant subset of routes.go's wiring
// for the two endpoints under test.
func newTestRouter(h *SystemHealthHandler) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			// Slim status — no permission, no platform requirement.
			r.Get("/platform-status", h.GetForTenant)

			// Full aggregator — RequirePlatform + RequirePermission.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RequirePlatform)
				r.With(middleware.RequirePermission("platform:operations:view")).
					Get("/platform/system-health", h.Get)
			})
		})
	})
	return r
}

// withCtx wraps a request in a fresh context carrying the given
// tenant + claims, mirroring what ResolveTenant + Authenticated do.
// tenant=nil means platform host context.
func withCtx(req *http.Request, tenant *domain.Tenant, claims *auth.AccessClaims) *http.Request {
	ctx := req.Context()
	if tenant != nil {
		ctx = middleware.WithTenant(ctx, tenant)
	}
	if claims != nil {
		ctx = middleware.WithClaims(ctx, claims)
	}
	return req.WithContext(ctx)
}

func newFixedSnapshot() *SystemHealthResponse {
	return &SystemHealthResponse{
		OverallStatus: healthx.StatusOK,
		CheckedAt:     time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
		Services: []ServiceHealth{
			{Name: "identity", Role: "core", Status: healthx.StatusOK, LatencyMS: 4},
		},
		Infrastructure: map[string]InfrastructureCheck{
			"postgres": {Status: healthx.StatusOK, LatencyMS: 2},
		},
	}
}

func tenantStaffClaims(tenantID uuid.UUID) *auth.AccessClaims {
	return &auth.AccessClaims{
		TenantID:    tenantID.String(),
		UserID:      uuid.NewString(),
		Permissions: []string{"members:view"}, // ordinary staff perms only
	}
}

func platformAdminClaims() *auth.AccessClaims {
	return &auth.AccessClaims{
		TenantID:        uuid.Nil.String(),
		UserID:          uuid.NewString(),
		IsPlatformAdmin: true,
	}
}

func testTenant() *domain.Tenant {
	return &domain.Tenant{
		ID:     uuid.New(),
		Slug:   "tujenge",
		Name:   "Tujenge SACCO",
		Status: domain.TenantStatusActive,
	}
}

func TestPlatformSystemHealth_TenantSubdomainReturns404(t *testing.T) {
	h := &SystemHealthHandler{snapshotOverride: func(context.Context) *SystemHealthResponse {
		t.Fatal("snapshot should not be called when route is gated out")
		return nil
	}}
	router := newTestRouter(h)

	tn := testTenant()
	req := withCtx(httptest.NewRequest(http.MethodGet, "/v1/platform/system-health", nil),
		tn, tenantStaffClaims(tn.ID))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (RequirePlatform should reject tenant subdomain)", rec.Code)
	}
}

func TestPlatformSystemHealth_PlatformAdminReturns200WithFullPayload(t *testing.T) {
	h := &SystemHealthHandler{snapshotOverride: func(context.Context) *SystemHealthResponse {
		return newFixedSnapshot()
	}}
	router := newTestRouter(h)

	req := withCtx(httptest.NewRequest(http.MethodGet, "/v1/platform/system-health", nil),
		nil, platformAdminClaims())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	// Decode as the {data: …} envelope httpx.OK wraps responses in,
	// falling back to the raw struct if the wrapper is absent.
	var wrapped struct {
		Data *SystemHealthResponse `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &wrapped)
	body := wrapped.Data
	if body == nil {
		body = &SystemHealthResponse{}
		if err := json.Unmarshal(rec.Body.Bytes(), body); err != nil {
			t.Fatalf("decode body: %v (raw=%s)", err, rec.Body.String())
		}
	}
	if len(body.Services) == 0 {
		t.Errorf("expected full payload with services, got empty (raw=%s)", rec.Body.String())
	}
	if _, ok := body.Infrastructure["postgres"]; !ok {
		t.Errorf("expected infrastructure.postgres in full payload")
	}
}

func TestPlatformSystemHealth_TenantStaffOnPlatformReturns403(t *testing.T) {
	h := &SystemHealthHandler{snapshotOverride: func(context.Context) *SystemHealthResponse {
		t.Fatal("snapshot should not be called when permission gate rejects")
		return nil
	}}
	router := newTestRouter(h)

	tn := testTenant()
	// Tenant context is nil (platform host) but claims belong to
	// regular staff with no platform:operations:view permission.
	req := withCtx(httptest.NewRequest(http.MethodGet, "/v1/platform/system-health", nil),
		nil, tenantStaffClaims(tn.ID))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403 (missing platform:operations:view)", rec.Code)
	}
}

func TestPlatformStatus_TenantSubdomainReturns200Slim(t *testing.T) {
	h := &SystemHealthHandler{snapshotOverride: func(context.Context) *SystemHealthResponse {
		s := newFixedSnapshot()
		s.OverallStatus = healthx.StatusDegraded
		return s
	}}
	router := newTestRouter(h)

	tn := testTenant()
	req := withCtx(httptest.NewRequest(http.MethodGet, "/v1/platform-status", nil),
		tn, tenantStaffClaims(tn.ID))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s, want 200", rec.Code, rec.Body.String())
	}

	body := decodePlatformStatus(t, rec.Body.Bytes())
	if body.OverallStatus != healthx.StatusDegraded {
		t.Errorf("overall_status: got %q, want degraded", body.OverallStatus)
	}
	if body.Message == "" {
		t.Error("message should be auto-derived from status, got empty")
	}
	// Slim shape — assert no service-level fields leak through. We
	// re-decode into a generic map and ensure none of the rich keys
	// the platform endpoint returns are present.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if data, ok := raw["data"].(map[string]any); ok {
		raw = data
	}
	for _, leak := range []string{"services", "workers", "infrastructure"} {
		if _, found := raw[leak]; found {
			t.Errorf("slim payload leaked %q field — tenants should never see it (raw=%s)", leak, rec.Body.String())
		}
	}
}

func TestPlatformStatus_PlatformHostAlsoReturns200(t *testing.T) {
	h := &SystemHealthHandler{snapshotOverride: func(context.Context) *SystemHealthResponse {
		return newFixedSnapshot()
	}}
	router := newTestRouter(h)

	// No tenant in context (platform host), no platform-admin claim
	// either — slim endpoint is intentionally open to any authed user.
	req := withCtx(httptest.NewRequest(http.MethodGet, "/v1/platform-status", nil),
		nil, &auth.AccessClaims{UserID: uuid.NewString()})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (slim endpoint should work on platform host too)", rec.Code)
	}
}

// Acceptance check #6: when SYSTEM_HEALTH_TARGETS yields no services,
// the response envelope is still well-formed (the UI relies on the
// services + infrastructure + workers keys being present). We exercise
// this via a snapshot value that mirrors what runProbes produces in
// that case — empty services slice, infrastructure populated, status
// derived from the worst-of (still ok if infra is ok).
func TestSnapshot_EmptyTargetsStillReturnsEnvelope(t *testing.T) {
	h := &SystemHealthHandler{snapshotOverride: func(context.Context) *SystemHealthResponse {
		return &SystemHealthResponse{
			OverallStatus: healthx.StatusOK,
			CheckedAt:     time.Now().UTC(),
			Services:      []ServiceHealth{}, // empty — what an empty target list produces
			Infrastructure: map[string]InfrastructureCheck{
				"postgres": {Status: healthx.StatusOK, LatencyMS: 1},
				"redis":    {Status: healthx.StatusOK, Note: "not configured"},
			},
			Workers: nil,
		}
	}}
	router := newTestRouter(h)
	req := withCtx(httptest.NewRequest(http.MethodGet, "/v1/platform/system-health", nil),
		nil, platformAdminClaims())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	body := raw
	if data, ok := raw["data"].(map[string]any); ok {
		body = data
	}
	if _, ok := body["services"]; !ok {
		t.Errorf("services key missing from envelope — UI relies on it being present (raw=%s)", rec.Body.String())
	}
	if _, ok := body["infrastructure"]; !ok {
		t.Errorf("infrastructure key missing from envelope")
	}
	if _, ok := body["overall_status"]; !ok {
		t.Errorf("overall_status key missing from envelope")
	}
}

func decodePlatformStatus(t *testing.T, b []byte) *PlatformStatusResponse {
	t.Helper()
	var wrapped struct {
		Data *PlatformStatusResponse `json:"data"`
	}
	_ = json.Unmarshal(b, &wrapped)
	if wrapped.Data != nil {
		return wrapped.Data
	}
	body := &PlatformStatusResponse{}
	if err := json.Unmarshal(b, body); err != nil {
		t.Fatalf("decode slim body: %v (raw=%s)", err, string(b))
	}
	return body
}
