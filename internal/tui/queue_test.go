package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/abe/loopy/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
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
