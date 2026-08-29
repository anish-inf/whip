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

// whipPlaceholder is whip's default prompt placeholder, restored when leaving
// opencode mode. Keep in sync with newInput.
const whipPlaceholder = "Ask whip anything… (/ for commands, tab completes)"

// ocActive mirrors m.uiMode == opencodeMode at package scope so block.render (a
// method on block, not model) can branch on the render mode. Set by applyUIMode.
var ocActive bool

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

// opencodeHome renders the empty-state "home" screen: the wordmark logo
// centered in the given area, like opencode's home route before any messages.
func opencodeHome(width, height int) string {
	logo := opencodeLogo()
	block := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, logo)
	return block
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

	title := strings.TrimSpace(m.sessTitle)
	if title == "" {
		title = filepath.Base(cwd()) // untitled session: fall back to the working dir
	}

	var b strings.Builder
	b.WriteString(head.Render(truncLine(title, sidebarWidth-4)) + "\n\n")

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

	// Top content (title + Context + LSP), clipped if the sidebar is very short.
	top := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	footer := growStyle.Render("• ") + head.Render("whip") + dimStyle.Render(" "+Version)

	rows := make([]string, 0, height)
	if height <= 0 {
		rows = append(top, footer)
	} else {
		if len(top) > height-1 { // keep the last row for the footer
			top = top[:max(height-1, 0)]
		}
		rows = append(rows, top...)
		for len(rows) < height-1 {
			rows = append(rows, "")
		}
		rows = append(rows, footer) // pinned to the bottom row
	}
	// opencode's sidebar has no border — it's set apart by a panel background and
	// spacing; keeping whip's theme, a left pad plus the gap is the analog.
	col := lipgloss.NewStyle().Width(sidebarWidth).PaddingLeft(2).PaddingRight(2)
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

// opencodePrompt wraps the textarea in opencode's prompt chrome: a ┃ left bar,
// the input, a model/mode row beneath, and a ╹ tail with a ▀ underline. Themed
// with whip's styles (no forced colors). width is the content width. inner is
// m.input.View() (already includes the textarea's own "┃ " prompt, so we strip
// it and supply the bar ourselves for the full-height box).
func (m *model) opencodePrompt(inner string, width int) string {
	if width < 6 {
		return inner
	}
	bar := youStyle.Render("┃")
	var b strings.Builder
	for _, ln := range strings.Split(inner, "\n") {
		b.WriteString(bar + " " + ln + "\n")
	}
	// model/mode row: "Low · kimi-k3  provider"
	meta := m.ocModeLabel() + dimStyle.Render(" · ") + m.modelName + dimStyle.Render("  "+m.provName)
	b.WriteString(bar + " " + meta + "\n")
	b.WriteString(youStyle.Render("╹") + dimStyle.Render(strings.Repeat("▀", max(width-2, 0))))
	return b.String()
}

// opencodeUserCard renders a user turn as opencode's bordered card: a ┃ left
// bar (accent color) with one blank padding row above and below the text.
// Themed with whip's styles (no forced background), so the bar + padding give
// the card impression while honoring light/dark/auto.
func opencodeUserCard(text string, width int) string {
	if width < 4 {
		return text
	}
	bar := youStyle.Render("┃")
	lines := strings.Split(wrap(text, width-2), "\n")
	rows := append([]string{""}, lines...) // blank row above
	rows = append(rows, "")                // blank row below
	var b strings.Builder
	for i, ln := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(bar + " " + ln)
	}
	return b.String()
}

// ocModeLabel is the left segment of the prompt meta row. whip has no named
// agents like opencode's "Build"; its closest analog is the reasoning effort.
func (m *model) ocModeLabel() string {
	eff := m.agent.Effort
	if eff == "" {
		eff = "off"
	}
	return strings.ToUpper(eff[:1]) + eff[1:]
}

// applyUIMode points the live render state at the given UI mode. opencode mode
// is purely structural — it does not touch whip's theme, colors, glyphs, or
// spinner — so this only records the flag.
func (m *model) applyUIMode(mode string) {
	if mode == opencodeMode {
		m.uiMode = opencodeMode
		ocActive = true
		m.input.Prompt = "" // opencodePrompt supplies the ┃ bar per line
		m.input.Placeholder = "Ask anything…"
	} else {
		m.uiMode = ""
		ocActive = false
		m.input.Prompt = "┃ "
		m.input.Placeholder = whipPlaceholder
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
