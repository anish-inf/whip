package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newGrowModel builds a model with the real input and a known width, as Run
// does after the first WindowSizeMsg.
func newGrowModel() *model {
	m := &model{input: newInput()}
	m.width = 80
	m.input.SetWidth(m.width - 2) // matches Update's WindowSizeMsg handling
	m.layout()
	return m
}

// Regression: ctrl+j must both insert a newline and grow the input box so the
// whole prompt stays visible. The bug was that layout() sized the box from
// View(), which the textarea clamps to its current height — so it never grew.
func TestInputGrowsOnCtrlJ(t *testing.T) {
	m := newGrowModel()
	if got := m.input.Height(); got != 1 {
		t.Fatalf("empty input should be 1 line, got %d", got)
	}

	m.input.SetValue("first line")
	m.input.CursorEnd()

	// press ctrl+j through the real key handler, then type the next line
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = tm.(*model)
	m.input.InsertString("second line")
	m.layout()

	if got := m.input.LineCount(); got != 2 {
		t.Fatalf("ctrl+j should insert a newline: LineCount=%d value=%q", got, m.input.Value())
	}
	if got := m.input.Height(); got != 2 {
		t.Fatalf("input box should grow to 2 lines, got %d", got)
	}

	// a third line keeps it growing
	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = tm.(*model)
	m.input.InsertString("third line")
	m.layout()
	if got := m.input.Height(); got != 3 {
		t.Fatalf("input box should grow to 3 lines, got %d", got)
	}
}

// A single long line that wraps past the content width must also grow the box.
func TestInputGrowsOnWrap(t *testing.T) {
	m := newGrowModel()
	m.input.SetValue(strings.Repeat("x", (m.input.Width()-2)*2)) // two full content rows
	m.layout()
	if got := m.input.Height(); got != 2 {
		t.Fatalf("wrapped long line should need 2 rows, got %d", got)
	}
}

// The box must never exceed MaxHeight, so very long input scrolls instead of
// pushing the transcript off-screen.
func TestInputCappedAtMaxHeight(t *testing.T) {
	m := newGrowModel()
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, "line")
	}
	m.input.SetValue(strings.Join(lines, "\n"))
	m.layout()
	if got := m.input.Height(); got != m.input.MaxHeight {
		t.Fatalf("input should cap at MaxHeight=%d, got %d", m.input.MaxHeight, got)
	}
}

// Deleting back to one line shrinks the box again.
func TestInputShrinksWhenContentRemoved(t *testing.T) {
	m := newGrowModel()
	m.input.SetValue("a\nb\nc")
	m.layout()
	if got := m.input.Height(); got != 3 {
		t.Fatalf("3 lines, got %d", got)
	}
	m.input.SetValue("a")
	m.layout()
	if got := m.input.Height(); got != 1 {
		t.Fatalf("should shrink back to 1 line, got %d", got)
	}
}

// Regression: when the box grows, every line of content must be visible in the
// rendered textarea — the textarea's internal viewport must not clip the top
// lines. The bug: repositionView only scrolls down to track the cursor, so
// after growing, earlier lines sat above the visible window. Proven by driving
// the real Update() path and checking each line appears in input.View().
func TestInputShowsAllLinesAfterGrowth(t *testing.T) {
	m := newGrowModel()
	lines := []string{"line one", "line two", "line three", "line four"}
	for i, ln := range lines {
		if i > 0 {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
			m = tm.(*model)
		}
		for _, r := range ln {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			m = tm.(*model)
		}
	}
	if got := m.input.Height(); got != len(lines) {
		t.Fatalf("box should have grown to %d lines, got %d", len(lines), got)
	}
	rendered := m.input.View()
	for _, ln := range lines {
		if !strings.Contains(rendered, ln) {
			t.Errorf("rendered input is missing %q\n--- rendered ---\n%s", ln, rendered)
		}
	}
}

// The box caps at MaxHeight and keeps the cursor's line visible when content
// exceeds it (older lines scroll off, which is correct once capped).
func TestInputScrollsWhenCapped(t *testing.T) {
	m := newGrowModel()
	for i := 0; i < m.input.MaxHeight+5; i++ {
		if i > 0 {
			tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
			m = tm.(*model)
		}
		tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(fmt.Sprintf("row%d", i))})
		m = tm.(*model)
	}
	if got := m.input.Height(); got != m.input.MaxHeight {
		t.Fatalf("should cap at MaxHeight=%d, got %d", m.input.MaxHeight, got)
	}
	// the most recent line (with the cursor) must be visible
	last := fmt.Sprintf("row%d", m.input.MaxHeight+4)
	if !strings.Contains(m.input.View(), last) {
		t.Errorf("cursor line %q should be visible after cap\n%s", last, m.input.View())
	}
}
