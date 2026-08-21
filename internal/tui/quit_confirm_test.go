package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/context-labs/loopy/internal/agent"
)

// idleModel builds a model that is not busy (no agent turn running).
func idleModel() *model {
	m := &model{
		input: newInput(),
		agent: &agent.Agent{},
	}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	return m
}

// A single ctrl+c at idle must NOT quit; it arms a confirm window. The second
// ctrl+c within the window quits. After the window expires (quitArmMsg), the
// next press re-arms instead of quitting.
func TestCtrlCRequiresDoublePressToQuit(t *testing.T) {
	m := idleModel()

	// first press: arms, no quit
	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.quit1 {
		t.Fatal("first ctrl+c should arm quit1")
	}
	if cmd != nil {
		// the cmd is the arm-window ticker, not tea.Quit
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("first ctrl+c must not quit")
		}
	}

	// second press within the window: quits
	tm, cmd = m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if cmd == nil {
		t.Fatal("second ctrl+c should produce a quit command")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Fatalf("second ctrl+c should quit, got %T", cmd())
	}
	if m.quit1 {
		t.Fatal("quit1 should clear after the confirming press")
	}
}

// After the arm window expires, a lone ctrl+c re-arms rather than quitting.
func TestCtrlCArmExpires(t *testing.T) {
	m := idleModel()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.quit1 {
		t.Fatal("first press should arm")
	}

	// simulate the 2s window elapsing
	tm2, _ := m.Update(quitArmMsg{})
	m = tm2.(*model)
	if m.quit1 {
		t.Fatal("quitArmMsg should disarm quit1")
	}

	// now a single press re-arms (no quit)
	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.quit1 {
		t.Fatal("post-expiry press should re-arm, not quit")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("post-expiry single press must not quit")
		}
	}
}

// The busy path is unchanged: first ctrl+c arms the interrupt, second cancels
// the in-flight turn — it never quits the program outright.
func TestCtrlCBusyInterruptsNotQuits(t *testing.T) {
	m := idleModel()
	m.busy = true
	cancelled := false
	m.cancel = func() { cancelled = true }

	tm, cmd := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !m.interrupt1 {
		t.Fatal("busy first press should arm interrupt1")
	}
	if cancelled {
		t.Fatal("busy first press must not cancel yet")
	}

	tm, cmd = m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if !cancelled {
		t.Fatal("busy second press should cancel the turn")
	}
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Fatal("busy path must never quit")
		}
	}
}
