package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/config"
)

// busyQueueModel builds a model that is busy with a populated queue.
func busyQueueModel(queue ...string) *model {
	m := &model{
		input:    newInput(),
		agent:    &agent.Agent{},
		busy:     true,
		queue:    queue,
		queueSel: -1,
	}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	return m
}

func press(t *testing.T, m *model, msg tea.KeyMsg) *model {
	t.Helper()
	tm, _ := m.key(msg)
	return tm.(*model)
}

func TestQueueNavigateAndSelect(t *testing.T) {
	m := busyQueueModel("first", "second", "third")

	// ↑ from the input selects the newest queued message
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.queueSel != 2 {
		t.Fatalf("↑ should select newest (index 2), got %d", m.queueSel)
	}
	// ↑ again moves older
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.queueSel != 1 {
		t.Fatalf("second ↑ should move to index 1, got %d", m.queueSel)
	}
	// ↓ moves back newer
	m = press(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.queueSel != 2 {
		t.Fatalf("↓ should move to index 2, got %d", m.queueSel)
	}
	// ↓ off the end deselects
	m = press(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.queueSel != -1 {
		t.Fatalf("↓ past the end should deselect, got %d", m.queueSel)
	}
}

func TestQueueDeleteSelected(t *testing.T) {
	m := busyQueueModel("first", "second", "third")

	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // select "third" (idx 2)
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // select "second" (idx 1)
	m = press(t, m, tea.KeyMsg{Type: tea.KeyDelete})
	if len(m.queue) != 2 || m.queue[0] != "first" || m.queue[1] != "third" {
		t.Fatalf("after deleting idx1: queue=%v", m.queue)
	}
	// selection clamps to the new last element
	if m.queueSel != 1 {
		t.Fatalf("selection should clamp to last index 1, got %d", m.queueSel)
	}
	// delete again removes "third", leaving "first"
	m = press(t, m, tea.KeyMsg{Type: tea.KeyDelete})
	if len(m.queue) != 1 || m.queue[0] != "first" {
		t.Fatalf("after second delete: queue=%v", m.queue)
	}
	if m.queueSel != 0 {
		t.Fatalf("selection should be 0, got %d", m.queueSel)
	}
	// delete the last one clears the queue and deselects
	m = press(t, m, tea.KeyMsg{Type: tea.KeyDelete})
	if len(m.queue) != 0 || m.queueSel != -1 {
		t.Fatalf("queue should be empty and deselected: %v sel=%d", m.queue, m.queueSel)
	}
}

func TestQueueBackspaceAlsoDeletes(t *testing.T) {
	m := busyQueueModel("only")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	m = press(t, m, tea.KeyMsg{Type: tea.KeyBackspace})
	if len(m.queue) != 0 {
		t.Fatalf("backspace should remove the selected queued message: %v", m.queue)
	}
}

func TestQueueNavOnlyWhenInputEmpty(t *testing.T) {
	m := busyQueueModel("queued")
	m.input.SetValue("typing something")
	m.input.CursorEnd()
	sel := m.queueSel
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.queueSel != sel {
		t.Fatalf("with text in the input, ↑ should edit history not the queue (sel %d→%d)", sel, m.queueSel)
	}
}

func TestQueueSelResetsOnSteer(t *testing.T) {
	m := busyQueueModel("a", "b")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.queueSel < 0 {
		t.Fatal("expected a selection")
	}
	// empty enter steers the whole queue and clears it
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 0 || m.queueSel != -1 {
		t.Fatalf("steer should clear queue and selection: %v sel=%d", m.queue, m.queueSel)
	}
}

// TestBusyCmdAllowList pins which commands run mid-turn (and which /goal
// forms do) — anything else must queue as a message.
func TestBusyCmdAllowList(t *testing.T) {
	runs := []string{
		"/help", "/theme", "/theme dark", "/mouse", "/effort", "/effort high",
		"/tasks", "/tasks abc123", "/goal", "/goal clear", "/goal rounds 5",
		"/cd", "/cd /tmp", "/pwd",
	}
	for _, c := range runs {
		if !busyCmd(c) {
			t.Errorf("%q should run mid-turn", c)
		}
	}
	queues := []string{
		"/goal resume", "/goal ship the release", "/model", "/model x",
		"/compact", "/clear", "/fork", "/rename", "/resume", "/quit",
		"/bogus", "hello",
	}
	for _, c := range queues {
		if busyCmd(c) {
			t.Errorf("%q should queue, not run mid-turn", c)
		}
	}
}

func TestEnterWhileBusyRunsSettingsCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep cfg.Save() away from the real config
	m := busyQueueModel()
	m.cfg = &config.Config{}
	m.input.SetValue("/effort high")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.queue) != 0 {
		t.Fatalf("/effort should run now, not queue: %v", m.queue)
	}
	if m.agent.Effort != "high" {
		t.Fatalf("effort should have changed to high, got %q", m.agent.Effort)
	}
	if len(m.blocks) == 0 {
		t.Fatal("the confirmation note should land in the transcript")
	}
	if m.hist[len(m.hist)-1] != "/effort high" {
		t.Fatalf("the command should be in history: %v", m.hist)
	}
}

func TestEnterWhileBusyQueuesOtherCommands(t *testing.T) {
	m := busyQueueModel()
	m.input.SetValue("/model gpt-5")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 1 || m.queue[0] != "/model gpt-5" {
		t.Fatalf("/model should still queue while busy: %v", m.queue)
	}
}

func TestEnterWhileBusyQueuesGoalSubmits(t *testing.T) {
	m := busyQueueModel()
	m.input.SetValue("/goal ship it")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 1 || m.queue[0] != "/goal ship it" {
		t.Fatalf("/goal <text> submits a turn and must queue: %v", m.queue)
	}

	m = busyQueueModel()
	m.input.SetValue("/goal resume")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.queue) != 1 || m.queue[0] != "/goal resume" {
		t.Fatalf("/goal resume submits a turn and must queue: %v", m.queue)
	}
}

func TestEnterWhileBusyRunsGoalSettings(t *testing.T) {
	m := busyQueueModel()
	m.goal = "old goal"
	m.input.SetValue("/goal clear")
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.goal != "" {
		t.Fatalf("/goal clear should run now, goal=%q", m.goal)
	}
	if len(m.queue) != 0 {
		t.Fatalf("/goal clear should not queue: %v", m.queue)
	}
}

func TestEscInterruptsMidResponse(t *testing.T) {
	m := &model{input: newInput(), agent: &agent.Agent{}, busy: true}
	m.width = 80
	m.input.SetWidth(78)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.cancel = cancel

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if ctx.Err() != context.Canceled {
		t.Fatalf("esc should cancel the in-flight turn, ctx.Err=%v", ctx.Err())
	}
}

func TestEscDoesNotInterruptWhenIdle(t *testing.T) {
	m := &model{input: newInput(), agent: &agent.Agent{}, busy: false}
	m.width = 80
	m.input.SetWidth(78)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.cancel = cancel // set but not busy

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if ctx.Err() == context.Canceled {
		t.Fatal("esc while idle should not cancel")
	}
}

func TestQueueViewShowsSelection(t *testing.T) {
	m := busyQueueModel("first", "second")
	m.queueSel = 1
	m.agent.Model = "m"
	m.provName = "p"
	view := m.View()
	if !strings.Contains(view, "del to remove") {
		t.Errorf("selected queued message should show a delete hint:\n%s", view)
	}
	if !strings.Contains(view, "↑/↓ select") {
		t.Errorf("queue footer should advertise navigation:\n%s", view)
	}
}
