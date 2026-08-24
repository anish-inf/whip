package tui

import (
	"strings"
	"testing"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/llm"
)

// statusModel builds a model with an agent so statusView has data.
func statusModel() *model {
	m := newGrowModel()
	m.agent = &agent.Agent{}
	return m
}

// The status line always renders below the input with directory, model
// (effort), provider, and session token spend — regardless of scroll or state.
func TestStatusLineAlwaysShown(t *testing.T) {
	m := statusModel()
	m.modelName = "kimi-k3-fast"
	m.provName = "inference"
	m.agent.Effort = "high"
	m.agent.AddUsage(llm.Usage{PromptTokens: 45230, CompletionTokens: 3120})

	v := m.View()
	for _, want := range []string{"kimi-k3-fast (high)", "inference", "45.2k", "3.1k"} {
		if !strings.Contains(v, want) {
			t.Errorf("status line should show %q\n--- view tail ---\n%s", want, tailLines(v, 6))
		}
	}
	// the directory is present (compacted to its last segments)
	if !strings.Contains(v, "loopy-three") {
		t.Errorf("status line should show the working directory\n%s", tailLines(v, 6))
	}
}

// With no usage yet the spend reads zero, and effort off drops the parens.
func TestStatusLineDefaults(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"

	v := m.View()
	if !strings.Contains(v, "0/0 tok") {
		t.Errorf("empty session should read 0/0 tok\n%s", tailLines(v, 6))
	}
	if strings.Contains(v, "m (") {
		t.Errorf("effort off should not add parens\n%s", tailLines(v, 6))
	}
	if !strings.Contains(v, "  m   p  ") && !strings.Contains(v, " m   p ") {
		t.Errorf("bare model and provider should appear\n%s", tailLines(v, 6))
	}
}

// Cached tokens surface in the spend segment.
func TestStatusLineShowsCached(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	u := llm.Usage{PromptTokens: 10000, CompletionTokens: 500}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 4000}
	m.agent.AddUsage(u)

	if got := m.statusView(); !strings.Contains(got, "10.0k(4.0k)/500 tok") {
		t.Errorf("cached tokens should show in the spend: %q", got)
	}
}

// The status line is the last content row before the bottom padding, sitting
// below the input even when the esc/quit warnings or completion menu show.
func TestStatusLineBelowInputAndWarnings(t *testing.T) {
	m := statusModel()
	m.modelName = "m"
	m.provName = "p"
	m.escClr = true // draft-clear warning armed

	v := m.View()
	lines := strings.Split(strings.TrimRight(v, "\n"), "\n")
	var inputRow, statusRow int
	for i, l := range lines {
		if strings.Contains(l, "Ask loopy anything") {
			inputRow = i
		}
		if strings.Contains(l, "0/0 tok") {
			statusRow = i
		}
	}
	if statusRow <= inputRow {
		t.Fatalf("status line should sit below the input (input=%d status=%d)\n%s", inputRow, statusRow, v)
	}
}

// tailLines returns the last n lines of s, for failure output.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
