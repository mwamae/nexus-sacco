// Package classification computes SASRA classification + IFRS 9 stage
// for a loan given its DPD, tenant policy, and a few qualitative
// signals (restructured / forborne flag).
//
// The package is intentionally pure: no DB, no HTTP, no clock. All
// inputs are passed in. This keeps the rules cheap to unit-test and
// makes the rule set legible at a glance — every change here is an
// auditable change to the classification policy.
//
// SASRA bands (CBK SACCO Prudential 0419):
//   performing  : DPD < sasra_watch_dpd          (default 1)
//   watch       : DPD in [watch, substandard)    (default 1..30)
//   substandard : DPD in [substandard, doubtful) (default 31..90)
//   doubtful    : DPD in [doubtful, loss)        (default 91..180)
//   loss        : DPD >= loss                    (default 181+)
//
// IFRS 9 stages (independent of SASRA — different thresholds):
//   Stage 1 : DPD < ifrs9_stage2_dpd            (default <31)
//   Stage 2 : DPD in [stage2, stage3)           (default 31..90)
//   Stage 3 : DPD >= ifrs9_stage3_dpd            (default 91+)
//
// Restructured-loan SICR override (IFRS 9 paragraph B5.5.20):
//   restructured AND DPD == 0  → at least Stage 2
//   restructured AND DPD > 30  → Stage 3 (credit-impaired)
// SASRA classification is NOT lifted by the restructured flag —
// SASRA bands are pure DPD.
package classification

// SASRAClass is one of the five SASRA prudential classes.
type SASRAClass string

const (
	SASRAPerforming  SASRAClass = "performing"
	SASRAWatch       SASRAClass = "watch"
	SASRASubstandard SASRAClass = "substandard"
	SASRADoubtful    SASRAClass = "doubtful"
	SASRALoss        SASRAClass = "loss"
)

// IFRS9Stage is the IFRS 9 ECL stage (1, 2, or 3).
type IFRS9Stage int

const (
	Stage1 IFRS9Stage = 1
	Stage2 IFRS9Stage = 2
	Stage3 IFRS9Stage = 3
)

// Thresholds are the per-tenant DPD cut-offs. All values are days.
// The structure mirrors the columns added to tenant_operations in
// migration 0039 (savings) — see classifier_test.go for the default
// CBK-aligned values.
type Thresholds struct {
	SASRAWatchDPD      int // >= this DPD is at least 'watch'
	SASRASubstandardDPD int // >= this DPD is at least 'substandard'
	SASRADoubtfulDPD   int // >= this DPD is at least 'doubtful'
	SASRALossDPD       int // >= this DPD is 'loss'
	IFRS9Stage2DPD     int // >= this DPD is at least Stage 2
	IFRS9Stage3DPD     int // >= this DPD is Stage 3
}

// DefaultThresholds returns the standard CBK-aligned thresholds.
// Used as a fallback and for the test suite — runtime callers should
// pass the tenant's actual operations row.
func DefaultThresholds() Thresholds {
	return Thresholds{
		SASRAWatchDPD:       1,
		SASRASubstandardDPD: 31,
		SASRADoubtfulDPD:    91,
		SASRALossDPD:        181,
		IFRS9Stage2DPD:      31,
		IFRS9Stage3DPD:      91,
	}
}

// Input bundles the per-loan signals the classifier reads.
type Input struct {
	DPD          int  // days past due (0 if current)
	Restructured bool // forborne / restructured within the watch period
}

// Result is what the classifier returns.
type Result struct {
	SASRA SASRAClass
	Stage IFRS9Stage
}

// Classify applies the rules described in the package doc.
//
// Negative DPD is treated as zero (a loan that's paid ahead of schedule
// is performing — never substandard). Thresholds must be in ascending
// order (watch < substandard < doubtful < loss; stage2 < stage3);
// the function does not validate this — callers should ensure their
// tenant policy is well-ordered.
func Classify(in Input, t Thresholds) Result {
	dpd := in.DPD
	if dpd < 0 {
		dpd = 0
	}

	return Result{
		SASRA: sasraFor(dpd, t),
		Stage: stageFor(dpd, in.Restructured, t),
	}
}

func sasraFor(dpd int, t Thresholds) SASRAClass {
	switch {
	case dpd >= t.SASRALossDPD:
		return SASRALoss
	case dpd >= t.SASRADoubtfulDPD:
		return SASRADoubtful
	case dpd >= t.SASRASubstandardDPD:
		return SASRASubstandard
	case dpd >= t.SASRAWatchDPD:
		return SASRAWatch
	default:
		return SASRAPerforming
	}
}

func stageFor(dpd int, restructured bool, t Thresholds) IFRS9Stage {
	// SICR override for restructured loans. Even with DPD=0 a forborne
	// loan represents a significant increase in credit risk (IFRS 9
	// B5.5.20), and DPD>30 on a forborne loan is presumptively credit-
	// impaired.
	if restructured && dpd > 30 {
		return Stage3
	}

	base := Stage1
	switch {
	case dpd >= t.IFRS9Stage3DPD:
		base = Stage3
	case dpd >= t.IFRS9Stage2DPD:
		base = Stage2
	}

	if restructured && base == Stage1 {
		return Stage2
	}
	return base
}
