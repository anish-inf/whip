package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// opencode.go reproduces the visual identity of opencode's TUI
// (github.com/sst/opencode, MIT-licensed) as a selectable render mode, so whip
// can draw the same way. Enabled with config UIMode == "opencode" (also via the
// command palette). It swaps the package style vars, marker glyphs, the prompt
// input styling, the spinner, and shows opencode's block-glyph wordmark at
// startup. Every value below — the hex palette, the box-drawing glyphs, the
// logo — is lifted from opencode's default "opencode" dark theme
// (packages/tui/src/theme/assets/opencode.json and src/logo.ts) so the output
// matches pixel-for-pixel where whip's single-column layout allows.

// opencodeMode is the config/UIMode value that selects this render mode.
const opencodeMode = "opencode"

// Dark "opencode" theme, hex values verbatim from opencode.json.
const (
	ocPrimary   = "#fab283" // warm peach — tool/accent text
	ocSecondary = "#5c9cf5" // blue — default agent color (user marker)
	ocAccent    = "#9d7cd8" // purple — assistant marker / headings
	ocText      = "#eeeeee" // foreground
	ocTextMuted = "#808080" // muted / dim
	ocError     = "#e06c75" // red
	ocWarning   = "#f5a742" // orange — reasoning/thinking
	ocSuccess   = "#7fd88f" // green
)

// whipPlaceholder is the default (non-opencode) prompt placeholder, restored
// when switching back out of opencode mode. Keep in sync with newInput.
const whipPlaceholder = "Ask whip anything… (/ for commands, tab completes)"

// ocPlaceholder mirrors opencode's prompt placeholder.
const ocPlaceholder = "Ask anything…"

// ocSpinner reproduces opencode's braille loading spinner (component/spinner.tsx,
// ~80ms/frame).
var ocSpinner = spinner.Spinner{
	Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	FPS:    80 * time.Millisecond,
}

// The "opencode" block-glyph wordmark (src/logo.ts), decoded to plain runes:
// opencode's encoding maps _→space, ^→▀, ~→▀, so the shapes below are the
// literal cells. "open" renders muted, "code" renders in the foreground, bold;
// a one-column gap joins the two halves per line.
var (
	ocLogoOpen = []string{
		"                  ",
		"█▀▀█ █▀▀█ █▀▀█ █▀▀▄",
		"█  █ █  █ █▀▀▀ █  █",
		"▀▀▀▀ █▀▀▀ ▀▀▀▀ ▀  ▀",
	}
	ocLogoCode = []string{
		"             ▄     ",
		"█▀▀▀ █▀▀█ █▀▀█ █▀▀█",
		"█    █  █ █  █ █▀▀▀",
		"▀▀▀▀ ▀▀▀▀ ▀▀▀▀ ▀▀▀▀",
	}
)

// opencodeLogo renders the wordmark: muted "open", foreground-bold "code",
// joined with a single-column gap per line.
func opencodeLogo() string {
	left := lipgloss.NewStyle().Foreground(lipgloss.Color(ocTextMuted))
	right := lipgloss.NewStyle().Foreground(lipgloss.Color(ocText)).Bold(true)
	var b strings.Builder
	for i := range ocLogoOpen {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(left.Render(ocLogoOpen[i]))
		b.WriteByte(' ')
		b.WriteString(right.Render(ocLogoCode[i]))
	}
	return b.String()
}

// applyOpencodeStyles repoints the package style vars and marker glyphs at
// opencode's dark palette. Global, matching how whip's theme is already
// package-global; setUIMode / applyDefaultTheme are the only callers plus
// startup.
func applyOpencodeStyles() {
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }
	youStyle = fg(ocSecondary).Bold(true)
	botStyle = fg(ocAccent).Bold(true)
	toolStyle = fg(ocPrimary)
	dimStyle = fg(ocTextMuted)
	errStyle = fg(ocError)
	growStyle = fg(ocSuccess)
	thinkingStyle = fg(ocWarning).Italic(true)
	glyphUser = "┃ "      // opencode's left message bar (U+2503)
	glyphAssistant = "▣ " // opencode's assistant/attribution bullet (U+25A3)
	// opencode owns a near-black background; force dark so any adaptive
	// pickers elsewhere resolve to their dark variants.
	lipgloss.SetHasDarkBackground(true)
}

// applyDefaultTheme restores whip's original style vars and glyphs, mirroring
// the definitions in tui.go. Called when leaving opencode mode.
func applyDefaultTheme() {
	adaptive := func(light, dark string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: light, Dark: dark})
	}
	youStyle = adaptive("21", "12").Bold(true)
	botStyle = adaptive("90", "13").Bold(true)
	toolStyle = adaptive("136", "11")
	dimStyle = adaptive("240", "245")
	errStyle = adaptive("124", "9")
	growStyle = adaptive("28", "10")
	thinkingStyle = adaptive("240", "245").Italic(true)
	glyphUser = "❯ "
	glyphAssistant = "● "
}

// styleInput re-applies the prompt input styles from the current package style
// vars and sets the mode's placeholder. Called after the style vars change,
// since the textarea copies style values at set time.
func (m *model) styleInput() {
	m.input.FocusedStyle.Placeholder = dimStyle
	m.input.BlurredStyle.Placeholder = dimStyle
	m.input.FocusedStyle.Prompt = botStyle
	m.input.BlurredStyle.Prompt = dimStyle
	if m.uiMode == opencodeMode {
		m.input.Placeholder = ocPlaceholder
	} else {
		m.input.Placeholder = whipPlaceholder
	}
}

// applyUIMode points the live render state at the given UI mode without
// persisting. Called at startup and by setUIMode.
func (m *model) applyUIMode(mode string) {
	if mode == opencodeMode {
		m.uiMode = opencodeMode
		applyOpencodeStyles()
		m.spin = spinner.New(spinner.WithSpinner(ocSpinner))
	} else {
		m.uiMode = ""
		applyDefaultTheme()
		m.applyTheme(m.cfg.Theme) // restore the correct light/dark scheme
		m.spin = spinner.New(spinner.WithSpinner(spinner.Dot))
	}
	m.styleInput()
}

// setUIMode switches render mode live, persists the choice, and redraws.
func (m *model) setUIMode(mode string) {
	if mode != opencodeMode {
		mode = ""
	}
	m.applyUIMode(mode)
	m.cfg.UIMode = mode
	if m.cfgExtra == nil {
		m.cfgExtra = map[string]string{}
	}
	if mode == "" {
		delete(m.cfgExtra, "uiMode")
	} else {
		m.cfgExtra["uiMode"] = mode
	}
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	m.refreshVP()
	if mode == opencodeMode {
		m.append(opencodeLogo())
	}
	m.append(dimStyle.Render("◐ ui mode: " + uiModeLabel(mode)))
}

// uiModeLabel is the display name for a UI mode value.
func uiModeLabel(mode string) string {
	if mode == opencodeMode {
		return "opencode"
	}
	return "default"
}
