package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

// opencode's own theme palette (packages/tui/src/theme/assets/opencode.json),
// both variants, so opencode mode matches opencode pixel-for-pixel while still
// honoring whip's light/dark/auto — dark terminal → opencode dark, light → light.
var (
	ocPanelBg    = lipgloss.AdaptiveColor{Light: "#fafafa", Dark: "#141414"} // backgroundPanel: cards, sidebar
	ocElementBg  = lipgloss.AdaptiveColor{Light: "#f5f5f5", Dark: "#1e1e1e"} // backgroundElement: prompt box
	ocAgentCol   = lipgloss.AdaptiveColor{Light: "#7b5bb6", Dark: "#5c9cf5"} // secondary: agent color (bars, ▣)
	ocTextCol    = lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#eeeeee"} // text
	ocMutedCol   = lipgloss.AdaptiveColor{Light: "#8a8a8a", Dark: "#808080"} // textMuted
	ocWarnCol    = lipgloss.AdaptiveColor{Light: "#d68c27", Dark: "#f5a742"} // warning: reasoning "+ Thought"
	ocSuccessCol = lipgloss.AdaptiveColor{Light: "#3d9a57", Dark: "#7fd88f"} // success: sidebar footer bullet
)

// sidebarWidth is the fixed width of the opencode-mode right sidebar, matching
// opencode (routes/session/sidebar.tsx). The sidebar shows only when the
// terminal is at least sidebarMinWidth columns wide, so a narrow terminal
// falls back to the single-column layout.
const (
	sidebarWidth    = 42
	sidebarMinWidth = 120
	// opencodeLeftMargin is the left padding on opencode's main column
	// (routes/session paddingLeft=2), applied to the whole main body.
	opencodeLeftMargin = 2
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
	left := lipgloss.NewStyle().Foreground(ocMutedCol)
	right := lipgloss.NewStyle().Foreground(ocTextCol).Bold(true)
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
	// Every style carries the panel background so text doesn't punch holes in the
	// filled panel column; opencode's exact text/muted colors for readability.
	head := lipgloss.NewStyle().Bold(true).Foreground(ocTextCol).Background(ocPanelBg)
	dim := lipgloss.NewStyle().Foreground(ocMutedCol).Background(ocPanelBg)

	title := strings.TrimSpace(m.sessTitle)
	if title == "" {
		title = filepath.Base(cwd()) // untitled session: fall back to the working dir
	}

	var b strings.Builder
	b.WriteString(head.Render(truncLine(title, sidebarWidth-4)) + "\n\n")

	// Context: tokens used, share of the window, spend.
	b.WriteString(head.Render("Context") + "\n")
	u := m.agent.Usage()
	b.WriteString(dim.Render(fmt.Sprintf("%s tokens", fmtTok(u.PromptTokens+u.CompletionTokens))) + "\n")
	if m.agent.ContextLimit > 0 {
		pct := agent.EstimateTokens(m.agent.Messages) * 100 / m.agent.ContextLimit
		b.WriteString(dim.Render(fmt.Sprintf("%d%% used", pct)) + "\n")
	}
	if cost, ok := m.sessionCost(); ok {
		b.WriteString(dim.Render(fmt.Sprintf("$%.2f spent", cost)) + "\n\n")
	} else {
		b.WriteString(dim.Render("$0.00 spent") + "\n\n")
	}

	// LSP status.
	b.WriteString(head.Render("LSP") + "\n")
	b.WriteString(dim.Render(m.lspSummary()) + "\n")

	// Top content (title + Context + LSP), clipped if the sidebar is very short.
	top := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	bullet := lipgloss.NewStyle().Foreground(ocSuccessCol).Background(ocPanelBg)
	footer := bullet.Render("• ") + head.Render("whip") + dim.Render(" "+Version)

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
	// opencode's sidebar is set apart by a panel background (no border). Fill the
	// whole column with the panel shade so it reads as a distinct panel.
	col := lipgloss.NewStyle().
		Width(sidebarWidth).
		PaddingLeft(2).
		PaddingRight(2).
		Background(ocPanelBg)
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
	elem := lipgloss.NewStyle().Background(ocElementBg)
	bar := lipgloss.NewStyle().Foreground(ocAgentCol).Background(ocElementBg).Render("┃")
	row := func(content string) string { return elem.Width(width).Render(content) }
	var b strings.Builder
	b.WriteString(row(bar) + "\n") // paddingTop (bar continues down the whole box)
	for _, ln := range strings.Split(inner, "\n") {
		b.WriteString(row(bar+elem.Render("  "+ln)) + "\n")
	}
	b.WriteString(row(bar) + "\n") // padding below the input, above the meta row
	// model/mode row: mode in the agent color, model in text, provider muted.
	agent := lipgloss.NewStyle().Foreground(ocAgentCol).Background(ocElementBg)
	txt := lipgloss.NewStyle().Foreground(ocTextCol).Background(ocElementBg)
	muted := lipgloss.NewStyle().Foreground(ocMutedCol).Background(ocElementBg)
	meta := agent.Render(m.ocModeLabel()) + muted.Render(" · ") + txt.Render(m.modelName) + muted.Render("  "+m.provName)
	b.WriteString(row(bar+elem.Render("  ")+meta) + "\n")
	// Soft bottom edge: a ╹ tail then a ▀ line the SAME color as the box fill, so
	// it reads as the box's rounded bottom rather than a bright bar.
	shadow := lipgloss.NewStyle().Foreground(ocElementBg)
	b.WriteString(lipgloss.NewStyle().Foreground(ocAgentCol).Render("╹") + shadow.Render(strings.Repeat("▀", max(width-1, 0))))
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
	panel := lipgloss.NewStyle().Background(ocPanelBg)
	bar := lipgloss.NewStyle().Foreground(ocAgentCol).Background(ocPanelBg).Render("┃")
	txt := lipgloss.NewStyle().Foreground(ocTextCol).Background(ocPanelBg)
	lines := strings.Split(wrap(text, width-3), "\n")
	rows := append([]string{""}, lines...) // blank padding row above
	rows = append(rows, "")                // blank padding row below
	var b strings.Builder
	for i, ln := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		content := bar // opencode draws the left bar on every card row, padding rows included
		if ln != "" {
			content = bar + txt.Render("  "+ln) // two spaces after the bar
		}
		b.WriteString(panel.Width(width).Render(content)) // fill the row to width with the panel bg
	}
	return b.String()
}

// opencodeStatus renders opencode's session footer: the working directory on
// the left, and "{tokens} ({pct%})  ctrl+p commands" on the right. Themed with
// whip's dim style; the +2 main-column margin is applied by View().
func (m *model) opencodeStatus() string {
	muted := lipgloss.NewStyle().Foreground(ocMutedCol)
	txt := lipgloss.NewStyle().Foreground(ocTextCol)
	// right side: "{tokens} ({pct})  " muted, then "ctrl+p" in text, " commands" muted.
	rightRaw := ""
	if u := m.agent.Usage(); u.PromptTokens+u.CompletionTokens > 0 {
		rightRaw = strings.ToUpper(fmtTok(u.PromptTokens + u.CompletionTokens)) // opencode uses uppercase (15.8K)
		if m.agent.ContextLimit > 0 {
			rightRaw += fmt.Sprintf(" (%d%%)", agent.EstimateTokens(m.agent.Messages)*100/m.agent.ContextLimit)
		}
		rightRaw += "  "
	}
	rightRaw += "ctrl+p commands"
	right := muted.Render(strings.TrimSuffix(rightRaw, "ctrl+p commands")) + txt.Render("ctrl+p") + muted.Render(" commands")
	left := cwd()
	w := max(m.width, 0)
	rightW := lipgloss.Width(rightRaw)
	if lipgloss.Width(left)+rightW+2 > w { // no room: truncate the cwd, keep the right side
		left = truncLine(left, max(w-rightW-2, 0))
	}
	pad := max(w-lipgloss.Width(left)-rightW-1, 1)
	return muted.Render(" "+left+strings.Repeat(" ", pad)) + right
}

// opencodeThought renders opencode's collapsed reasoning line, "+ Thought:
// {duration}", indented 3 to sit under the assistant column.
func (m *model) opencodeThought(d time.Duration) string {
	warn := lipgloss.NewStyle().Foreground(ocWarnCol)
	return "   " + warn.Render("+ Thought: "+fmtShortDur(d)) // 3-space indent to sit under the assistant column
}

// opencodeAttribution renders opencode's per-response attribution line:
// "▣  {mode} · {model} · {duration}", indented 3 to sit under the assistant body.
func (m *model) opencodeAttribution(d time.Duration) string {
	agent := lipgloss.NewStyle().Foreground(ocAgentCol)
	txt := lipgloss.NewStyle().Foreground(ocTextCol)
	muted := lipgloss.NewStyle().Foreground(ocMutedCol)
	return "   " + agent.Render("▣") + txt.Render("  "+m.ocModeLabel()) + // 3-space indent under the assistant column
		muted.Render(" · "+m.modelName+" · "+fmtShortDur(d))
}

// fmtShortDur formats a duration the way opencode does: "173ms" under a second,
// otherwise "2.4s".
func fmtShortDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
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
	invalidateMDRenderer() // opencode markdown style differs; rebuild on mode change
	if mode == opencodeMode {
		m.uiMode = opencodeMode
		ocActive = true
		m.input.Prompt = "" // opencodePrompt supplies the ┃ bar per line
		m.input.Placeholder = "Ask anything…"
		// Fill the textarea with the element background so the input box reads as
		// a filled panel (opencode's prompt box).
		elem := lipgloss.NewStyle().Background(ocElementBg)
		m.input.FocusedStyle.Text = elem
		m.input.FocusedStyle.CursorLine = elem
		m.input.FocusedStyle.Placeholder = dimStyle.Background(ocElementBg)
		m.input.BlurredStyle.Text = elem
		m.input.BlurredStyle.Placeholder = dimStyle.Background(ocElementBg)
	} else {
		m.uiMode = ""
		ocActive = false
		m.input.Prompt = "┃ "
		m.input.Placeholder = whipPlaceholder
		m.input.FocusedStyle.Text = lipgloss.NewStyle()
		m.input.FocusedStyle.CursorLine = lipgloss.NewStyle()
		m.input.FocusedStyle.Placeholder = dimStyle
		m.input.BlurredStyle.Text = lipgloss.NewStyle()
		m.input.BlurredStyle.Placeholder = dimStyle
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
