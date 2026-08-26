package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Drag on the exact screen row of a block must copy that block's text.
func TestSelectionRowAccuracy(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.append("hello world")                                               // block 0
	m.appendAssistantBlock("MARKER-ANSWER")                               // block 1
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}) // settle
	m = tm.(*model)
	m.input.SetValue("")

	// find MARKER on the rendered screen: its row AND its start column (the
	// assistant "● " prefix width depends on render state, so don't hardcode it)
	v := m.View()
	screenRow, screenCol := -1, -1
	for i, l := range strings.Split(v, "\n") {
		if j := strings.Index(ansi.Strip(l), "MARKER-ANSWER"); j >= 0 {
			// view row i renders at absolute screen row viewTop + i, and
			// mouse events carry absolute screen coordinates
			screenRow, screenCol = m.viewTop+i, ansi.StringWidth(ansi.Strip(l)[:j])
		}
	}
	if screenRow < 0 {
		t.Fatal("MARKER not rendered")
	}
	// the short view is bottom-anchored, so its top row is well below screen
	// row 0 — this is what makes the test catch top-anchored (viewTop-less)
	// row math, which selected ~2 rows above the pointer in real terminals
	if m.viewTop == 0 {
		t.Fatal("test setup: view must not start at screen row 0")
	}
	t.Logf("MARKER at screen (%d,%d); block[1] y0=%d", screenRow, screenCol, m.blocks[1].y0)

	// drag across that screen row, starting exactly on the M
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: screenCol, Y: screenRow})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: screenCol + 13, Y: screenRow})
	m = tm.(*model)
	tm, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, X: screenCol + 13, Y: screenRow})
	m = tm.(*model)
	if m.sel == nil {
		t.Fatal("no selection")
	}
	got := m.selText(*m.sel)
	if !strings.Contains(got, "MARKER") {
		t.Fatalf("drag on row %d copied %q, want MARKER text", screenRow, got)
	}
	t.Logf("copied %q", got)
}
