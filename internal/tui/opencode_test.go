package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/lsp"
)

func TestOpencodeLogo(t *testing.T) {
	logo := opencodeLogo()
	lines := strings.Split(logo, "\n")
	if len(lines) != 4 {
		t.Fatalf("logo has %d lines, want 4", len(lines))
	}
	if !strings.Contains(logo, "█") || !strings.Contains(logo, "▀") {
		t.Fatal("logo missing block glyphs")
	}
}

func TestUIModeLabel(t *testing.T) {
	if uiModeLabel(opencodeMode) != "opencode" {
		t.Fatal("opencode label")
	}
	if uiModeLabel("") != "default" {
		t.Fatal("default label")
	}
}

func TestApplyUIMode(t *testing.T) {
	m := &model{}
	m.applyUIMode(opencodeMode)
	if m.uiMode != opencodeMode {
		t.Fatalf("uiMode = %q, want opencode", m.uiMode)
	}
	m.applyUIMode("bogus")
	if m.uiMode != "" {
		t.Fatalf("uiMode = %q, want default", m.uiMode)
	}
}

func TestSidebarVisible(t *testing.T) {
	m := &model{uiMode: opencodeMode}
	m.termWidth = sidebarMinWidth - 1
	if m.sidebarVisible() {
		t.Fatal("narrow terminal should hide the sidebar")
	}
	m.termWidth = sidebarMinWidth
	if !m.sidebarVisible() {
		t.Fatal("wide terminal should show the sidebar")
	}
	m.uiMode = "" // off in default mode regardless of width
	if m.sidebarVisible() {
		t.Fatal("default mode should never show the sidebar")
	}
}

func TestLspSummary(t *testing.T) {
	m := &model{}
	if got := m.lspSummary(); got != "LSPs are disabled" {
		t.Fatalf("no manager = %q", got)
	}
	if got := lspSummaryLine(nil); got != "no servers" {
		t.Fatalf("empty = %q", got)
	}
	got := lspSummaryLine([]lsp.Status{{State: "connected"}, {State: "failed"}})
	if got != "1/2 connected" {
		t.Fatalf("count = %q, want 1/2 connected", got)
	}
	// non-nil manager path (no specs -> no servers)
	m2 := &model{lspMgr: lsp.NewManager(nil)}
	if got := m2.lspSummary(); got != "no servers" {
		t.Fatalf("empty manager = %q", got)
	}
}

func TestSetUIModeSaveError(t *testing.T) {
	// Point HOME at a regular file so the config directory can't be created and
	// cfg.Save() fails; setUIMode must surface the error, not panic.
	f := filepath.Join(t.TempDir(), "home-is-a-file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// WHIP_HOME (pinned by TestMain) wins over HOME; point it under a regular
	// file so MkdirAll fails and Save() returns an error.
	t.Setenv("WHIP_HOME", filepath.Join(f, "cfg"))
	m := &model{cfg: &config.Config{}, agent: &agent.Agent{}}
	m.setUIMode(opencodeMode) // must not panic even though Save fails
	if m.uiMode != opencodeMode {
		t.Fatalf("uiMode = %q, want opencode", m.uiMode)
	}
}

func TestSidebarView(t *testing.T) {
	// Plain model: no pricing, no context limit -> the fallback branches.
	m := &model{agent: &agent.Agent{}, termWidth: sidebarMinWidth}
	out := m.sidebarView(20)
	if !strings.Contains(out, "Context") || !strings.Contains(out, "LSP") {
		t.Fatal("sidebar missing Context/LSP sections")
	}
	// clip path: request fewer rows than the content produces
	if out := m.sidebarView(1); out == "" {
		t.Fatal("clipped sidebar should still render")
	}

	// Priced model with a context window -> the ctx% and spend branches.
	m2 := &model{
		agent:    &agent.Agent{Model: "m", ContextLimit: 1000},
		provName: "p",
		catalogs: map[string]config.Catalog{
			"p": {Models: []config.ModelInfoLite{{ID: "m", InPrice: 1, OutPrice: 1}}},
		},
		termWidth: sidebarMinWidth,
	}
	if out := m2.sidebarView(20); !strings.Contains(out, "% used") || !strings.Contains(out, "spent") {
		t.Fatalf("priced sidebar missing ctx%%/spend: %q", out)
	}
}

func TestSetUIModeRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep cfg.Save() off the real config
	m := &model{cfg: &config.Config{}, agent: &agent.Agent{}}

	m.setUIMode(opencodeMode)
	if m.uiMode != opencodeMode || m.cfg.UIMode != opencodeMode {
		t.Fatalf("enable: uiMode=%q cfg=%q", m.uiMode, m.cfg.UIMode)
	}
	if m.cfgExtra["uiMode"] != opencodeMode {
		t.Fatalf("cfgExtra not pinned: %v", m.cfgExtra)
	}

	m.setUIMode("bogus") // anything not "opencode" reverts to default
	if m.uiMode != "" || m.cfg.UIMode != "" {
		t.Fatalf("revert: uiMode=%q cfg=%q", m.uiMode, m.cfg.UIMode)
	}
	if _, ok := m.cfgExtra["uiMode"]; ok {
		t.Fatalf("cfgExtra still pinned: %v", m.cfgExtra)
	}
}
