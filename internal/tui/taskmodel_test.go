package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/config"
)

func taskCfg(url string) *config.Config {
	return &config.Config{
		DefaultModel: "m",
		Providers:    map[string]config.Provider{"p": {BaseURL: url, APIKey: "k"}},
		Models: map[string]config.Model{
			"m":                     {Providers: []string{"p"}},
			config.DefaultTaskModel: {Providers: []string{"p"}, Context: 384000},
		},
	}
}

// The built-in default task model resolves when the config routes it.
func TestTaskDefaultForResolvesDefault(t *testing.T) {
	o, err := TaskDefaultFor(taskCfg("http://x"))
	if err != nil || o.Client == nil || o.Model != config.DefaultTaskModel {
		t.Fatalf("default should resolve: %+v, %v", o, err)
	}
	if o.ContextLimit != 384000 {
		t.Fatalf("context should carry over, got %d", o.ContextLimit)
	}
}

// A missing default is a silent fallback; an explicit bad pick is an error.
func TestTaskDefaultForFallbacks(t *testing.T) {
	cfg := taskCfg("http://x")
	delete(cfg.Models, config.DefaultTaskModel)
	o, err := TaskDefaultFor(cfg)
	if err != nil || o.Client != nil {
		t.Fatalf("missing default must silently fall back, got %+v, %v", o, err)
	}
	cfg.TaskModel = "nope"
	if _, err := TaskDefaultFor(cfg); err == nil {
		t.Fatal("an explicit taskModel that fails to resolve should error")
	}
}

// An openrouter-style catalog id ("vendor/name") satisfies the default via
// the suffix scan when the bare name isn't routed in config.
func TestTaskDefaultForCatalogSuffix(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "m",
		Providers:    map[string]config.Provider{"openrouter": {BaseURL: "http://x", APIKey: "k"}},
		Models:       map[string]config.Model{"m": {Providers: []string{"openrouter"}}},
	}
	if err := config.SaveCatalogs(map[string]config.Catalog{
		"openrouter": {FetchedAt: time.Now(), Models: []config.ModelInfoLite{
			{ID: "deepseek/" + config.DefaultTaskModel, ContextLength: 128000},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	defer config.SaveCatalogs(map[string]config.Catalog{})
	o, err := TaskDefaultFor(cfg)
	if err != nil || o.Client == nil || o.Model != "deepseek/"+config.DefaultTaskModel {
		t.Fatalf("suffix scan should resolve the prefixed catalog id: %+v, %v", o, err)
	}
}

// /task spawns a background subagent with an auto description; bare /task
// prints usage; -m with an unresolvable model refuses to spawn.
func TestTaskCommandSpawns(t *testing.T) {
	srv := sseTextServer(t, "done")
	defer srv.Close()
	m := tasksModel(srv.URL)

	m.taskCommand("poke around the repo and report what you find")
	tasks := m.agent.Tasks().List()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Description != "poke around the repo and report what you…" {
		t.Fatalf("auto description: %q", tasks[0].Description)
	}
	waitSettled(t, &tasks[0])

	before := len(m.blocks)
	m.taskCommand("")
	if len(m.blocks) != before+1 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "usage:") {
		t.Fatal("bare /task should print usage")
	}

	m.taskCommand("-m nope do a thing")
	if len(m.agent.Tasks().List()) != 1 {
		t.Fatal("an unresolvable -m model must not spawn a task")
	}
}

// Typing in an open task view steers a running task and starts a follow-up
// turn on a settled one.
func TestTaskViewChat(t *testing.T) {
	srv := sseTextServer(t, "report")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	waitSettled(t, task)

	m.openTask(task.ID)
	tv := m.taskVP
	tv.input.SetValue("what about the tests?")
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(tv.buf.String(), "what about the tests?") {
		t.Fatal("the sent message should land in the pane transcript")
	}
	if !tv.busy {
		t.Fatal("a follow-up turn should be in flight")
	}
	if tv.input.Value() != "" {
		t.Fatal("the input should clear on send")
	}
	// second send while busy is refused politely, no second turn
	tv.input.SetValue("more")
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(tv.buf.String(), "still replying") {
		t.Fatal("sends while busy should be refused in the pane")
	}
	tv.followCancel() // don't leak the follow-up goroutine's turn
}

// A restored task's view is read-only: no input, keys scroll.
func TestTaskViewRestoredReadOnly(t *testing.T) {
	srv := sseTextServer(t, "x")
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.agent.RestoreTask(agent.BackgroundTask{ID: "task-77", Description: "old", Status: agent.TaskDone, Report: "r", Restored: true})

	m.openTask("task-77")
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.taskVP.input.Value() != "" {
		t.Fatal("restored tasks must not accept chat input")
	}
	if !strings.Contains(m.taskViewView(), "read-only") {
		t.Fatal("the view should say it is read-only")
	}
}

// The dock renders below the input: ↓ on an empty input moves focus into it,
// typing hands focus back implicitly (so enter submits instead of opening a
// task), and ↓ with a draft in the input never steals focus.
func TestDownArrowFocusesDockBelowInput(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground("probe", "p", agent.SubModel{})
	defer m.agent.Tasks().Cancel(task.ID)

	m.key(mkKey("down"))
	if !m.tasksFocus || m.taskSel != 0 {
		t.Fatalf("↓ on empty input should focus the dock at its top row, focus=%v sel=%d", m.tasksFocus, m.taskSel)
	}
	m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	if m.tasksFocus {
		t.Fatal("typing should hand focus back to the input")
	}
	if m.input.Value() != "h" {
		t.Fatalf("the typed rune should land in the input, got %q", m.input.Value())
	}
	m.key(mkKey("down"))
	if m.tasksFocus {
		t.Fatal("↓ with a draft in the input must not steal focus")
	}
}
