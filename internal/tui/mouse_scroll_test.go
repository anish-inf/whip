package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A wheel-up MouseMsg routed through Update must scroll the transcript viewport
// up (YOffset increases) and drop follow mode. This is the event tmux forwards
// to whip now that mouse_any_flag=1 (the regression was: capture off → tmux
// swallowed the wheel into copy-mode, so YOffset never moved).
func TestWheelScrollsTranscript(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 20))
	// overflow the viewport so there's somewhere to scroll
	for range 40 {
		m.appendAssistant("line of transcript content that is long enough to matter")
	}
	m.vp.GotoBottom()
	if !m.vp.AtBottom() {
		t.Fatal("setup: should start at bottom")
	}
	start := m.vp.YOffset

	up := tea.MouseMsg(tea.MouseEvent{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp, X: 40, Y: 10})
	um, _ := m.Update(up)
	m = um.(*model)
	if m.vp.YOffset >= start {
		t.Fatalf("wheel-up must scroll up: YOffset %d -> %d", start, m.vp.YOffset)
	}
	if m.follow {
		t.Fatal("scrolling up off the bottom must drop follow mode")
	}

	down := tea.MouseMsg(tea.MouseEvent{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, X: 40, Y: 10})
	for range 20 {
		um, _ := m.Update(down)
		m = um.(*model)
	}
	if !m.vp.AtBottom() {
		t.Fatalf("wheel-down must scroll back to bottom, YOffset=%d", m.vp.YOffset)
	}
	if !m.follow {
		t.Fatal("returning to the bottom must re-engage follow mode")
	}
}
