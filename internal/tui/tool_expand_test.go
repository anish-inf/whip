package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Tool results collapse to a preview; ctrl+e toggles the latest one and
// clicking the block expands it.
func TestToolExpand(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	result := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8"
	m.appendRaw(blockTool, result)

	// collapsed: 5 lines + hint
	out := ansi.Strip(m.blocks[0].render(m.width))
	if strings.Contains(out, "line8") || !strings.Contains(out, "… +3 lines") {
		t.Fatalf("collapsed render wrong: %q", out)
	}

	// ctrl+e expands the latest tool block
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = tm.(*model)
	out = ansi.Strip(m.blocks[0].render(m.width))
	if !strings.Contains(out, "line8") || strings.Contains(out, "…") {
		t.Fatalf("expanded render wrong: %q", out)
	}

	// and collapses back
	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = tm.(*model)
	if m.blocks[0].expanded {
		t.Fatal("second ctrl+e should collapse")
	}

	// click on the block row expands it
	m.refreshVP()
	y0 := m.blocks[0].y0 + m.contentPad()
	screenY := y0 - m.vp.YOffset + 2
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 5, Y: screenY})
	m = tm.(*model)
	if !m.blocks[0].expanded {
		t.Fatalf("click at screen Y=%d should expand the tool block", screenY)
	}
}
