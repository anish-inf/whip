package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/llm"
)

// sseTextServer serves every streaming chat request with a fixed text
// response — enough for a background subagent's Turn to complete.
func sseTextServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}]}`+"\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// tasksModel builds a headless model whose agent can start background tasks
// against a stub server (no tea.Program: prog.Send paths are nil-guarded).
func tasksModel(url string) *model {
	m := &model{
		input:    newInput(),
		agent:    agent.New(llm.New(url, "k"), "m", 100, "sys"),
		queueSel: -1, // not navigating the queue (the zero value would arm esc's queue branch)
	}
	m.width, m.height = 80, 30
	m.input.SetWidth(78)
	return m
}

// mkKey builds a KeyMsg from a name ("enter", "esc", "ctrl+t", "up", "down").
func mkKey(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+t":
		return tea.KeyMsg{Type: tea.KeyCtrlT}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// waitSettled blocks until the task's Done channel closes.
func waitSettled(t *testing.T, task *agent.BackgroundTask) {
	t.Helper()
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
}

func TestTasksDockHiddenWithoutTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	if got := m.tasksDock(); got != "" {
		t.Fatalf("dock should be empty without tasks, got %q", got)
	}
}

func TestTasksDockListsTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe grafana", "look around")
	defer m.agent.Tasks().Cancel(task.ID)

	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, task.ID) || !strings.Contains(dock, "probe grafana") {
		t.Fatalf("dock should list the running task, got %q", dock)
	}
	if !strings.Contains(dock, "⏳") {
		t.Fatalf("running task should show the spinner icon, got %q", dock)
	}
}

func TestCtrlTFocusesDockAndArrowsSelect(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	t1 := m.agent.StartBackground(t.Context(), "first", "p")
	defer m.agent.Tasks().Cancel(t1.ID)
	t2 := m.agent.StartBackground(t.Context(), "second", "p")
	defer m.agent.Tasks().Cancel(t2.ID)

	m.key(mkKey("ctrl+t"))
	if !m.tasksFocus {
		t.Fatal("ctrl+t should focus the dock")
	}
	if m.taskSel != 0 {
		t.Fatalf("selection should start on the newest task, got %d", m.taskSel)
	}
	m.key(mkKey("down"))
	if m.taskSel != 1 {
		t.Fatalf("↓ should move the selection down, got %d", m.taskSel)
	}
	m.key(mkKey("up"))
	if m.taskSel != 0 {
		t.Fatalf("↑ should move the selection back up, got %d", m.taskSel)
	}
	m.key(mkKey("esc"))
	if m.tasksFocus {
		t.Fatal("esc should unfocus the dock")
	}
}

func TestEnterOpensTaskViewAndEscBacksOut(t *testing.T) {
	srv := sseTextServer(t, "report-body")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "find things")
	defer m.agent.Tasks().Cancel(task.ID)

	m.key(mkKey("ctrl+t"))
	m.key(mkKey("enter"))
	if m.taskVP == nil || m.taskVP.id != task.ID {
		t.Fatalf("enter should open the selected task, got %+v", m.taskVP)
	}
	body := stripAll(m.taskViewView())
	if !strings.Contains(body, "probe") || !strings.Contains(body, "find things") {
		t.Fatalf("task view should show description and prompt, got %q", body)
	}
	if !strings.Contains(m.View(), "esc back") {
		t.Fatal("the open task view should render the back hint")
	}
	m.key(mkKey("esc"))
	if m.taskVP != nil {
		t.Fatal("esc should close the task view")
	}
	if !m.tasksFocus {
		t.Fatal("esc from a task view should land on the focused dock")
	}
	m.key(mkKey("esc"))
	if m.tasksFocus {
		t.Fatal("second esc should return to the main thread")
	}
}

func TestSettledTaskViewShowsReport(t *testing.T) {
	srv := sseTextServer(t, "the final report")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "p")
	waitSettled(t, task)

	m.openTask(task.ID)
	if m.taskVP.live {
		t.Fatal("a settled task's view should not subscribe to events")
	}
	if !strings.Contains(stripAll(m.taskViewView()), "the final report") {
		t.Fatalf("settled task view should render the report, got %q", stripAll(m.taskViewView()))
	}
	if m.agent.Tasks().Subscribe(task.ID, agent.Events{}) {
		t.Fatal("subscribing a settled task should fail")
	}
}

func TestSlashTasksFocusesDockAndOpensByID(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "p")
	defer m.agent.Tasks().Cancel(task.ID)

	m.command("/tasks")
	if !m.tasksFocus {
		t.Fatal("bare /tasks should focus the dock")
	}
	m.command("/tasks " + task.ID)
	if m.taskVP == nil || m.taskVP.id != task.ID {
		t.Fatalf("/tasks <id> should open that task's view, got %+v", m.taskVP)
	}
}
