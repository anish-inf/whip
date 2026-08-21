package tui

import (
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
)

// renderMarkdown renders assistant message text as rich terminal markdown
// (glamour): headings, bold/italic, lists, fenced code blocks, tables.
// Falls back to the raw input when parsing fails — a degraded transcript is
// never worth a broken one.
//
// The style is a hardcoded dark variant (never WithEnvironmentConfig): an
// OSC background query mid-session can hang over mosh/tmux, and the TUI
// already commits to plain ANSI colors everywhere else.
func renderMarkdown(s string, width int) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	width = max(width, 8) // glamour treats width<=0 as its ~80-col default
	out, err := mdRenderer(width).Render(s)
	if err != nil {
		return s
	}
	return wrapWideLines(stripLinePadding(strings.Trim(out, "\n")), width)
}

// wrapWideLines hard-wraps any rendered line still wider than width.
// Glamour never breaks code-fence or table content, so a long line overflows
// the terminal; ansi.Hardwrap is cell- and escape-aware (styles stay intact).
func wrapWideLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if ansi.StringWidth(l) > width {
			lines[i] = ansi.Hardwrap(l, width, true) // ANSI-aware, breaks mid-word
		}
	}
	return strings.Join(lines, "\n")
}

// padStripRE matches glamour's right-padding at end of line: runs of (SGR
// sequence [empty params allowed — bare \x1b[m], spaces), optionally closed
// by a final SGR reset. The reset is kept (captured group) so a line's
// styling never bleeds into the next block.
var padStripRE = regexp.MustCompile(`(?:\x1b\[[0-9;]*m[ \t]*)+(\x1b\[[0-9;]*m)?$`)

// stripLinePadding removes glamour's right-padding: it pads every line to
// the full render width with individually styled spaces, which bloats the
// transcript 10-20x and breaks terminal select/copy. Lines whose visible
// content is empty (blank separators) become truly empty — no styled blank
// rows. Leading indentation and styled content are untouched.
func stripLinePadding(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = padStripRE.ReplaceAllString(l, "$1")
		if ansi.StringWidth(l) == 0 || strings.TrimSpace(ansi.Strip(l)) == "" {
			l = "" // blank separator line: drop any leftover styling entirely
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

var (
	mdMu           sync.Mutex
	mdAtWidth      int
	mdRendererC    *glamour.TermRenderer
	mdRendererErr  bool // style init failed once: don't retry per message
	mdLight        bool // light terminal background detected (set at startup)
	mdStyleChecked bool
)

// SetLightTheme records the terminal's background and drops the cached
// renderer so the next render builds with the matching style. Called from
// Run once the background is known (OSC query result or heuristic).
func SetLightTheme(light bool) {
	mdMu.Lock()
	mdLight, mdStyleChecked = light, true
	mdRendererC, mdAtWidth = nil, 0
	mdMu.Unlock()
}

// mdStyle picks the glamour style for the detected background.
func mdStyle() glamouransi.StyleConfig {
	if mdLight {
		return styles.LightStyleConfig
	}
	return styles.DarkStyleConfig
}

// mdRenderer returns a cached renderer per width (glamour builds a
// style-traversed renderer per Render call otherwise).
func mdRenderer(width int) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()
	if mdRendererErr {
		return nil
	}
	if mdRendererC != nil && mdAtWidth == width {
		return mdRendererC
	}
	st := mdStyle()
	margin := uint(2)
	st.Document.Margin = &margin
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(st),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(), // streamed text keeps its line breaks verbatim
	)
	if err != nil { // style is built-in; only reachable on a broken build
		mdRendererErr = true
		return nil
	}
	mdRendererC, mdAtWidth = r, width
	return r
}

// bareSGR is the empty SGR escape (\x1b[m) lipgloss' Width().Render appends
// before its right-padding; some terminals render the empty parameter list
// inconsistently, and the styled pad shows up as visual smear. Normalize it
// to a proper reset.
var bareSGR = strings.NewReplacer("\x1b[m", "\x1b[0m")

// sanitizeView cleans one rendered screen: bare SGR escapes become real
// resets and trailing style+space tails (lipgloss/viewport padding) are
// trimmed from each line.
func sanitizeView(s string) string {
	s = bareSGR.Replace(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = padStripRE.ReplaceAllString(l, "$1")
	}
	return strings.Join(lines, "\n")
}

// renderAssistant renders one assistant text segment for the transcript:
// markdown when it parses, else the raw text. The "● " prefix is applied by
// the caller so it stays out of the markdown flow.
func renderAssistant(s string, width int) string {
	if mdRenderer(width) == nil {
		return s // no renderer: plain text
	}
	return renderMarkdown(s, width)
}
