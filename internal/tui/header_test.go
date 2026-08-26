package tui

import (
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/llm"
)

func TestFmtTok(t *testing.T) {
	for in, want := range map[int]string{
		0: "0", 999: "999", 1000: "1.0k", 12345: "12.3k",
		1_000_000: "1.0M", 1_234_567: "1.2M",
	} {
		if got := fmtTok(in); got != want {
			t.Errorf("fmtTok(%d) = %q, want %q", in, got, want)
		}
	}
}

// The always-on header shows model, effort, and session token usage.
func TestHeaderShowsUsage(t *testing.T) {
	m := compactCmdModel()
	m.agent = agent.New(llm.New("https://x", "k"), "kimi-k3-fast", 100, "sys")
	m.agent.ContextLimit = 100000
	m.follow = true
	m.agent.AddUsage(llm.Usage{PromptTokens: 12345, CompletionTokens: 678})
	m.agent.AddUsage(llm.Usage{ // cached tokens accumulate through the details struct
		PromptTokens: 1,
		PromptTokensDetails: &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: 4000},
	})
	m.width = 200 // wide enough for the full usage block (header truncates the tail)
	head, _, _ := strings.Cut(m.View(), "\n")
	for _, want := range []string{"kimi-k3-fast", "⚡ off", "12.3k in", "4.0k cached", "678 out", "% ctx"} {
		if !strings.Contains(head, want) {
			t.Errorf("header missing %q: %q", want, head)
		}
	}
}

// No usage reported yet: the header omits the token block entirely.
func TestHeaderOmitsUsageUntilReported(t *testing.T) {
	m := compactCmdModel()
	m.width = 120
	head, _, _ := strings.Cut(m.View(), "\n")
	if strings.Contains(head, "⣿") {
		t.Errorf("no usage should mean no token block: %q", head)
	}
	if !strings.Contains(head, "⚡ off") || !strings.Contains(head, "kimi-k3-fast") {
		t.Errorf("model and effort always show: %q", head)
	}
}
