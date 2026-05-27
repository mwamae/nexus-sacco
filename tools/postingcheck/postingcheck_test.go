package postingcheck

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	// The testdata layout mirrors the real repo's
	// services/<svc>/internal/<pkg>/... path so the analyzer's
	// path-regex filters fire.
	analysistest.Run(t, analysistest.TestData(), Analyzer,
		// handler/ — rules 1, 2, 3, 4, 5
		"services/savings/internal/handler",
		// non-handler store outside sanctioned writers — rule 6
		// (R-OPEN-2). Case study is member/internal/store/application_store.go.
		"services/member/internal/store",
	)
}
