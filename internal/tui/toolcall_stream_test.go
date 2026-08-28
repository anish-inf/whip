package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A tool call streaming from the model renders a queued row before execution;
// when execution starts, the same tool-call id's running row replaces it
// rather than appending a duplicate.
func TestToolCallQueuedRowReplacedOnStart(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	m.Update(toolCallMsg{id: "c1", name: "bash", args: `{"command":"make"}`})
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != blockToolQueued {
		t.Fatal("toolCallMsg should append a queued row")
	}
	row := m.blocks[len(m.blocks)-1]
	if !strings.Contains(ansi.Strip(row.render(m.width)), "bash") {
		t.Fatalf("queued row should name the tool, got %q", ansi.Strip(row.render(m.width)))
	}

	before := len(m.blocks)
	m.Update(toolStartMsg{id: "c1", name: "bash", args: `{"command":"make"}`})
	if len(m.blocks) != before {
		t.Fatalf("toolStart should replace the queued row, not append (before=%d after=%d)", before, len(m.blocks))
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != blockToolRun || !last.toolRunning {
		t.Fatalf("after toolStart the row should be a running row, got kind=%v running=%v", last.kind, last.toolRunning)
	}
}

// onToolCall fires per args delta with the cumulative snapshot; each delta for
// the same tool-call id must update the queued row in place, not stack one row
// per fragment (long commands would flood the transcript otherwise).
func TestToolCallQueuedRowUpdatesInPlace(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 24))

	deltas := []string{
		`{"command": "`,
		`{"command": "mkdir`,
		`{"command": "mkdir -p`,
		`{"command": "mkdir -p ~/.config/k9s"}`,
	}
	for _, args := range deltas {
		m.Update(toolCallMsg{id: "c1", name: "bash", args: args})
	}

	var queued int
	for _, b := range m.blocks {
		if b.kind == blockToolQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("expected 1 queued row after %d deltas, got %d", len(deltas), queued)
	}
	row := m.blocks[len(m.blocks)-1]
	got := ansi.Strip(row.render(m.width))
	if !strings.Contains(got, deltas[len(deltas)-1]) {
		t.Fatalf("queued row should show the latest args snapshot, got %q", got)
	}

	// a second tool call streaming concurrently gets its own row
	m.Update(toolCallMsg{id: "c2", name: "read", args: `{"path":"/tmp/x"}`})
	if n := len(m.blocks); n != 2 {
		t.Fatalf("a different tool-call id should append its own row, blocks=%d", n)
	}
}
