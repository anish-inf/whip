package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/context-labs/whip/internal/config"
)

// snapshotStyles saves the package-global style vars and glyphs and restores
// them when the test ends, so mode-swapping tests don't bleed into others.
func snapshotStyles(t *testing.T) {
	t.Helper()
	y, b, tl, d, e, g, th := youStyle, botStyle, toolStyle, dimStyle, errStyle, growStyle, thinkingStyle
	gu, ga := glyphUser, glyphAssistant
	t.Cleanup(func() {
		youStyle, botStyle, toolStyle, dimStyle, errStyle, growStyle, thinkingStyle = y, b, tl, d, e, g, th
		glyphUser, glyphAssistant = gu, ga
	})
}

func TestApplyOpencodeStylesSwapsGlyphs(t *testing.T) {
	snapshotStyles(t)
	applyDefaultTheme()
	if glyphUser != "❯ " || glyphAssistant != "● " {
		t.Fatalf("default glyphs = %q/%q", glyphUser, glyphAssistant)
	}
	applyOpencodeStyles()
	if glyphUser != "┃ " || glyphAssistant != "▣ " {
		t.Fatalf("opencode glyphs = %q/%q, want ┃ / ▣", glyphUser, glyphAssistant)
	}
	// opencode uses fixed hex, not whip's AdaptiveColor.
	if got := toolStyle.GetForeground(); got != lipgloss.Color(ocPrimary) {
		t.Fatalf("toolStyle fg = %v, want opencode primary", got)
	}
	applyDefaultTheme()
	if glyphUser != "❯ " || glyphAssistant != "● " {
		t.Fatalf("revert failed: %q/%q", glyphUser, glyphAssistant)
	}
}

func TestOpencodeLogo(t *testing.T) {
	logo := opencodeLogo()
	lines := strings.Split(logo, "\n")
	if len(lines) != 4 {
		t.Fatalf("logo has %d lines, want 4", len(lines))
	}
	// Block-glyph wordmark must contain the upper/lower half blocks.
	if !strings.Contains(logo, "█") || !strings.Contains(logo, "▀") {
		t.Fatal("logo missing block glyphs")
	}
}

func TestUIModeLabel(t *testing.T) {
	if uiModeLabel(opencodeMode) != "opencode" {
		t.Fatal("opencode label")
	}
	if uiModeLabel("") != "default" {
		t.Fatal("default label")
	}
}

func TestStyleInputPlaceholder(t *testing.T) {
	snapshotStyles(t)
	m := &model{input: newInput()}
	m.uiMode = opencodeMode
	m.styleInput()
	if m.input.Placeholder != ocPlaceholder {
		t.Fatalf("opencode placeholder = %q", m.input.Placeholder)
	}
	m.uiMode = ""
	m.styleInput()
	if m.input.Placeholder != whipPlaceholder {
		t.Fatalf("default placeholder = %q", m.input.Placeholder)
	}
}

func TestSetUIModeRoundTrip(t *testing.T) {
	snapshotStyles(t)
	t.Setenv("HOME", t.TempDir()) // keep cfg.Save() off the real config
	m := &model{cfg: &config.Config{}, input: newInput()}

	m.setUIMode(opencodeMode)
	if m.uiMode != opencodeMode || m.cfg.UIMode != opencodeMode {
		t.Fatalf("enable: uiMode=%q cfg=%q", m.uiMode, m.cfg.UIMode)
	}
	if m.cfgExtra["uiMode"] != opencodeMode {
		t.Fatalf("cfgExtra not pinned: %v", m.cfgExtra)
	}
	if glyphAssistant != "▣ " {
		t.Fatalf("glyph not swapped: %q", glyphAssistant)
	}

	m.setUIMode("bogus") // anything not "opencode" reverts to default
	if m.uiMode != "" || m.cfg.UIMode != "" {
		t.Fatalf("revert: uiMode=%q cfg=%q", m.uiMode, m.cfg.UIMode)
	}
	if _, ok := m.cfgExtra["uiMode"]; ok {
		t.Fatalf("cfgExtra still pinned: %v", m.cfgExtra)
	}
	if glyphUser != "❯ " {
		t.Fatalf("glyph not reverted: %q", glyphUser)
	}
}
