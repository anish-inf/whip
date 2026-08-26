package tui

import (
	"strings"

	"github.com/context-labs/whip/internal/computer"
)

// computerConsent is the computer-use per-app consent hook. The tool calls
// it from a tool goroutine when an app needs approval; we can't show a modal
// mid-turn, so the safest v1 behavior is: deny silently (return false) and
// let the ApprovalNeeded error text tell the model to surface the ask in its
// reply. The user re-runs with the app allowed, or adds it to
// computer.allow. A real interactive approve-y/n prompt is the follow-up
// (it needs a turn-pausing modal we don't have yet).
func (m *model) computerConsent(app string) bool {
	m.append(dimStyle.Render("◎ computer_exec wants to drive " + app + " — approve with `computer.allow` in config or re-run after granting"))
	return false
}

// ensure computer is referenced (policy install lives in tui.go init)
var (
	_ = computer.ApprovalNeeded{}
	_ = strings.Contains
)
