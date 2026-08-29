package uilock

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// TestAnalyzer pins the rule against AST fixtures: a synchronous Send inside
// internal/tui is flagged; a detached (go …) Send and a //nolint:uilock Send
// are not; a package outside internal/tui is never scanned.
func TestAnalyzer(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, Analyzer, "internal/tui")
}
