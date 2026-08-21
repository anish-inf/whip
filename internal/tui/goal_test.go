package tui

import (
	"strings"
	"testing"
)

func TestGoalHelpers(t *testing.T) {
	p := goalContinuePrompt("ship the feature")
	if !strings.Contains(p, "ship the feature") || !strings.Contains(p, goalMetToken) {
		t.Fatalf("prompt: %q", p)
	}
	// stopping requires the explicit leading token
	if !goalMet("GOAL_MET — everything verified") {
		t.Fatal("leading token must count as met")
	}
	if !goalMet("\n  GOAL_MET done") {
		t.Fatal("leading whitespace tolerated")
	}
	for _, s := range []string{
		"I am making progress toward GOAL_MET soon", // mentioned, not leading
		"almost done",
		"",
	} {
		if goalMet(s) {
			t.Fatalf("%q must not count as met", s)
		}
	}
}
