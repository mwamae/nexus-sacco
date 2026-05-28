package classification

import "testing"

// Exhaustive table over the CBK-default thresholds. Every boundary is
// exercised (one DPD below, exactly at, one above) for SASRA and
// IFRS 9 independently, plus the restructured-loan SICR matrix.

func TestDefaultThresholds_CBKAligned(t *testing.T) {
	got := DefaultThresholds()
	want := Thresholds{
		SASRAWatchDPD: 1, SASRASubstandardDPD: 31,
		SASRADoubtfulDPD: 91, SASRALossDPD: 181,
		IFRS9Stage2DPD: 31, IFRS9Stage3DPD: 91,
	}
	if got != want {
		t.Fatalf("default thresholds drifted from CBK 0419 baseline: got %+v want %+v", got, want)
	}
}

func TestClassify_SASRA_Boundaries(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		dpd  int
		want SASRAClass
	}{
		{-5, SASRAPerforming}, // negative treated as 0
		{0, SASRAPerforming},
		{1, SASRAWatch},     // first DPD bucket
		{30, SASRAWatch},    // last day of watch
		{31, SASRASubstandard},
		{90, SASRASubstandard},
		{91, SASRADoubtful},
		{180, SASRADoubtful},
		{181, SASRALoss},
		{5000, SASRALoss},
	}
	for _, c := range cases {
		got := Classify(Input{DPD: c.dpd}, th)
		if got.SASRA != c.want {
			t.Errorf("DPD=%d: SASRA got %s want %s", c.dpd, got.SASRA, c.want)
		}
	}
}

func TestClassify_IFRS9_Boundaries(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		dpd  int
		want IFRS9Stage
	}{
		{0, Stage1},
		{30, Stage1},
		{31, Stage2},
		{90, Stage2},
		{91, Stage3},
		{365, Stage3},
	}
	for _, c := range cases {
		got := Classify(Input{DPD: c.dpd}, th)
		if got.Stage != c.want {
			t.Errorf("DPD=%d: IFRS9 stage got %d want %d", c.dpd, got.Stage, c.want)
		}
	}
}

func TestClassify_RestructuredSICR(t *testing.T) {
	th := DefaultThresholds()
	cases := []struct {
		name      string
		dpd       int
		want      IFRS9Stage
		wantSasra SASRAClass // SASRA must NOT be lifted by restructuring
	}{
		{"current restructured → at least stage 2", 0, Stage2, SASRAPerforming},
		{"watch + restructured → stage 2", 15, Stage2, SASRAWatch},
		{"30 DPD restructured still stage 2", 30, Stage2, SASRAWatch},
		{"31 DPD restructured → SICR override forces stage 3", 31, Stage3, SASRASubstandard},
		{"90 DPD restructured → stage 3", 90, Stage3, SASRASubstandard},
		{"181 DPD restructured → still loss/stage3", 181, Stage3, SASRALoss},
	}
	for _, c := range cases {
		got := Classify(Input{DPD: c.dpd, Restructured: true}, th)
		if got.Stage != c.want {
			t.Errorf("%s: stage got %d want %d", c.name, got.Stage, c.want)
		}
		if got.SASRA != c.wantSasra {
			t.Errorf("%s: SASRA got %s want %s (restructuring must not change SASRA)", c.name, got.SASRA, c.wantSasra)
		}
	}
}

// A tenant override (e.g. stricter substandard threshold of 14 days
// instead of 31) should be honored.
func TestClassify_TenantOverride(t *testing.T) {
	strict := Thresholds{
		SASRAWatchDPD:       1,
		SASRASubstandardDPD: 14,
		SASRADoubtfulDPD:    60,
		SASRALossDPD:        120,
		IFRS9Stage2DPD:      14,
		IFRS9Stage3DPD:      60,
	}
	r := Classify(Input{DPD: 20}, strict)
	if r.SASRA != SASRASubstandard {
		t.Errorf("strict override: SASRA got %s want substandard", r.SASRA)
	}
	if r.Stage != Stage2 {
		t.Errorf("strict override: IFRS9 stage got %d want 2", r.Stage)
	}
}

// Sanity check: restructured + DPD=0 yields the milder of the two
// IFRS 9 effects (Stage 2, not Stage 3 — the >30 SICR doesn't fire).
func TestClassify_RestructuredButCurrent(t *testing.T) {
	r := Classify(Input{DPD: 0, Restructured: true}, DefaultThresholds())
	if r.Stage != Stage2 {
		t.Fatalf("restructured + DPD=0: want Stage 2 (SICR floor), got %d", r.Stage)
	}
	if r.SASRA != SASRAPerforming {
		t.Fatalf("restructured + DPD=0: SASRA still performing (DPD bands aren't lifted), got %s", r.SASRA)
	}
}

func TestClassify_NegativeDPD(t *testing.T) {
	r := Classify(Input{DPD: -100}, DefaultThresholds())
	if r.SASRA != SASRAPerforming || r.Stage != Stage1 {
		t.Errorf("negative DPD must be treated as current: got %+v", r)
	}
}
