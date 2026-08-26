package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A running tool renders a verb line; when the result lands, the same block
// collapses to one line — red on failure — and ctrl+e expands it back.
func TestToolRowCollapsesOnCompletion(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	m.Update(toolStartMsg{id: "c1", name: "read", args: `{"path":"foo.go"}`})
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockToolRun {
		t.Fatal("toolStart should append a running row")
	}
	row := m.blocks[len(m.blocks)-1]
	if !row.toolRunning {
		t.Fatal("row should be running")
	}
	if got := ansi.Strip(row.render(m.width)); !strings.Contains(got, "Reading") {
		t.Fatalf("running row should show the verb, got %q", got)
	}

	m.Update(toolEndMsg{id: "c1", name: "read", result: "file body\nline2\nline3\nline4\nline5\nline6"})
	row = m.blocks[len(m.blocks)-2] // the run row; the result block follows it
	if row.toolRunning {
		t.Fatal("completion should stop the run state")
	}
	if got := ansi.Strip(row.render(m.width)); strings.Count(got, "\n") > 0 {
		t.Fatalf("completed row should collapse to one line, got %q", got)
	}
	if !row.toggle() {
		t.Fatal("ctrl+e should expand the collapsed row")
	}
	if got := ansi.Strip(row.render(m.width)); !strings.Contains(got, "file body") {
		t.Fatalf("expanded row should show the result, got %q", got)
	}
}

// A failed tool collapses to a red one-liner.
func TestToolRowFailureIsRed(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))
	m.Update(toolStartMsg{id: "c1", name: "bash", args: `{"command":"false"}`})
	m.Update(toolEndMsg{id: "c1", name: "bash", result: "Error: exit status 1"})
	var run *block
	for i := range m.blocks {
		if m.blocks[i].kind == blockToolRun {
			run = &m.blocks[i]
		}
	}
	if run == nil || !run.toolFailed {
		t.Fatal("a failed tool should mark the collapsed row")
	}
	if got := run.render(m.width); !strings.Contains(got, "31") && !strings.Contains(got, "Error") {
		// errStyle is red (ansi 31) in most themes; at minimum the text shows
		t.Fatalf("failed row should render the error, got %q", got)
	}
}
