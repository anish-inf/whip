package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdownBasics(t *testing.T) {
	out := renderMarkdown("# Title\n\nsome **bold** text\n\n- a\n- b\n\n```go\nfmt.Println()\n```", 80)
	plain := ansi.Strip(out)
	for _, want := range []string{"Title", "bold", "• a", "• b", "fmt.Println()"} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered output missing %q:\n%s", want, plain)
		}
	}
	// bold is styled, not literal asterisks
	if strings.Contains(plain, "**") {
		t.Errorf("markdown markers should be consumed:\n%s", plain)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected ANSI styling in rendered output")
	}
}

func TestRenderMarkdownStripsRightPadding(t *testing.T) {
	out := renderMarkdown("short line", 80)
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 12 {
			t.Errorf("line %d padded to width %d (should be unpadded): %q", i, w, l)
		}
	}
}

func TestRenderMarkdownFallback(t *testing.T) {
	if got := renderMarkdown("", 80); got != "" {
		t.Errorf("empty input should pass through, got %q", got)
	}
	// width<=0 is clamped to the minimum render width, never passed through
	// unwrapped (that was the overflow bug)
	out := renderMarkdown("plain text", 0)
	plain := strings.Join(strings.Fields(ansi.Strip(out)), " ")
	if plain != "plain text" {
		t.Errorf("content must survive the clamp, got %q", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 8 {
			t.Errorf("clamped render must respect width 8: %q", l)
		}
	}
}

func TestRenderMarkdownWrapsToWidth(t *testing.T) {
	long := strings.Repeat("word ", 40)
	out := renderMarkdown(long, 40)
	for i, l := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Errorf("line %d exceeds width 40 (%d): %q", i, w, l)
		}
	}
}

func TestIndentLines(t *testing.T) {
	in := "  first\n\n    second"
	want := "  first\n\n  second"
	if got := indentLines(in, 2); got != want {
		t.Errorf("indentLines:\ngot  %q\nwant %q", got, want)
	}
}

// Assistant segments land in the transcript markdown-rendered with the ●
// marker on the first line and the body aligned under it.
func TestAppendAssistantRendersMarkdown(t *testing.T) {
	m := compactCmdModel()
	m.width = 80
	m.appendAssistant("results:\n\n- **one**\n- two")
	if len(m.blocks) == 0 {
		t.Fatal("no transcript block")
	}
	block := ansi.Strip(m.blocks[0])
	if !strings.HasPrefix(block, "● ") {
		t.Errorf("first line should carry the marker: %q", block)
	}
	if !strings.Contains(block, "• one") || !strings.Contains(block, "• two") {
		t.Errorf("list should be rendered: %q", block)
	}
	if strings.Contains(block, "**") {
		t.Errorf("markdown markers should be consumed: %q", block)
	}
	// continuation segment: no second marker
	m.appendAssistant("more text")
	full := ansi.Strip(strings.Join(m.blocks, "\n"))
	if strings.Count(full, "● ") != 1 {
		t.Errorf("continuation segment must not add a second marker:\n%s", full)
	}
}
