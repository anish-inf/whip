// whipvet runs whip's custom go/analysis analyzers as a standalone vet-style
// checker, wired into CI alongside `go vet` and golangci-lint. It exists
// because the deadlock class it guards against (a synchronous tea.Program.Send
// reachable from the TUI event loop) has no off-the-shelf linter.
//
// Usage: go run ./cmd/whipvet ./...
package main

import (
	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/context-labs/whip/internal/analysis/uilock"
)

func main() {
	multichecker.Main(uilock.Analyzer)
}
