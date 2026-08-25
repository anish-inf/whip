package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/context-labs/loopy/internal/config"
)

// /theme light must switch markdown rendering to the light style (dark text
// 234) immediately, and /theme dark back — and both must survive a render of
// every sample kind (the chroma registry poisoning case).
func TestThemeCommandSwitchesRendering(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.command("/theme light")
	if CurrentTheme() != "light" {
		t.Fatalf("theme: %q", CurrentTheme())
	}
	out := renderMarkdown("body **bold** `code`\n\n```go\nx := 1\n```", 70)
	if !strings.Contains(out, "38;5;234") {
		t.Errorf("light body should be 234: %q", out[:80])
	}
	m.command("/theme dark")
	if CurrentTheme() != "dark" {
		t.Fatalf("theme: %q", CurrentTheme())
	}
	out = renderMarkdown("body\n\n```go\nx := 1\n```", 70)
	if !strings.Contains(out, "38;5;252") || !strings.Contains(out, "38;5;251") {
		t.Errorf("dark body/code should be 252/251 after switch back: %q", out[:120])
	}
	// and flip back to light once more — the chroma poisoning case
	m.command("/theme light")
	out = renderMarkdown("```go\nx := 1\n```", 70)
	if strings.Contains(out, "38;5;251") {
		t.Errorf("light code block must not use dark chroma 251: %q", out[:120])
	}
	m.setTheme("dark") // leave tests in dark default
}

// bare /theme opens the theme switcher (palette panel) instead of toggling
// blindly — the whole point is to see the choices.
func TestThemeBareOpensSwitcher(t *testing.T) {
	m := compactCmdModel()
	m.command("/theme")
	if m.palette == nil {
		t.Fatal("bare /theme should open the palette")
	}
	pp := m.palette.top()
	if pp == nil || pp.kind != panelTheme {
		t.Fatalf("expected the theme panel, got %+v", pp)
	}
	// the panel lists auto/light/dark with the current one selected
	if len(pp.list) != 3 || pp.list[0] != "auto" || pp.list[1] != "light" || pp.list[2] != "dark" {
		t.Fatalf("theme panel list: %v", pp.list)
	}
	// navigate to light and apply with enter
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
	m = tm.(*model)
	tm, _ = m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if CurrentTheme() != "light" {
		t.Fatalf("selecting light in the switcher should apply it, got %q", CurrentTheme())
	}
	// the switcher came from /theme, not ctrl+p: commit-and-close, don't
	// strand the user on a palette root they never opened
	if m.palette != nil {
		t.Fatal("enter in a directly-opened switcher should close the palette")
	}
	m.setTheme("dark") // leave dark default for other tests
}

// Theme defaults to auto ("" in config) unless the user picks one.
func TestThemeDefaultsToAuto(t *testing.T) {
	cfg := config.Default()
	if cfg.Theme != "" {
		t.Fatalf("default theme should be auto (\"\"), got %q", cfg.Theme)
	}
}

// the full screen renders without artifacts under both themes
func TestNoArtifactsBothThemes(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		m := compactCmdModel()
		m.Update(mkWinSize(70, 30))
		m.setTheme(theme)
		m.appendAssistant("Found it. **Fixed**:\n\n1. one\n2. two\n\n```go\nx := 1\n```")
		v := m.View()
		for i, l := range strings.Split(v, "\n") {
			if strings.Contains(l, "\x1b[m") {
				t.Errorf("%s: row %d bare SGR: %q", theme, i, l)
			}
			if strings.TrimSpace(ansi.Strip(l)) == "" && strings.Contains(l, "\x1b[") {
				t.Errorf("%s: row %d styled blank: %q", theme, i, l)
			}
			if ansi.StringWidth(l) > 70 {
				t.Errorf("%s: row %d overflows (%d)", theme, i, ansi.StringWidth(l))
			}
		}
		m.setTheme("dark")
	}
}
