package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestCounter_IncAndExport(t *testing.T) {
	r := New()
	c := NewCounter("test_counter", "demo counter", "kind")
	r.Register(c)
	c.Inc("a")
	c.Inc("a")
	c.Inc("b")

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()
	for _, want := range []string{
		`test_counter{kind="a"} 2`,
		`test_counter{kind="b"} 1`,
		`# TYPE test_counter counter`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSumCount_Observe(t *testing.T) {
	r := New()
	s := NewSumCount("test_duration", "demo duration", "tenant")
	r.Register(s)
	s.Observe(100 * time.Millisecond, "t1")
	s.Observe(300 * time.Millisecond, "t1")

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()
	if !strings.Contains(out, `test_duration_count{tenant="t1"} 2`) {
		t.Errorf("missing count: %s", out)
	}
	if !strings.Contains(out, `test_duration_seconds_sum{tenant="t1"} 0.400000`) {
		t.Errorf("missing sum: %s", out)
	}
}

func TestCounter_MismatchedLabelsIsNoOp(t *testing.T) {
	c := NewCounter("test", "x", "k1", "k2")
	// Should silently no-op, not panic.
	c.Inc("only_one")
	c.Add(5, "only_one")
}

func TestFormatLabels_EscapesQuotes(t *testing.T) {
	got := formatLabels([]string{"k"}, []string{`hello "world"`})
	want := `{k="hello \"world\""}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
