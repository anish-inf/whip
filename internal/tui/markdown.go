package tui

import (
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
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
// sequence, spaces), optionally closed by a final SGR reset. The reset is
// kept (captured group) so a line's styling never bleeds into the next block.
var padStripRE = regexp.MustCompile(`(?:\x1b\[[0-9;]*m[ \t]*)+(\x1b\[0*m)?$`)

// stripLinePadding removes glamour's right-padding: it pads every line to
// the full render width with individually styled spaces, which bloats the
// transcript 10-20x and breaks terminal select/copy. Leading indentation and
// styled content are untouched.
func stripLinePadding(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = padStripRE.ReplaceAllString(l, "$1")
	}
	return strings.Join(lines, "\n")
}

var (
	mdMu          sync.Mutex
	mdAtWidth     int
	mdRendererC   *glamour.TermRenderer
	mdRendererErr bool // style init failed once: don't retry per message
)

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
	st := styles.DarkStyleConfig
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

// renderAssistant renders one assistant text segment for the transcript:
// markdown when it parses, else the raw text. The "● " prefix is applied by
// the caller so it stays out of the markdown flow.
func renderAssistant(s string, width int) string {
	if mdRenderer(width) == nil {
		return s // no renderer: plain text
	}
	return renderMarkdown(s, width)
}
