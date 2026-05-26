// Hand-rolled Prometheus metrics for the mpesa service.
//
// Why hand-rolled instead of the prometheus/client_golang lib:
//   • Adds zero new dependencies (the package is ~150 LoC pure stdlib).
//   • Mpesa's metric surface is small + fixed: a handful of counters
//     plus one duration sum/count pair for the distribution_run
//     histogram approximation.
//   • Grafana renders the text-format output identically to what
//     the official lib emits, so the dashboards don't care.
//
// What this is NOT: a full Prometheus client. Histograms here are
// approximated as (sum, count) pairs — Grafana can compute averages
// but not native quantiles. If we ever need real p95/p99 we'll swap
// to the official lib and the wire shape stays compatible.

package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonic uint64 over a fixed label set. Construct
// once at startup; never deletes labels (Prometheus convention —
// counters accumulate forever within one process lifetime).
type Counter struct {
	name string
	help string
	// labelNames is the ordered list of label keys this counter
	// expects. labels appearing in Inc() must match this list
	// position-by-position.
	labelNames []string
	mu         sync.RWMutex
	series     map[string]*atomic.Uint64 // key = joined label values
}

func NewCounter(name, help string, labelNames ...string) *Counter {
	return &Counter{name: name, help: help, labelNames: labelNames, series: map[string]*atomic.Uint64{}}
}

// Inc bumps the counter for the supplied label values. labels must
// match the construction order; mismatched length is silently
// ignored to avoid panicking a hot code path.
func (c *Counter) Inc(labels ...string) {
	if len(labels) != len(c.labelNames) {
		return
	}
	key := strings.Join(labels, "|")
	c.mu.RLock()
	v := c.series[key]
	c.mu.RUnlock()
	if v == nil {
		c.mu.Lock()
		v = c.series[key]
		if v == nil {
			v = &atomic.Uint64{}
			c.series[key] = v
		}
		c.mu.Unlock()
	}
	v.Add(1)
}

// Add increments the counter by n. Used for amount-aggregating
// counters (e.g. mpesa_outbound_amount_total).
func (c *Counter) Add(n uint64, labels ...string) {
	if len(labels) != len(c.labelNames) {
		return
	}
	key := strings.Join(labels, "|")
	c.mu.RLock()
	v := c.series[key]
	c.mu.RUnlock()
	if v == nil {
		c.mu.Lock()
		v = c.series[key]
		if v == nil {
			v = &atomic.Uint64{}
			c.series[key] = v
		}
		c.mu.Unlock()
	}
	v.Add(n)
}

// SumCount is a duration approximation: a running sum + count pair.
// Grafana renders avg via sum/count. Real quantiles are deferred to
// a future migration to the official client lib.
type SumCount struct {
	name       string
	help       string
	labelNames []string
	mu         sync.RWMutex
	series     map[string]*sumCountSlot
}

type sumCountSlot struct {
	sumMicros atomic.Uint64
	count     atomic.Uint64
}

func NewSumCount(name, help string, labelNames ...string) *SumCount {
	return &SumCount{name: name, help: help, labelNames: labelNames, series: map[string]*sumCountSlot{}}
}

// Observe records one duration sample.
func (s *SumCount) Observe(d time.Duration, labels ...string) {
	if len(labels) != len(s.labelNames) {
		return
	}
	key := strings.Join(labels, "|")
	s.mu.RLock()
	slot := s.series[key]
	s.mu.RUnlock()
	if slot == nil {
		s.mu.Lock()
		slot = s.series[key]
		if slot == nil {
			slot = &sumCountSlot{}
			s.series[key] = slot
		}
		s.mu.Unlock()
	}
	slot.sumMicros.Add(uint64(d.Microseconds()))
	slot.count.Add(1)
}

// ─────────── registry ───────────

// Registry — process-global default. New() returns a fresh one for
// tests that want isolation.
var Default = New()

type Registry struct {
	counters  []*Counter
	sumCounts []*SumCount
}

func New() *Registry { return &Registry{} }

// Register adds a metric to the registry. Calling twice with the
// same metric is safe (the registry de-dupes by pointer).
func (r *Registry) Register(m any) {
	switch v := m.(type) {
	case *Counter:
		for _, existing := range r.counters {
			if existing == v {
				return
			}
		}
		r.counters = append(r.counters, v)
	case *SumCount:
		for _, existing := range r.sumCounts {
			if existing == v {
				return
			}
		}
		r.sumCounts = append(r.sumCounts, v)
	}
}

// MustRegister is a convenience that registers a metric to the
// Default registry. Most call sites use this at package init time.
func MustRegister(m any) { Default.Register(m) }

// Handler returns an http.Handler that serves /metrics in
// Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WriteText(w)
	})
}

// HandlerForDefault is what callers wire to the /metrics route.
func HandlerForDefault() http.Handler { return Default.Handler() }

// WriteText writes the registry's snapshot in Prometheus text format.
// Exposed for tests to assert format compliance without standing up
// an httptest server.
func (r *Registry) WriteText(w io.Writer) {
	// Counters
	for _, c := range r.counters {
		fmt.Fprintf(w, "# HELP %s %s\n", c.name, escapeHelp(c.help))
		fmt.Fprintf(w, "# TYPE %s counter\n", c.name)
		c.mu.RLock()
		keys := make([]string, 0, len(c.series))
		for k := range c.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			labels := strings.Split(key, "|")
			fmt.Fprintf(w, "%s%s %d\n", c.name, formatLabels(c.labelNames, labels), c.series[key].Load())
		}
		c.mu.RUnlock()
	}
	// SumCount pairs
	for _, s := range r.sumCounts {
		fmt.Fprintf(w, "# HELP %s_seconds_sum %s (sum of observed durations in seconds)\n", s.name, escapeHelp(s.help))
		fmt.Fprintf(w, "# TYPE %s_seconds_sum counter\n", s.name)
		s.mu.RLock()
		keys := make([]string, 0, len(s.series))
		for k := range s.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			labels := strings.Split(key, "|")
			slot := s.series[key]
			secs := float64(slot.sumMicros.Load()) / 1e6
			fmt.Fprintf(w, "%s_seconds_sum%s %.6f\n", s.name, formatLabels(s.labelNames, labels), secs)
		}
		fmt.Fprintf(w, "# HELP %s_count %s (number of observations)\n", s.name, escapeHelp(s.help))
		fmt.Fprintf(w, "# TYPE %s_count counter\n", s.name)
		for _, key := range keys {
			labels := strings.Split(key, "|")
			slot := s.series[key]
			fmt.Fprintf(w, "%s_count%s %d\n", s.name, formatLabels(s.labelNames, labels), slot.count.Load())
		}
		s.mu.RUnlock()
	}
}

func formatLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteByte('"')
		v := ""
		if i < len(values) {
			v = values[i]
		}
		// Escape backslash + double-quote + newline per Prometheus
		// text format spec.
		v = strings.ReplaceAll(v, `\`, `\\`)
		v = strings.ReplaceAll(v, `"`, `\"`)
		v = strings.ReplaceAll(v, "\n", `\n`)
		b.WriteString(v)
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// ─────────── known metrics ───────────
//
// Centralised here so producers + consumers don't drift on names.
// Init-time registration so every binary that imports this package
// exposes the same surface.

var (
	InboundTotal = NewCounter(
		"mpesa_inbound_total",
		"Number of M-PESA C2B confirmations processed, by status.",
		"status", "tenant_id",
	)
	OutboundTotal = NewCounter(
		"mpesa_outbound_total",
		"Number of M-PESA B2C outbound requests, by status.",
		"status", "tenant_id",
	)
	UnallocatedTotal = NewCounter(
		"mpesa_unallocated_total",
		"Number of inbound payments that landed unallocated (no resolver match).",
		"tenant_id",
	)
	DistributionDuration = NewSumCount(
		"mpesa_distribution_duration",
		"Wall-clock time the distributor takes to plan + apply + audit a single inbound event.",
		"tenant_id",
	)
	ReconcilerRuns = NewCounter(
		"mpesa_reconciler_runs_total",
		"Daily reconciler completions, by outcome (ok/failed).",
		"outcome", "tenant_id",
	)
	ReconcilerDiffs = NewCounter(
		"mpesa_reconciler_diffs_total",
		"Number of reconciliation diffs detected per run.",
		"kind", "tenant_id",
	)
)

func init() {
	MustRegister(InboundTotal)
	MustRegister(OutboundTotal)
	MustRegister(UnallocatedTotal)
	MustRegister(DistributionDuration)
	MustRegister(ReconcilerRuns)
	MustRegister(ReconcilerDiffs)
}
