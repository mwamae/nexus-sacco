// Tests for SASRA helpers — column layout integrity + the
// classification function. End-to-end CSV generation is exercised
// in the integration suite (needs Postgres); these unit tests pin
// the static-data invariants.

package handler

import (
	"encoding/json"
	"testing"
)

func TestSASRADefaultColumns_Decodable(t *testing.T) {
	layout := &SASRAColumnLayout{}
	if err := json.Unmarshal(defaultSASRAColumnsJSON, layout); err != nil {
		t.Fatalf("default sasra_columns.json must be decodable: %v", err)
	}
	if len(layout.Columns) == 0 {
		t.Error("default layout must have columns")
	}
	if len(layout.HeaderMetadata) == 0 {
		t.Error("default layout must define header metadata")
	}
	for _, k := range []string{"loan_no", "member_no", "principal_outstanding", "total_outstanding", "days_in_arrears", "classification_label", "provision_required"} {
		found := false
		for _, c := range layout.Columns {
			if c.Key == k {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default layout missing required column %q", k)
		}
	}
}

func TestSASRAClassify_DPDBuckets(t *testing.T) {
	layout := &SASRAColumnLayout{}
	if err := json.Unmarshal(defaultSASRAColumnsJSON, layout); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		dpd       int
		status    string
		wantLabel string
		wantCode  int
	}{
		{0, "active", "normal", 1},
		{15, "active", "normal", 1},
		{31, "in_arrears", "watch", 2},
		{60, "in_arrears", "watch", 2},
		{61, "in_arrears", "substandard", 3},
		{180, "in_arrears", "substandard", 3},
		{181, "in_arrears", "doubtful", 4},
		{360, "in_arrears", "doubtful", 4},
		{361, "in_arrears", "loss", 5},
		{99999, "in_arrears", "loss", 5},
		// status=written_off short-circuits to loss regardless of dpd.
		{0, "written_off", "loss", 5},
	}
	for _, tc := range cases {
		code, label, _ := classify(tc.dpd, tc.status, layout.ClassificationThresholds)
		if label != tc.wantLabel || code != tc.wantCode {
			t.Errorf("classify(dpd=%d status=%s) = (%d, %s); want (%d, %s)",
				tc.dpd, tc.status, code, label, tc.wantCode, tc.wantLabel)
		}
	}
}

func TestValidQuarter(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"2026-Q1", true}, {"2026-Q4", true},
		{"2026-Q5", false}, {"2026-Q0", false},
		{"2026-q1", false}, // lowercase q rejected
		{"26-Q1", false}, {"2026Q1", false}, {"", false},
	}
	for _, tc := range cases {
		if got := validQuarter(tc.s); got != tc.want {
			t.Errorf("validQuarter(%q) = %v; want %v", tc.s, got, tc.want)
		}
	}
}

func TestPeriodBounds_QuarterDates(t *testing.T) {
	from, to, err := periodBounds("2026-Q1")
	if err != nil {
		t.Fatal(err)
	}
	if from.Format("2006-01-02") != "2026-01-01" {
		t.Errorf("Q1 from: got %s, want 2026-01-01", from.Format("2006-01-02"))
	}
	if to.Format("2006-01-02") != "2026-03-31" {
		t.Errorf("Q1 to: got %s, want 2026-03-31", to.Format("2006-01-02"))
	}
}
