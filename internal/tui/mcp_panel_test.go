package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/mcp"
)

// panelMCPModel builds a headless model with a started MCP manager, the
// OnChange wiring Run installs, and a cfg whose Save lands in a scratch
// WHIP_HOME (mcpSetImport persists there).
func panelMCPModel(t *testing.T, cfgs, blocked map[string]mcp.ServerConfig) *model {
	t.Helper()
	t.Setenv("WHIP_HOME", t.TempDir())
	m := tasksModel("http://unused")
	m.cfg = &config.Config{}
	if cfgs != nil {
		m.mcpMgr = mcp.NewManager(cfgs)
		m.mcpMgr.SetBlocked(blocked)
		m.mcpMgr.SetOnChange(func() { m.agent.SetMCPTools(m.mcpMgr.Tools()) })
		m.mcpMgr.Start(context.Background())
	}
	return m
}

// TestBuildMCPRows pins the panel's row assembly: the two source toggles
// lead, then live servers, then the policy-blocked (untoggleable) rows.
func TestBuildMCPRows(t *testing.T) {
	off := false
	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"own": {Command: []string{"true"}, Source: "whip"},
	}, map[string]mcp.ServerConfig{
		"gate": {Command: []string{"true"}, Enabled: &off, Note: "blocked by mcpImport config"},
	})
	m.cfg.MCPImport = &config.MCPImport{Codex: &config.MCPImportSource{Enabled: &off}}

	rows := m.buildMCPRows()
	if len(rows) != 4 {
		t.Fatalf("expected 4 rows (2 sources + 1 live + 1 blocked), got %d: %+v", len(rows), rows)
	}
	if !rows[0].source || rows[0].name != "claude" || !rows[0].on {
		t.Errorf("claude row: %+v", rows[0])
	}
	if !rows[1].source || rows[1].name != "codex" || rows[1].on {
		t.Errorf("codex row should be off: %+v", rows[1])
	}
	if rows[2].source || rows[2].name != "own" {
		t.Errorf("server row: %+v", rows[2])
	}
	if !rows[3].disabled || rows[3].name != "gate" {
		t.Errorf("blocked row should be untoggleable: %+v", rows[3])
	}
}

// TestPanelMCPToggleImportOffLive pins the headline behavior: flipping codex
// imports off in the palette disconnects that source's servers — no restart —
// persists the gate, and never touches whip's own servers. The Source values
// are ABSOLUTE PATHS, the shape setSource stamps at discovery in production —
// the short labels this test previously injected made the toggle a silent
// no-op in the field while passing here.
func TestPanelMCPToggleImportOffLive(t *testing.T) {
	dir := t.TempDir()
	// A real codex fixture at the discovered path: after the toggle-off,
	// discovery re-reads it and lands the server in the blocked list (visible,
	// not silent). This also isolates the test from the developer's real
	// ~/.codex/config.toml / ~/.claude.json.
	codexDir := filepath.Join(dir, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	codexAbs := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(codexAbs, []byte("[mcp_servers.fromcodex]\ncommand = [\"true\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origCodex, origClaude := mcp.CodexPath, mcp.ClaudeGlobalPath
	mcp.CodexPath = func() string { return codexAbs }
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(dir, "absent-claude.json") }
	t.Cleanup(func() { mcp.CodexPath, mcp.ClaudeGlobalPath = origCodex, origClaude })

	m := panelMCPModel(t, map[string]mcp.ServerConfig{
		"fromcodex": {Command: []string{"true"}, Source: codexAbs},
		"own":       {Command: []string{"true"}, Source: "whip"},
	}, nil)

	// Drive the palette: select the "MCPs" row directly (filtering for "mcp"
	// also matches other rows by subsequence), drill in, move to the codex
	// row, toggle it off.
	m.openPalette()
	for i, it := range m.palette.items {
		if it.title == "MCPs" {
			m.palette.idx = i
			break
		}
	}
	tm, _ := m.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	mdl := tm.(*model)
	pp := mdl.palette.top()
	if pp == nil || pp.kind != panelMCP {
		t.Fatalf("enter should push the MCPs panel, got %+v", pp)
	}
	for pp.mcps[pp.midx].name != "codex" {
		tm, _ = mdl.paletteKey(tea.KeyMsg{Type: tea.KeyDown})
		mdl = tm.(*model)
		pp = mdl.palette.top()
	}
	tm, _ = mdl.paletteKey(tea.KeyMsg{Type: tea.KeyEnter})
	mdl = tm.(*model)

	// The codex-imported server is gone from the manager; whip's own survives.
	for _, s := range mdl.mcpMgr.Statuses() {
		if s.Name == "fromcodex" {
			t.Fatalf("toggled-off server still live: %+v", s)
		}
	}
	found := false
	for _, s := range mdl.mcpMgr.Statuses() {
		if s.Name == "own" {
			found = true
		}
	}
	if !found {
		t.Fatal("whip-owned server must survive a source toggle")
	}
	// The transcript claims the actual disconnect count — the production
	// no-op (0 removed while servers stay live) is caught here, not masked.
	var sb strings.Builder
	for _, b := range mdl.blocks {
		sb.WriteString(b.text + "\n")
	}
	joined := sb.String()
	if !strings.Contains(joined, "codex imports: off") || !strings.Contains(joined, "1 server(s) disconnected") {
		t.Fatalf("transcript should report 1 disconnected server, got %q", joined)
	}
	// And the toggled-off server shows up as blocked (visible, not silent).
	blockedFound := false
	for _, s := range mdl.mcpMgr.Blocked() {
		if s.Name == "fromcodex" {
			blockedFound = true
		}
	}
	if !blockedFound {
		t.Fatal("toggled-off server should appear in the blocked list")
	}
	// The gate persisted to disk.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.MCPImport == nil || reloaded.MCPImport.Codex == nil ||
		reloaded.MCPImport.Codex.Enabled == nil || *reloaded.MCPImport.Codex.Enabled {
		t.Fatalf("codex import gate should persist as off, got %+v", reloaded.MCPImport)
	}
	// The panel rebuilt with the codex row flipped off.
	pp = mdl.palette.top()
	for _, row := range pp.mcps {
		if row.source && row.name == "codex" && row.on {
			t.Errorf("codex row should render off after the toggle: %+v", row)
		}
	}
}

// TestPaletteMCPRowReplacesServersEntry pins the root-list wiring: the old
// "MCP servers" run-row is gone; "MCPs" drills into the panel.
func TestPaletteMCPRowReplacesServersEntry(t *testing.T) {
	m := compactCmdModel()
	m.openPalette()
	seen := false
	for _, it := range m.palette.all {
		if it.title == "MCP servers" {
			t.Fatal("the MCP servers row should be renamed to MCPs")
		}
		if it.title == "MCPs" {
			seen = true
			if it.panel == nil {
				t.Fatal("the MCPs row must open a panel, not run /mcp")
			}
		}
	}
	if !seen {
		t.Fatal("no MCPs row in the palette")
	}
}

// TestMCPSetImportEnableDiscoversLive pins the enable half: a source toggled
// on re-runs discovery and connects the admitted server without a restart.
func TestMCPSetImportEnableDiscoversLive(t *testing.T) {
	// A codex config fixture with one server; discovery reads it when the
	// gate flips on. The manager starts with codex imports off.
	dir := t.TempDir()
	t.Setenv("WHIP_HOME", dir)
	codexCfg := filepath.Join(dir, "codex.toml")
	if err := os.WriteFile(codexCfg, []byte("[mcp_servers.late]\ncommand = [\"true\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origCodex, origClaude := mcp.CodexPath, mcp.ClaudeGlobalPath
	mcp.CodexPath = func() string { return codexCfg }
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(dir, "absent-claude.json") }
	t.Cleanup(func() { mcp.CodexPath, mcp.ClaudeGlobalPath = origCodex, origClaude })

	off := false
	m := tasksModel("http://unused")
	m.cfg = &config.Config{MCPImport: &config.MCPImport{Codex: &config.MCPImportSource{Enabled: &off}}}
	m.mcpMgr = mcp.NewManager(nil)
	m.mcpMgr.SetOnChange(func() { m.agent.SetMCPTools(m.mcpMgr.Tools()) })
	m.mcpMgr.Start(context.Background())

	m.mcpSetImport("codex", true)

	// The server is discovered and starts connecting (give the lifecycle
	// goroutine a moment; the connect itself fails on `true`, but the row
	// appearing proves the live re-discovery).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range m.mcpMgr.Statuses() {
			if s.Name == "late" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("enabling codex imports never discovered the late server: %+v", m.mcpMgr.Statuses())
}

// TestMCPSetImportKeepsFilters pins that toggling a source with only/exclude
// filters set doesn't drop them.
func TestMCPSetImportKeepsFilters(t *testing.T) {
	// Isolate discovery from the developer's real ~/.codex/config.toml and
	// ~/.claude.json: toggling codex on must find nothing here.
	dir := t.TempDir()
	origCodex, origClaude := mcp.CodexPath, mcp.ClaudeGlobalPath
	mcp.CodexPath = func() string { return filepath.Join(dir, "absent-codex.toml") }
	mcp.ClaudeGlobalPath = func() string { return filepath.Join(dir, "absent-claude.json") }
	t.Cleanup(func() { mcp.CodexPath, mcp.ClaudeGlobalPath = origCodex, origClaude })

	off := false
	m := panelMCPModel(t, nil, nil)
	m.cfg.MCPImport = &config.MCPImport{Codex: &config.MCPImportSource{Enabled: &off, Exclude: []string{"node_repl"}}}

	m.mcpSetImport("codex", true)

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	src := reloaded.MCPImport.Codex
	if src.Enabled == nil || !*src.Enabled {
		t.Errorf("enabled should persist, got %+v", src)
	}
	if len(src.Exclude) != 1 || src.Exclude[0] != "node_repl" {
		t.Errorf("exclude filter must survive the toggle, got %v", src.Exclude)
	}
	// The toggle note is one of the last two transcript blocks (the status
	// table follows it when discovery finds anything; here it finds nothing).
	var sb strings.Builder
	for _, b := range m.blocks[max(0, len(m.blocks)-2):] {
		sb.WriteString(b.text)
	}
	joined := sb.String()
	if !strings.Contains(joined, "codex imports: on") {
		t.Errorf("transcript should note the toggle, got %q", joined)
	}
}
