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
	"github.com/charmbracelet/lipgloss"

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

// A settled-but-unseen task still occupies a dock row: the strip is the
// record of every background subagent, not just the in-flight ones.
func TestTasksDockShowsSettledTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "finished probe", "p")
	waitSettled(t, task)

	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "✓") || !strings.Contains(dock, "finished probe") {
		t.Fatalf("dock should show the settled task with a ✓, got %q", dock)
	}
	if !strings.Contains(dock, "done") {
		t.Fatalf("settled row should name its status, got %q", dock)
	}
}

// The dock eats into the transcript's height exactly by its rendered rows
// (plus the blank separator), so it never overlaps or underflows the layout.
// Go through Update: its deferred layout() always runs, whereas a direct
// layout() call skips the resize when the dims coincidentally match.
func TestLayoutReservesDockHeight(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.Update(mkWinSize(80, 30))
	base := m.vp.Height

	task := m.agent.StartBackground(t.Context(), "probe", "p")
	defer m.agent.Tasks().Cancel(task.ID)
	tm, _ := m.Update(taskUpdateMsg{}) // force a layout pass with the task visible
	m = tm.(*model)
	dockRows := lipgloss.Height(m.tasksDock())
	if dockRows != 1 {
		t.Fatalf("one unfocused task should be one dock row, got %d", dockRows)
	}
	if m.vp.Height != base-dockRows-1 {
		t.Fatalf("viewport should shrink by dock+separator: base=%d now=%d dock=%d", base, m.vp.Height, dockRows)
	}
	// and the dock renders on its own row above the input, not glued to it
	v := stripAll(m.View())
	di := strings.Index(v, "probe")
	ii := strings.Index(v, "Ask loopy")
	if di < 0 || ii < 0 || di > ii {
		t.Fatalf("dock must render above the input: dock@%d input@%d\n%s", di, ii, v)
	}
	if m.dockTop() < 0 || m.dockTop() >= m.height {
		t.Fatalf("dockTop out of screen: %d (height %d)", m.dockTop(), m.height)
	}

	m.tasksFocus = true // the focused hint row costs one more
	tm, _ = m.Update(taskUpdateMsg{})
	m = tm.(*model)
	if m.vp.Height != base-dockRows-2 {
		t.Fatalf("focused dock should cost the hint row too: %d vs %d", m.vp.Height, base-dockRows-2)
	}
}

// ctrl+t with no tasks is a no-op (nothing to focus).
func TestCtrlTNoopWithoutTasks(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	m.key(mkKey("ctrl+t"))
	if m.tasksFocus {
		t.Fatal("ctrl+t should not focus an empty dock")
	}
}

// With more tasks than the strip fits, the dock scrolls to keep the
// selection visible and advertises the hidden remainder.
func TestDockScrollsWithSelection(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	// match descriptions to index: task IDs come from a global counter, so
	// tests can't rely on a fresh numbering
	var tasks []*agent.BackgroundTask
	for i := 0; i < 8; i++ {
		tk := m.agent.StartBackground(t.Context(), fmt.Sprintf("probe-%d", i), "p")
		defer m.agent.Tasks().Cancel(tk.ID)
		tasks = append(tasks, tk)
	}

	m.tasksFocus = true
	m.taskSel = 6 // beyond the visible window
	if got := lipgloss.Height(m.tasksDock()); got > tasksDockHeight {
		t.Fatalf("dock must stay within %d rows, rendered %d", tasksDockHeight, got)
	}
	dock := stripAll(m.tasksDock())
	if !strings.Contains(dock, "probe-1") { // newest-first: sel 6 = probe-1
		t.Fatalf("scrolled dock should keep the selection visible, got %q", dock)
	}
	if !strings.Contains(dock, "more") {
		t.Fatalf("dock should advertise hidden rows, got %q", dock)
	}
	// the newest task scrolled out of view
	if strings.Contains(dock, "probe-7") {
		t.Fatalf("row above the window should be scrolled out, got %q", dock)
	}
}

// A click on a dock row opens that task's view; the wheel moves the
// selection through the strip.
func TestDockMouseClickOpensTask(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	t1 := m.agent.StartBackground(t.Context(), "first", "p")
	defer m.agent.Tasks().Cancel(t1.ID)
	t2 := m.agent.StartBackground(t.Context(), "second", "p")
	defer m.agent.Tasks().Cancel(t2.ID)

	m.layout()
	top := m.dockTop()
	if n := len(m.dockTasks()); n != 2 {
		t.Fatalf("want 2 dock tasks, got %d", n)
	}
	// newest first: row 0 is t2
	tm, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: top})
	m = tm.(*model)
	if m.taskVP == nil || m.taskVP.id != t2.ID {
		t.Fatalf("click on row 0 should open the newest task, got %+v", m.taskVP)
	}

	// back out, then wheel down to select the older task
	m.taskVP = nil
	m.tasksFocus = false
	tm, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, X: 5, Y: top})
	m = tm.(*model)
	if !m.tasksFocus || m.taskSel != 1 {
		t.Fatalf("wheel should focus the dock and move the selection: focus=%v sel=%d", m.tasksFocus, m.taskSel)
	}
	// focused: clicking a task row selects-and-opens that row (row 1 = t1;
	// row 0 is the hint row and maps to the current selection, t2)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: m.dockTop() + 1})
	m = tm.(*model)
	if m.taskVP == nil || m.taskVP.id != t1.ID {
		t.Fatalf("click on dock row 1 should open the older task, got id=%v", m.taskVP)
	}
}

// Live events from the subagent append into the open view's transcript.
func TestTaskEventAppendsToOpenView(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "p")
	defer m.agent.Tasks().Cancel(task.ID)

	m.openTask(task.ID)
	tm, _ := m.Update(taskEventMsg{id: task.ID, kind: 0, s: "streamed text"})
	m = tm.(*model)
	tm, _ = m.Update(taskEventMsg{id: task.ID, kind: 1, s: "bash", s2: `{"command":"ls"}`})
	m = tm.(*model)
	tm, _ = m.Update(taskEventMsg{id: task.ID, kind: 2, s: "bash", s2: "file1\nfile2"})
	m = tm.(*model)

	buf := m.taskVP.buf.String()
	for _, want := range []string{"streamed text", "⚒ bash", "file1"} {
		if !strings.Contains(stripAll(buf), want) {
			t.Fatalf("open view transcript missing %q: %q", want, stripAll(buf))
		}
	}
	// events for a different task are ignored
	tm, _ = m.Update(taskEventMsg{id: "task-999", kind: 0, s: "stray"})
	m = tm.(*model)
	if strings.Contains(m.taskVP.buf.String(), "stray") {
		t.Fatal("events for other tasks must not leak into the open view")
	}
}

// When the open task settles, the view swaps the live stream for the stored
// final report (taskUpdateMsg reseeds it).
func TestOpenTaskViewRefreshesOnSettle(t *testing.T) {
	srv := sseTextServer(t, "the streamed final report")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "p")

	m.openTask(task.ID)
	if !m.taskVP.live {
		t.Fatal("view of a running task should be live")
	}
	waitSettled(t, task)
	tm, _ := m.Update(taskUpdateMsg{})
	m = tm.(*model)

	if m.taskVP == nil || m.taskVP.live {
		t.Fatal("settled task's view should no longer be live")
	}
	if !strings.Contains(stripAll(m.taskVP.buf.String()), "the streamed final report") {
		t.Fatalf("refreshed view should show the report, got %q", stripAll(m.taskVP.buf.String()))
	}
	head := stripAll(m.taskViewView())
	if !strings.Contains(head, "(done)") {
		t.Fatalf("header should show the settled status, got %q", head)
	}
}

// x in an open view cancels a running task.
func TestTaskViewXCancels(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "p")

	m.openTask(task.ID)
	m.taskViewKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	waitSettled(t, task)
	snap, _ := m.agent.Tasks().Get(task.ID)
	if snap.Status != agent.TaskCancelled {
		t.Fatalf("x should cancel the running task, got %s", snap.Status)
	}
}

// ctrl+t inside an open view returns to the focused dock (not the input).
func TestCtrlTFromTaskViewLandsOnDock(t *testing.T) {
	srv := sseTextServer(t, "ok")
	defer srv.Close()
	m := tasksModel(srv.URL)
	task := m.agent.StartBackground(t.Context(), "probe", "p")
	defer m.agent.Tasks().Cancel(task.ID)

	m.openTask(task.ID)
	m.key(mkKey("ctrl+t"))
	if m.taskVP != nil {
		t.Fatal("ctrl+t should close the task view")
	}
	if !m.tasksFocus {
		t.Fatal("ctrl+t from a task view should land on the focused dock")
	}
}
