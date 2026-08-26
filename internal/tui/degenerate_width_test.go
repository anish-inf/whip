package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A degenerate WindowSizeMsg (1–4 cols, which a tmux/PTY handshake can emit
// transiently) must not collapse blocks into a one-char-per-line strip.
// blockTool/blockText wrap with no floor, so width 1 renders one character
// per row and — because renders cache per width and only a width *change*
// reflows — the strip persists. refreshVP floors the render width at
// minRenderWidth so the transcript stays readable.
func TestDegenerateWidthDoesNotStrip(t *testing.T) {
	m := compactCmdModel()
	m.appendAssistant("Here is the assistant reply that must stay readable.")
	m.append(dimStyle.Render("  - Liked the post (815 Likes. Liked)"))

	// A bogus narrow size arrives (tmux handshake hiccup).
	tm, _ := m.Update(mkWinSize(1, 30))
	m = tm.(*model)

	// The bug: text collapses to one character per line. The assistant "● "
	// marker is legitimately a single glyph, so look for a *run* of 1-char
	// lines — the signature of the strip — rather than any single short line.
	lines := strings.Split(ansi.Strip(m.vp.View()), "\n")
	var run int
	for _, l := range lines {
		if ansi.StringWidth(strings.TrimRight(l, " ")) == 1 {
			run++
			if run >= 3 {
				t.Fatalf("text collapsed into a one-char-per-line strip at degenerate width (run of %d): %q", run, lines)
			}
		} else {
			run = 0
		}
	}
}

// After the transient narrow size, a resize to the real width still reflows
// correctly (the floor never becomes a stale cached baseline).
func TestDegenerateWidthThenRealResizeReflows(t *testing.T) {
	m := compactCmdModel()
	m.append(dimStyle.Render("  some tool output line that is long enough to wrap at eighty cols"))

	tm, _ := m.Update(mkWinSize(1, 30)) // degenerate
	m = tm.(*model)
	tm, _ = m.Update(mkWinSize(80, 30)) // real width
	m = tm.(*model)

	var maxW int
	for l := range strings.SplitSeq(ansi.Strip(m.vp.View()), "\n") {
		if w := ansi.StringWidth(l); w > maxW {
			maxW = w
		}
	}
	if maxW > 80 {
		t.Fatalf("line exceeds real width after reflow: %d", maxW)
	}
}
