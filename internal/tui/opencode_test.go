package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/llm"
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

func TestPaletteChrome(t *testing.T) {
	m := &model{}
	if got := m.paletteChrome("x"); got != "x" {
		t.Fatalf("default mode should pass through, got %q", got)
	}
	m.uiMode = opencodeMode
	if got := m.paletteChrome("x"); !strings.HasPrefix(got, "   ") {
		t.Fatalf("opencode mode should indent, got %q", got)
	}
}

func TestOcPick(t *testing.T) {
	mdMu.Lock()
	sl, sk := mdLight, mdKnown
	mdMu.Unlock()
	t.Cleanup(func() { mdMu.Lock(); mdLight, mdKnown = sl, sk; mdMu.Unlock() })

	set := func(light, known bool) { mdMu.Lock(); mdLight, mdKnown = light, known; mdMu.Unlock() }

	set(false, true) // known dark
	if got := ocPick("#111", "#eee", "8"); got != lipgloss.Color("#111") {
		t.Fatalf("dark = %v", got)
	}
	set(true, true) // known light
	if got := ocPick("#111", "#eee", "8"); got != lipgloss.Color("#eee") {
		t.Fatalf("light = %v", got)
	}
	set(false, false) // unknown -> neutral
	if got := ocPick("#111", "#eee", "8"); got != lipgloss.Color("8") {
		t.Fatalf("unknown neutral = %v", got)
	}
	if got := ocPick("#111", "#eee", ""); got != (lipgloss.NoColor{}) { // unknown, no neutral -> transparent
		t.Fatalf("unknown transparent = %v", got)
	}
}

func TestOpencodeMDStyle(t *testing.T) {
	dark := opencodeMDStyle(false)
	if dark.Document.Color == nil || *dark.Document.Color != "#eeeeee" {
		t.Fatalf("dark document color = %v, want #eeeeee", dark.Document.Color)
	}
	light := opencodeMDStyle(true)
	if light.Document.Color == nil || *light.Document.Color != "#1a1a1a" {
		t.Fatalf("light document color = %v, want #1a1a1a", light.Document.Color)
	}
}

func TestOpencodeHome(t *testing.T) {
	out := opencodeHome(80, 20)
	if !strings.Contains(out, "█") {
		t.Fatal("home screen missing logo glyphs")
	}
	if lipgloss.Height(out) != 20 {
		t.Fatalf("home height = %d, want 20", lipgloss.Height(out))
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
	m := &model{input: newInput()}
	m.applyUIMode(opencodeMode)
	if m.uiMode != opencodeMode || !ocActive || m.input.Prompt != "" {
		t.Fatalf("opencode: uiMode=%q ocActive=%v prompt=%q", m.uiMode, ocActive, m.input.Prompt)
	}
	m.applyUIMode("bogus")
	if m.uiMode != "" || ocActive || m.input.Prompt != "┃ " {
		t.Fatalf("default: uiMode=%q ocActive=%v prompt=%q", m.uiMode, ocActive, m.input.Prompt)
	}
}

func TestOpencodeUserCard(t *testing.T) {
	if got := opencodeUserCard("hi", 2); got != "hi" {
		t.Fatalf("tiny width should pass through, got %q", got)
	}
	got := opencodeUserCard("hello", 40)
	if !strings.Contains(got, "┃") || !strings.Contains(got, "hello") {
		t.Fatal("card missing bar/text")
	}
	if n := strings.Count(got, "\n"); n != 2 { // blank above + text + blank below
		t.Fatalf("card rows: %d newlines, want 2", n)
	}
}

func TestOpencodePrompt(t *testing.T) {
	m := &model{agent: &agent.Agent{}}
	if got := m.opencodePrompt("in", 4); got != "in" {
		t.Fatalf("tiny width should pass through, got %q", got)
	}
	got := m.opencodePrompt("type here", 40)
	if !strings.Contains(got, "┃") || !strings.Contains(got, "╹") || !strings.Contains(got, "▀") {
		t.Fatal("prompt chrome missing ┃/╹/▀")
	}
	if !strings.Contains(got, "kimi") && !strings.Contains(got, "Off") {
		// meta row present (mode label at minimum)
		if !strings.Contains(got, "Off") {
			t.Fatalf("prompt meta row missing mode label: %q", got)
		}
	}
}

func TestFmtShortDur(t *testing.T) {
	if got := fmtShortDur(150 * time.Millisecond); got != "150ms" {
		t.Fatalf("sub-second = %q, want 150ms", got)
	}
	if got := fmtShortDur(2400 * time.Millisecond); got != "2.4s" {
		t.Fatalf("seconds = %q, want 2.4s", got)
	}
}

func TestOpencodeThoughtAndAttribution(t *testing.T) {
	m := &model{agent: &agent.Agent{}, modelName: "kimi-k3"}
	th := m.opencodeThought(159 * time.Millisecond)
	if !strings.HasPrefix(th, "   ") || !strings.Contains(th, "+ Thought: 159ms") {
		t.Fatalf("thought = %q", th)
	}
	at := m.opencodeAttribution(1600 * time.Millisecond)
	if !strings.HasPrefix(at, "   ") || !strings.Contains(at, "▣") || !strings.Contains(at, "kimi-k3") || !strings.Contains(at, "1.6s") {
		t.Fatalf("attribution = %q", at)
	}
}

func TestBlockOCMetaRendersVerbatim(t *testing.T) {
	b := block{kind: blockOCMeta, text: "   + Thought: 1s"}
	if got := b.render(80); got != "   + Thought: 1s" {
		t.Fatalf("blockOCMeta = %q, want verbatim (indent preserved)", got)
	}
}

func TestOpencodeStatus(t *testing.T) {
	// no usage: just cwd + ctrl+p commands
	m := &model{agent: &agent.Agent{}, width: 80}
	out := m.opencodeStatus()
	if !strings.Contains(out, "ctrl+p commands") {
		t.Fatalf("status missing commands hint: %q", out)
	}
	// with usage + context window: tokens and pct shown, uppercased
	m2 := &model{
		agent: &agent.Agent{ContextLimit: 1000},
		width: 80,
	}
	m2.agent.AddUsage(llm.Usage{PromptTokens: 15800})
	out2 := m2.opencodeStatus()
	if !strings.Contains(out2, "ctrl+p commands") {
		t.Fatalf("status2 missing commands: %q", out2)
	}
	// narrow width path: cwd gets truncated but the right side survives
	m.width = 20
	if narrow := m.opencodeStatus(); !strings.Contains(narrow, "ctrl+p commands") {
		t.Fatalf("narrow status dropped commands: %q", narrow)
	}
}

func TestOCModeLabel(t *testing.T) {
	m := &model{agent: &agent.Agent{}}
	if got := m.ocModeLabel(); got != "Off" {
		t.Fatalf("empty effort = %q, want Off", got)
	}
	m.agent.Effort = "high"
	if got := m.ocModeLabel(); got != "High" {
		t.Fatalf("high = %q, want High", got)
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
	// height<=0: natural-height path (no padding/clip)
	if out := m.sidebarView(0); !strings.Contains(out, "whip") {
		t.Fatalf("height<=0 sidebar missing footer: %q", out)
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
