package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/lsp"
)

// opencode.go implements whip's "opencode" UI mode: an opt-in *structural*
// layout inspired by opencode's TUI (github.com/sst/opencode) — full-screen,
// a right-hand sidebar, and the block-glyph wordmark. It deliberately keeps
// whip's own theming (light/dark/auto) and colors; only the layout/structure
// changes. Enabled with config UIMode == "opencode" or via the command palette
// (Display → UI mode).

// opencodeMode is the config/UIMode value that selects this render mode.
const opencodeMode = "opencode"

// sidebarWidth is the fixed width of the opencode-mode right sidebar, matching
// opencode (routes/session/sidebar.tsx). The sidebar shows only when the
// terminal is at least sidebarMinWidth columns wide, so a narrow terminal
// falls back to the single-column layout.
const (
	sidebarWidth    = 42
	sidebarMinWidth = 120
)

// The "opencode" block-glyph wordmark (src/logo.ts), decoded to plain runes.
// "open" renders dim, "code" renders in the foreground/bold — both through
// whip's theme styles so it adapts to light/dark like the rest of the UI.
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

// opencodeLogo renders the wordmark: dim "open", bold "code", joined with a
// single-column gap per line. Colored via whip's theme (dimStyle / bold text).
func opencodeLogo() string {
	left := dimStyle
	right := lipgloss.NewStyle().Bold(true)
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

// sidebarVisible reports whether the opencode-mode sidebar should render: the
// mode is on and the terminal is wide enough to spare sidebarWidth columns.
func (m *model) sidebarVisible() bool {
	return m.uiMode == opencodeMode && m.termWidth >= sidebarMinWidth
}

// sidebarView renders the opencode right sidebar: session title, a Context
// block (tokens / % of window / spend), LSP status, and a footer. Height is
// the number of rows to fill so the sidebar spans the body. All styling uses
// whip's theme styles, so it honors light/dark/auto.
func (m *model) sidebarView(height int) string {
	head := lipgloss.NewStyle().Bold(true)

	var b strings.Builder
	b.WriteString(head.Render(truncLine(filepath.Base(cwd()), sidebarWidth-4)) + "\n\n")

	// Context: tokens used, share of the window, spend.
	b.WriteString(head.Render("Context") + "\n")
	u := m.agent.Usage()
	b.WriteString(dimStyle.Render(fmt.Sprintf("%s tokens", fmtTok(u.PromptTokens+u.CompletionTokens))) + "\n")
	if m.agent.ContextLimit > 0 {
		pct := agent.EstimateTokens(m.agent.Messages) * 100 / m.agent.ContextLimit
		b.WriteString(dimStyle.Render(fmt.Sprintf("%d%% used", pct)) + "\n")
	}
	if cost, ok := m.sessionCost(); ok {
		b.WriteString(dimStyle.Render(fmt.Sprintf("$%.2f spent", cost)) + "\n\n")
	} else {
		b.WriteString(dimStyle.Render("$0.00 spent") + "\n\n")
	}

	// LSP status.
	b.WriteString(head.Render("LSP") + "\n")
	b.WriteString(dimStyle.Render(m.lspSummary()) + "\n")

	body := b.String()

	// Pad/clip to the requested height, then style as a fixed-width column with
	// a subtle left border to set it apart from the transcript.
	rows := strings.Split(body, "\n")
	for len(rows) < height {
		rows = append(rows, "")
	}
	if height > 0 && len(rows) > height {
		rows = rows[:height]
	}
	col := lipgloss.NewStyle().
		Width(sidebarWidth).
		PaddingLeft(2).
		PaddingRight(2).
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).
		BorderForeground(lipgloss.AdaptiveColor{Light: "250", Dark: "238"})
	return col.Render(strings.Join(rows, "\n"))
}

// lspSummary is a one-line LSP status for the sidebar: a connected count, or a
// disabled note when no LSP manager is configured.
func (m *model) lspSummary() string {
	if m.lspMgr == nil {
		return "LSPs are disabled"
	}
	return lspSummaryLine(m.lspMgr.Statuses())
}

// lspSummaryLine is the pure formatter behind lspSummary (extracted so its
// branches are testable without a live LSP server).
func lspSummaryLine(servers []lsp.Status) string {
	if len(servers) == 0 {
		return "no servers"
	}
	connected := 0
	for _, s := range servers {
		if s.State == "connected" {
			connected++
		}
	}
	return fmt.Sprintf("%d/%d connected", connected, len(servers))
}

// applyUIMode points the live render state at the given UI mode. opencode mode
// is purely structural — it does not touch whip's theme, colors, glyphs, or
// spinner — so this only records the flag.
func (m *model) applyUIMode(mode string) {
	if mode == opencodeMode {
		m.uiMode = opencodeMode
	} else {
		m.uiMode = ""
	}
}

// setUIMode switches render mode live, persists the choice, and redraws. It
// returns the bubbletea command that enters/exits the alternate screen so the
// full-screen state tracks the mode.
func (m *model) setUIMode(mode string) tea.Cmd {
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
	m.append(dimStyle.Render("◐ ui mode: " + uiModeLabel(mode)))
	if mode == opencodeMode {
		return tea.EnterAltScreen
	}
	return tea.ExitAltScreen
}

// uiModeLabel is the display name for a UI mode value.
func uiModeLabel(mode string) string {
	if mode == opencodeMode {
		return "opencode"
	}
	return "default"
}
