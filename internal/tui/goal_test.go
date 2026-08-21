package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/context-labs/loopy/internal/config"
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

// lastBlock returns the last transcript block's text.
func lastBlock(m *model) string {
	if len(m.blocks) == 0 {
		return ""
	}
	return m.blocks[len(m.blocks)-1].text
}

func TestGoalMaxRoundsResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := modelCmdModel()

	if n := m.goalMaxRounds(); n != config.DefaultGoalMaxRounds {
		t.Fatalf("default should be %d, got %d", config.DefaultGoalMaxRounds, n)
	}
	m.cfg.GoalMaxRounds = 250
	if n := m.goalMaxRounds(); n != 250 {
		t.Fatalf("global config should win, got %d", n)
	}
	// project override beats the global default
	wd, _ := os.Getwd()
	if err := config.SetProjectGoalMaxRounds(wd, 42); err != nil {
		t.Fatal(err)
	}
	if n := m.goalMaxRounds(); n != 42 {
		t.Fatalf("project override should win, got %d", n)
	}
	if err := config.SetProjectGoalMaxRounds(wd, 0); err != nil {
		t.Fatal(err)
	}
}

func TestGoalRoundsCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := modelCmdModel()

	// bare reports the effective cap and source
	m.command("/goal rounds")
	if out := lastBlock(m); !strings.Contains(out, "100") || !strings.Contains(out, "built-in default") {
		t.Fatalf("bare report: %q", out)
	}
	// project override
	m.command("/goal rounds 42")
	if n := m.goalMaxRounds(); n != 42 {
		t.Fatalf("project override: %d", n)
	}
	if out := lastBlock(m); !strings.Contains(out, "this project") {
		t.Fatalf("project set message: %q", out)
	}
	// global default is set, but the project override still wins and says so
	m.command("/goal rounds 250 --global")
	if m.cfg.GoalMaxRounds != 250 {
		t.Fatalf("global not saved on cfg: %d", m.cfg.GoalMaxRounds)
	}
	if n := m.goalMaxRounds(); n != 42 {
		t.Fatalf("project should still win: %d", n)
	}
	if out := lastBlock(m); !strings.Contains(out, "overrides it with 42") {
		t.Fatalf("override note: %q", out)
	}
	// clearing the project override falls back to the global value
	m.command("/goal rounds default")
	if n := m.goalMaxRounds(); n != 250 {
		t.Fatalf("after clearing override should be 250, got %d", n)
	}
	// clearing the global falls back to the built-in
	m.command("/goal rounds default --global")
	if n := m.goalMaxRounds(); n != config.DefaultGoalMaxRounds {
		t.Fatalf("after clearing global should be %d, got %d", config.DefaultGoalMaxRounds, n)
	}
	// garbage is rejected without changing anything
	m.command("/goal rounds nope")
	if out := lastBlock(m); !strings.Contains(out, "positive number") {
		t.Fatalf("bad input: %q", out)
	}
}
