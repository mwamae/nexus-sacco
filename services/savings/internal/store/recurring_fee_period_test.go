package store

import (
	"testing"
	"time"
)

func TestPeriodLabel(t *testing.T) {
	march := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	july := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		freq, want string
		when       time.Time
	}{
		{"monthly", "2026-03", march},
		{"monthly", "2026-07", july},
		{"quarterly", "2026-Q1", march},
		{"quarterly", "2026-Q3", july},
		{"annual", "2026", march},
		{"annual", "2026", july},
	}
	for _, c := range cases {
		got := PeriodLabel(c.freq, c.when)
		if got != c.want {
			t.Errorf("PeriodLabel(%q, %v) = %q, want %q", c.freq, c.when, got, c.want)
		}
	}
}

func TestNextRunFromAnchor(t *testing.T) {
	anchor := time.Date(2026, 5, 30, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		freq string
		want time.Time
	}{
		{"weekly", time.Date(2026, 6, 6, 6, 0, 0, 0, time.UTC)},
		{"biweekly", time.Date(2026, 6, 13, 6, 0, 0, 0, time.UTC)},
		{"monthly", time.Date(2026, 6, 30, 6, 0, 0, 0, time.UTC)},
		{"quarterly", time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := nextRunFromAnchor(anchor, c.freq)
		if err != nil {
			t.Errorf("nextRunFromAnchor(%v, %q): unexpected err %v", anchor, c.freq, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("nextRunFromAnchor(%v, %q) = %v, want %v", anchor, c.freq, got, c.want)
		}
	}
	if _, err := nextRunFromAnchor(anchor, "fortnightly"); err == nil {
		t.Errorf("nextRunFromAnchor: expected error for unknown frequency")
	}
}
