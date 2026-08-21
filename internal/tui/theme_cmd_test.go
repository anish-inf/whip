package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

// bare /theme toggles.
func TestThemeToggleBare(t *testing.T) {
	m := compactCmdModel()
	m.setTheme("dark")
	m.command("/theme")
	if CurrentTheme() != "light" {
		t.Fatalf("bare /theme should toggle to light, got %q", CurrentTheme())
	}
	m.command("/theme")
	if CurrentTheme() != "dark" {
		t.Fatalf("second toggle should return to dark, got %q", CurrentTheme())
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
