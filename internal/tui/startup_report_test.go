package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/mcp"
)

// TestStartupReportSkillsAndWarnings: the report names loaded skills, flags a
// description that exceeds maxDesc (truncated in the system prompt), and
// flags a SKILL.md that fails to parse — pi's [Skill conflicts] block.
func TestStartupReportSkillsAndWarnings(t *testing.T) {
	dir := t.TempDir()
	mkSkill := func(name, desc string) {
		d := filepath.Join(dir, ".agents", "skills", name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+desc+"\n---\n"), 0o644)
	}
	mkSkill("good", "fine")
	mkSkill("wordy", strings.Repeat("x", 1100)) // over the spec's 1024
	// A SKILL.md with no frontmatter = parse problem.
	bad := filepath.Join(dir, ".agents", "skills", "broken")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no frontmatter here"), 0o644)

	t.Chdir(dir)

	m := tasksModel("http://unused")
	m.startupReport()
	if len(m.blocks) == 0 {
		t.Fatal("no report rendered")
	}
	out := m.blocks[0].text
	if !strings.Contains(out, "skills: 2 loaded") {
		t.Errorf("missing loaded count:\n%s", out)
	}
	if !strings.Contains(out, "wordy") || !strings.Contains(out, "exceeds 1024") {
		t.Errorf("missing truncation warning:\n%s", out)
	}
	if !strings.Contains(out, "broken") {
		t.Errorf("missing parse problem:\n%s", out)
	}
}

// TestStartupReportMCP: ready/failed/disabled servers render with the right
// glyphs in one line.
func TestStartupReportMCP(t *testing.T) {
	m := tasksModel("http://unused")
	disabled := false
	m.mcpMgr = mcp.NewManager(map[string]mcp.ServerConfig{
		"off":     {Command: []string{"true"}, Enabled: &disabled},
		"invalid": {},
	})
	m.startupReport()
	out := m.blocks[0].text
	if !strings.Contains(out, "mcp:") || !strings.Contains(out, "off ○") || !strings.Contains(out, "invalid ✗") {
		t.Errorf("bad mcp line:\n%s", out)
	}
}

// TestStartupReportSilent: nothing loaded, nothing said.
func TestStartupReportSilent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir()) // no ~/.whip/skills either

	m := tasksModel("http://unused")
	m.startupReport()
	if len(m.blocks) != 0 {
		t.Errorf("expected silence, got %q", m.blocks[0].text)
	}
}

// TestStartupReportUpdateNotice: a pending newer release (spotted by main's
// background check) renders as a notice line naming `whip update`.
func TestStartupReportUpdateNotice(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())

	m := tasksModel("http://unused")
	m.updateLatest = "v0.4.0"
	m.startupReport()
	if len(m.blocks) == 0 {
		t.Fatal("no report rendered")
	}
	out := m.blocks[0].text
	if !strings.Contains(out, "update available: v0.4.0") || !strings.Contains(out, "whip update") {
		t.Errorf("missing update notice:\n%s", out)
	}
}
