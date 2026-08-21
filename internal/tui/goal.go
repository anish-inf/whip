package tui

import (
	"fmt"
	"strings"
)

// ponytail: fixed cap; make configurable when someone actually hits it
const maxGoalRounds = 20

const goalMetToken = "GOAL_MET"

// goalContinuePrompt is sent after each completed turn while a goal is set.
// Continuing is the default — stopping requires the explicit token — which is
// what prevents the early-termination failure mode.
func goalContinuePrompt(goal string) string {
	return fmt.Sprintf(`[goal check] The session goal is:
%s

If the goal is FULLY accomplished and you have VERIFIED it with your tools (builds pass, tests pass, behavior confirmed), reply starting with exactly %s followed by a one-line summary.

Otherwise do not mention %s — keep working toward the goal right now with your tools. If any part is incomplete, unverified, or you are unsure, that means keep working. Do not stop to ask questions; make reasonable assumptions and proceed.`, goal, goalMetToken, goalMetToken)
}

// goalMet reports whether the model explicitly declared the goal done.
func goalMet(final string) bool {
	return strings.HasPrefix(strings.TrimSpace(final), goalMetToken)
}
