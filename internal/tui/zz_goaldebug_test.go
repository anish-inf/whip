package tui

import (
	"fmt"
	"testing"
)

func TestZDebugGoalMetPath(t *testing.T) {
	final := "Verified on this run: clean tree.\n\nGOAL_MET — Native Go browser subsystem shipped."
	fmt.Printf("goalMet(%q) = %v\n", final[:40], goalMet(final))
	m := &model{goal: "x", goalRounds: 1}
	tm, cmd := m.Update(turnDoneMsg{final: final})
	mm := tm.(*model)
	fmt.Printf("after turnDone: goal=%q cmd==nil: %v\n", mm.goal, cmd == nil)
}
