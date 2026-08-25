package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/context-labs/whip/internal/llm"
)

// ctrl+k clears the conversation exactly as if /clear ran — messages reset to
// the system prompt, transcript blocks dropped, session detached — and the
// textarea's default delete-after-cursor binding must not shadow it.
func TestCtrlKClear(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendRaw(blockText, "hello world")
	if got := len(m.agent.Messages); got != 1 {
		t.Fatalf("expected just the system prompt, got %d messages", got)
	}
	m.agent.Messages = append(m.agent.Messages,
		llm.Message{Role: "user", Content: "hi", Authored: true})

	// a draft in the input box must survive — ctrl+k clears the CHAT
	m.input.SetValue("draft")

	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = tm.(*model)

	if got := len(m.agent.Messages); got != 1 {
		t.Fatalf("ctrl+k should reset messages to the system prompt, got %d", got)
	}
	// the old transcript blocks are gone; only the cleared notice remains
	if m.msgBlock != nil {
		t.Fatal("ctrl+k should drop the pending message block")
	}
	if len(m.blocks) != 1 || !strings.Contains(ansi.Strip(m.blocks[0].render(m.width)), "(conversation cleared)") {
		t.Fatalf("expected only the cleared notice block, got %d blocks", len(m.blocks))
	}
	if m.sessionID != "" {
		t.Fatalf("ctrl+k should detach the session, got %q", m.sessionID)
	}
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("ctrl+k must not delete-after-cursor in the input, got %q", got)
	}
	if out := ansi.Strip(m.View()); !strings.Contains(out, "(conversation cleared)") {
		t.Fatalf("missing cleared notice in transcript: %q", out)
	}
}
