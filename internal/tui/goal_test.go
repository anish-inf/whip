package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/config"
	"github.com/context-labs/loopy/internal/llm"
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

// goalFromContextModel builds a headless model whose provider serves one
// canned chat-completion body (or status) to the goal-formulation call.
// m.prog stays nil, so the command's goroutine runs but never p.Sends; tests
// poll m.goal / m.busy directly.
func goalFromContextModel(t *testing.T, status int, body string) *model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	m := compactCmdModel()
	m.agent = agent.New(llm.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys")
	return m
}

func TestGoalFromContextPrompt(t *testing.T) {
	call := llm.ToolCall{}
	call.Function.Name = "bash"
	call.Function.Arguments = `{"cmd":"go test ./..."}`
	tail := []llm.Message{
		{Role: "user", Content: "make the tests green"},
		{Role: "assistant", Content: "I'll fix the flaky test and run go test.", ToolCalls: []llm.ToolCall{call}},
	}
	p := agent.BuildGoalFromContextPrompt(tail)
	for _, want := range []string{"make the tests green", "flaky test", "assistant called bash(", "ONLY the goal"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}

	// window selection: last two conversation messages, system excluded
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "old"},
		{Role: "assistant", Content: "older"},
		{Role: "user", Content: "recent ask"},
		{Role: "assistant", Content: "recent reply"},
	}
	got, err := agent.GoalFromContextMessages(msgs)
	if err != nil || len(got) != 2 || got[0].Content != "recent ask" || got[1].Content != "recent reply" {
		t.Fatalf("window: %v %v", got, err)
	}
	if _, err := agent.GoalFromContextMessages(msgs[:2]); err == nil {
		t.Fatal("two conversation messages required")
	}
}

func TestGoalFromContextSetsGoal(t *testing.T) {
	m := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"fix the flaky test and verify with go test"}}]}`)
	m.agent.Messages = []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "tests are flaky"},
		{Role: "assistant", Content: "I'll fix them."},
	}
	m.command("/goal-from-context") // headless: runs the formulation inline
	if m.goal != "fix the flaky test and verify with go test" {
		t.Fatalf("goal: %q", m.goal)
	}
	if m.busy {
		t.Fatal("busy must clear when the inline formulation returns")
	}
}

func TestGoalFromContextErrorLeavesGoalUntouched(t *testing.T) {
	m := goalFromContextModel(t, 500, `{"error":"boom"}`)
	m.agent.Messages = []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "tests are flaky"},
		{Role: "assistant", Content: "I'll fix them."},
	}
	m.command("/goal-from-context")
	if m.goal != "" {
		t.Fatalf("failed formulation must not set a goal, got %q", m.goal)
	}
	if m.busy {
		t.Fatal("busy must clear after a failed formulation")
	}
	if out := lastBlock(m); !strings.Contains(out, "goal-from-context failed") {
		t.Fatalf("expected a failure note, got %q", out)
	}
}

func TestGoalFromContextNeedsHistory(t *testing.T) {
	m := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"x"}}]}`)
	m.agent.Messages = []llm.Message{{Role: "system", Content: "sys"}}
	m.command("/goal-from-context")
	if m.busy {
		t.Fatal("no formulation call should start without history")
	}
	if out := lastBlock(m); !strings.Contains(out, "not enough context") {
		t.Fatalf("expected a needs-history note, got %q", out)
	}
}

// The live-path message handler: on failure it clears busy/cancel itself and
// must NOT submit (no trailing turnDoneMsg, so a paused goal's loop cannot
// re-engage); on success it sets the goal and hands busy to the new turn.
func TestGoalFromContextMsgHandler(t *testing.T) {
	m := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"x"}}]}`)
	m.busy = true
	m.cancel = func() {}
	m.goal = "paused old goal"
	m.goalRounds = 20 // exhausted
	tm, cmd := m.Update(goalFromContextMsg{err: errors.New("boom")})
	m = tm.(*model)
	if cmd != nil {
		t.Fatal("a failed formulation must not submit anything")
	}
	if m.busy || m.cancel != nil {
		t.Fatal("the msg handler must clear busy/cancel on failure")
	}
	if m.goal != "paused old goal" {
		t.Fatalf("old goal must survive untouched, got %q", m.goal)
	}
	if out := lastBlock(m); !strings.Contains(out, "goal-from-context failed") {
		t.Fatalf("expected a failure note, got %q", out)
	}

	// esc-cancel reads as an interrupt note, not an error
	m.busy, m.cancel = true, func() {}
	tm, _ = m.Update(goalFromContextMsg{err: context.Canceled})
	m = tm.(*model)
	if m.busy || !strings.Contains(lastBlock(m), "(interrupted)") {
		t.Fatalf("cancelled formulation should interrupt cleanly: busy=%v last=%q", m.busy, lastBlock(m))
	}

	// success: goal trimmed, set, and submitted — busy stays owned by the
	// new turn. The submit's turn goroutine p.Sends on a nil prog, so it
	// must run to completion (or fail) without touching the assertions.
	m2 := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"x"}}]}`)
	m2.busy = true
	m2.cancel = func() {}
	m2.agent.Messages = []llm.Message{{Role: "system", Content: "sys"}}
	tm2, cmd2 := m2.Update(goalFromContextMsg{goal: "  ship it  "})
	m2 = tm2.(*model)
	if cmd2 == nil {
		t.Fatal("a successful formulation must submit the goal (start the turn)")
	}
	if !m2.busy {
		t.Fatal("busy must stay set — it belongs to the submitted turn now")
	}
	if m2.goal != "ship it" {
		t.Fatalf("goal should be trimmed and set, got %q", m2.goal)
	}
	found := false
	for _, b := range m2.blocks {
		if strings.Contains(b.text, "◎ goal set: ship it") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a goal-set note in the transcript")
	}
}

func TestGoalFromContextBusyRefuses(t *testing.T) {
	m := goalFromContextModel(t, 200, `{"choices":[{"message":{"content":"x"}}]}`)
	m.agent.Messages = []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	m.busy = true
	m.command("/goal-from-context")
	if out := lastBlock(m); !strings.Contains(out, "busy") {
		t.Fatalf("expected a busy note, got %q", out)
	}
	if m.goal != "" {
		t.Fatal("busy refusal must not touch the goal")
	}
}
