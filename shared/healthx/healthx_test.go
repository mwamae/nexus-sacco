package healthx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuilder_AllOK(t *testing.T) {
	b := &Builder{
		Service:   "svc-a",
		StartedAt: time.Now().Add(-1 * time.Hour),
		Probes: map[string]Probe{
			"dep1": okProbe(5),
			"dep2": okProbe(10),
		},
	}
	r := b.Snapshot(context.Background(), 100*time.Millisecond)
	if r.Status != StatusOK {
		t.Errorf("status: want ok, got %s", r.Status)
	}
	if r.Service != "svc-a" {
		t.Errorf("service: %s", r.Service)
	}
	if len(r.Dependencies) != 2 {
		t.Errorf("deps: want 2, got %d", len(r.Dependencies))
	}
}

func TestBuilder_OneDepDown_RollsUpToDown(t *testing.T) {
	b := &Builder{
		Service: "svc-b",
		Probes: map[string]Probe{
			"db":   okProbe(2),
			"smtp": downProbe("connection refused"),
		},
	}
	r := b.Snapshot(context.Background(), 100*time.Millisecond)
	if r.Status != StatusDown {
		t.Errorf("status: want down (one dep down), got %s", r.Status)
	}
	if r.Dependencies["smtp"].Reachable {
		t.Error("smtp dep should be unreachable")
	}
	if r.Dependencies["smtp"].Error != "connection refused" {
		t.Errorf("smtp error: got %q", r.Dependencies["smtp"].Error)
	}
}

func TestBuilder_SelfReportDegradesOverallStatus(t *testing.T) {
	b := &Builder{
		Service: "svc-c",
		Probes: map[string]Probe{
			"db": okProbe(1),
		},
		DetailsAndStatus: func(ctx context.Context) (Status, map[string]any) {
			return StatusDegraded, map[string]any{"outbox_pending": 42}
		},
	}
	r := b.Snapshot(context.Background(), 100*time.Millisecond)
	if r.Status != StatusDegraded {
		t.Errorf("status: want degraded (self-report), got %s", r.Status)
	}
	if got := r.Details["outbox_pending"]; got != 42 {
		t.Errorf("details outbox_pending: %v", got)
	}
}

func TestBuilder_DepDownTrumpsSelfDegraded(t *testing.T) {
	// Worst-of: down > degraded.
	b := &Builder{
		Service: "svc-d",
		Probes: map[string]Probe{
			"db": downProbe("down"),
		},
		DetailsAndStatus: func(ctx context.Context) (Status, map[string]any) {
			return StatusDegraded, nil
		},
	}
	r := b.Snapshot(context.Background(), 100*time.Millisecond)
	if r.Status != StatusDown {
		t.Errorf("status: want down (dep > self-degraded), got %s", r.Status)
	}
}

func TestBuilder_DefaultStartedAtAndVersion(t *testing.T) {
	b := &Builder{Service: "svc-e"}
	r := b.Snapshot(context.Background(), 100*time.Millisecond)
	if r.Version != "dev" {
		t.Errorf("version: want 'dev' fallback, got %q", r.Version)
	}
}

func TestBuilder_HandlerHTTPCodes(t *testing.T) {
	okSrv := httptest.NewServer((&Builder{
		Service: "ok",
		Probes:  map[string]Probe{"x": okProbe(1)},
	}).Handler(50 * time.Millisecond))
	defer okSrv.Close()
	resp, _ := http.Get(okSrv.URL)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ok service: want 200, got %d", resp.StatusCode)
	}
	var body Response
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if body.Status != StatusOK {
		t.Errorf("ok body.Status: %s", body.Status)
	}

	downSrv := httptest.NewServer((&Builder{
		Service: "down",
		Probes:  map[string]Probe{"x": downProbe("nope")},
	}).Handler(50 * time.Millisecond))
	defer downSrv.Close()
	resp2, _ := http.Get(downSrv.URL)
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("down service: want 503, got %d", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
}

func TestWorseSeverityOrder(t *testing.T) {
	cases := []struct {
		a, b Status
		want Status
	}{
		{StatusOK, StatusOK, StatusOK},
		{StatusOK, StatusDegraded, StatusDegraded},
		{StatusDegraded, StatusOK, StatusDegraded},
		{StatusDegraded, StatusDown, StatusDown},
		{StatusDown, StatusDegraded, StatusDown},
		{StatusDown, StatusDown, StatusDown},
	}
	for _, tc := range cases {
		if got := worse(tc.a, tc.b); got != tc.want {
			t.Errorf("worse(%s,%s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}

func okProbe(latencyMs int64) Probe {
	return func(ctx context.Context) DependencyResult {
		return DependencyResult{Reachable: true, LatencyMS: latencyMs}
	}
}
func downProbe(err string) Probe {
	return func(ctx context.Context) DependencyResult {
		return DependencyResult{Reachable: false, Error: err}
	}
}
