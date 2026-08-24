package tui

import (
	"strings"
	"testing"

	"github.com/context-labs/loopy/internal/mcp"
)

func TestDoctorFreshSession(t *testing.T) {
	m := tasksModel("http://unused")
	m.sysPrompt = "You are an expert coding assistant operating inside loopy. "
	disabled := false
	m.mcpMgr = mcp.NewManager(map[string]mcp.ServerConfig{
		"off":     {Command: []string{"true"}, Enabled: &disabled},
		"invalid": {},
	})
	out := m.doctorReport()
	for _, want := range []string{
		"context audit",
		"system prompt (base)",
		"skills (",
		"tool schemas (",
		"mcp: off",
		"disabled",
		"mcp: invalid",
		"TOTAL injected",
		"Trim:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDoctorCommandWired(t *testing.T) {
	m := tasksModel("http://unused")
	m.command("/context-doctor")
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "context audit") {
		t.Fatalf("/context-doctor produced no report: %+v", m.blocks)
	}
	// No /doctor shorthand: the full name is the command.
	before := len(m.blocks)
	m.command("/doctor")
	if !strings.Contains(m.blocks[len(m.blocks)-1].text, "unknown command") {
		t.Error("/doctor should not exist as a shorthand")
	}
	_ = before
}

func TestTok(t *testing.T) {
	if tok(350) != "350" || tok(4848) != "4.8k" {
		t.Errorf("tok: %q %q", tok(350), tok(4848))
	}
}
