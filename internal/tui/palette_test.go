package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPaletteOpensAndClosesOnEsc(t *testing.T) {
	m := compactCmdModel()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = tm.(*model)
	if m.palette == nil {
		t.Fatal("ctrl+p should open the palette")
	}
	// esc pops the dialog (opencode: esc pops one level)
	tm, _ = m.key(tea.KeyMsg{Type: tea.KeyEsc})
	m = tm.(*model)
	if m.palette != nil {
		t.Fatal("esc should close the palette")
	}
}

func TestPaletteSuggestedGroupOnTop(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	if m.palette.items[0].category != "Suggested" {
		t.Fatalf("empty filter should pin a Suggested group, got %q", m.palette.items[0].category)
	}
	titles := map[string]bool{}
	for _, it := range m.palette.items {
		titles[it.title] = true
	}
	for _, want := range []string{"Switch model", "Resume session", "Compact session", "Help", "Quit"} {
		if !titles[want] {
			t.Errorf("palette missing %q", want)
		}
	}
}

func TestPaletteFilter(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	for _, r := range "compact" {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	if len(m.palette.items) != 1 || m.palette.items[0].title != "Compact session" {
		t.Fatalf("filter 'compact': %+v", m.palette.items)
	}
	if m.palette.items[0].category != "Session" {
		t.Fatalf("filtering drops the Suggested group, got %q", m.palette.items[0].category)
	}
	// backspace restores the full list
	for i := 0; i < 7; i++ {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyBackspace})
		m = tm.(*model)
	}
	if len(m.palette.items) < 10 {
		t.Fatalf("backspace should restore all items, got %d", len(m.palette.items))
	}
}

func TestPaletteNavigationWraps(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	n := len(m.palette.items)
	// up from the top wraps to the bottom
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyUp})
	m = tm.(*model)
	if m.palette.idx != n-1 {
		t.Fatalf("up from 0 should wrap to %d, got %d", n-1, m.palette.idx)
	}
	// down from the bottom wraps to the top
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	if m.palette.idx != 0 {
		t.Fatalf("down should wrap to 0, got %d", m.palette.idx)
	}
}

func TestPaletteEnterRunsCommand(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	for _, r := range "quit" {
		tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	_, cmd := m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Quit should return tea.Quit")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("expected tea.QuitMsg, got %v", msg)
	}
	if m.palette != nil {
		t.Fatal("palette should close after running a command")
	}
}

func TestPaletteViewRendersCategories(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	m.width = 100
	v := m.paletteView()
	for _, want := range []string{"Commands", "Suggested", "Agent", "Session", "Display", "App", "esc close"} {
		if !strings.Contains(v, want) {
			t.Errorf("palette view missing %q:\n%s", want, v)
		}
	}
}

// The palette must not swallow the agent's interrupt keys while a turn runs:
// it routes through key() only as a modal, and ctrl+c closes it like esc.
func TestPaletteCtrlCClosesNotQuits(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = tm.(*model)
	if m.palette != nil {
		t.Fatal("ctrl+c should close the palette, not quit the app")
	}
}
