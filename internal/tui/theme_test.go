package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// On a light terminal the markdown body must render in the light style's
// dark color (234), not the dark style's 252 (near-invisible on white).
func TestLightThemeRendersDarkText(t *testing.T) {
	SetLightTheme(true)
	defer SetLightTheme(false)
	out := renderMarkdown("plain body text", 60)
	if !strings.Contains(out, "\x1b[38;5;234m") {
		t.Errorf("light theme should render body in color 234, got %q", out)
	}
	if strings.Contains(out, "\x1b[38;5;252m") {
		t.Errorf("light theme must not use dark-style color 252: %q", out)
	}
	// width behavior unchanged
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 60 {
			t.Errorf("light render overflow: %q", l)
		}
	}
}

// LOOPY_THEME overrides detection.
func TestThemeOverride(t *testing.T) {
	t.Setenv("LOOPY_THEME", "light")
	detectColorScheme()
	mdMu.Lock()
	light := mdLight
	mdMu.Unlock()
	if !light {
		t.Fatal("LOOPY_THEME=light should select the light style")
	}
	t.Setenv("LOOPY_THEME", "dark")
	detectColorScheme()
	mdMu.Lock()
	light = mdLight
	mdMu.Unlock()
	if light {
		t.Fatal("LOOPY_THEME=dark should select the dark style")
	}
}

// COLORFGBG is honored when LOOPY_THEME is unset.
func TestColorFGBGDetection(t *testing.T) {
	t.Setenv("LOOPY_THEME", "")
	t.Setenv("COLORFGBG", "0;15") // dark fg on white bg
	detectColorScheme()
	mdMu.Lock()
	light := mdLight
	mdMu.Unlock()
	if !light {
		t.Fatal("COLORFGBG=0;15 should select the light style")
	}
	t.Setenv("COLORFGBG", "15;0") // white on black
	detectColorScheme()
	mdMu.Lock()
	light = mdLight
	mdMu.Unlock()
	if light {
		t.Fatal("COLORFGBG=15;0 should select the dark style")
	}
}
