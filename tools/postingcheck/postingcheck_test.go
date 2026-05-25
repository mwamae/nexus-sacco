package postingcheck

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	// The testdata layout mirrors the real repo's
	// services/<svc>/internal/handler/... path so the analyzer's
	// path-regex filter fires.
	analysistest.Run(t, analysistest.TestData(), Analyzer,
		"services/savings/internal/handler")
}
