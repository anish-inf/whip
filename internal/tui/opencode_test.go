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

func TestSetThemeRefreshesOpencodeInputStyles(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir()) // keep cfg.Save() off the real config
	mdMu.Lock()
	sl, sk := mdLight, mdKnown
	mdMu.Unlock()
	t.Cleanup(func() { mdMu.Lock(); mdLight, mdKnown = sl, sk; mdMu.Unlock() })

	m := &model{cfg: &config.Config{}, input: newInput()}
	t.Cleanup(func() { m.applyUIMode("") }) // don't leak ocActive into other tests
	mdMu.Lock()
	mdKnown = false // start unknown: styles bake NoColor
	mdMu.Unlock()
	m.applyUIMode(opencodeMode)
	m.setTheme("light") // must re-bake the input styles with the light palette
	if got := m.input.FocusedStyle.Placeholder.GetBackground(); got != lipgloss.Color("#e1e1e1") {
		t.Fatalf("placeholder bg after /theme light = %v, want #e1e1e1", got)
	}
}

func TestStartupReportUnknownBgNotice(t *testing.T) {
	mdMu.Lock()
	sl, sk := mdLight, mdKnown
	mdMu.Unlock()
	t.Cleanup(func() { mdMu.Lock(); mdLight, mdKnown = sl, sk; mdMu.Unlock() })

	mdMu.Lock()
	mdKnown = false
	mdMu.Unlock()
	m := &model{uiMode: opencodeMode}
	m.startupReport()
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].text, "background unknown") {
		t.Fatal("unknown background should append an actionable notice")
	}

	mdMu.Lock()
	mdKnown, mdLight = true, true
	mdMu.Unlock()
	m2 := &model{uiMode: opencodeMode}
	m2.startupReport()
	if len(m2.blocks) != 0 {
		t.Fatalf("known background should append nothing, got %d blocks", len(m2.blocks))
	}
}

func TestOcDialogRows(t *testing.T) {
	m := &model{width: 80, cfg: &config.Config{}} // cfg: the theme sub-panel reads cfg.Theme
	m.palette = &palette{
		items: []paletteItem{
			{title: "Model", category: "Agent", dynHint: func(*model) string { return "/model" }},
			{title: "Theme", category: "Display"},
		},
	}
	out := strings.Join(m.ocDialogRows(), "\n")
	for _, want := range []string{"Commands", "esc", "Search", "Agent", "Display", "Model", "/model", "Theme"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dialog missing %q:\n%s", want, out)
		}
	}
	// every row is exactly the dialog width
	for i, r := range m.ocDialogRows() {
		if lipgloss.Width(r) != 64 {
			t.Fatalf("row %d width = %d, want 64", i, lipgloss.Width(r))
		}
	}
	// filter typed: replaces the Search placeholder
	m.palette.filter = "the"
	if out := strings.Join(m.ocDialogRows(), "\n"); !strings.Contains(out, "the") || strings.Contains(out, "Search") {
		t.Fatalf("filter should replace Search placeholder:\n%s", out)
	}
	// no matches
	m.palette.items = nil
	if out := strings.Join(m.ocDialogRows(), "\n"); !strings.Contains(out, "No results found") {
		t.Fatal("empty palette should say No results found")
	}
	// sub-panel renders inside the box with a breadcrumb header
	m.palette.stack = []*ppanel{{kind: panelTheme, title: "Theme", list: []string{"auto", "light", "dark"}}}
	if out := strings.Join(m.ocDialogRows(), "\n"); !strings.Contains(out, "Commands › Theme") {
		t.Fatalf("sub-panel missing breadcrumb:\n%s", out)
	}
	m.palette.stack = nil

	// narrow terminal: the left/right gap clamps to 1 instead of going negative
	m.width = 20
	m.palette.items = []paletteItem{
		{title: "First", category: "Agent"},
		{title: "A very long item title here", category: "Agent", dynHint: func(*model) string { return "/hint" }},
	}
	m.palette.filter = ""
	if out := strings.Join(m.ocDialogRows(), "\n"); !strings.Contains(out, "Commands") {
		t.Fatal("narrow dialog should still render")
	}
}

func TestOcOverlay(t *testing.T) {
	m := &model{width: 80, termWidth: 80, height: 30}
	m.palette = &palette{items: []paletteItem{{title: "Model", category: "Agent"}}}
	backdrop := strings.TrimSuffix(strings.Repeat("session line here\n", 30), "\n")
	out := m.ocOverlay(backdrop)
	if !strings.Contains(out, "Commands") {
		t.Fatal("overlay missing dialog")
	}
	if !strings.Contains(out, "\x1b[2m") {
		t.Fatal("backdrop should be dimmed (SGR 2)")
	}
	if got := len(strings.Split(out, "\n")); got != 30 {
		t.Fatalf("overlay changed line count: %d, want 30", got)
	}
	// dialog taller than the backdrop: clipped, line count preserved
	short := "one\ntwo\nthree"
	if got := len(strings.Split(m.ocOverlay(short), "\n")); got != 3 {
		t.Fatalf("clipped overlay lines = %d, want 3", got)
	}
}

func TestOcDimLine(t *testing.T) {
	if got := ocDimLine(""); got != "" {
		t.Fatalf("empty line should stay empty, got %q", got)
	}
	got := ocDimLine("a\x1b[0mb")
	if !strings.HasPrefix(got, "\x1b[2m") || !strings.Contains(got, "\x1b[0m\x1b[2m") {
		t.Fatalf("dim not re-applied after reset: %q", got)
	}
}

func TestOcPadTo(t *testing.T) {
	got := ocPadTo("ab", 6, lipgloss.Color("#ebebeb"))
	if lipgloss.Width(got) != 6 {
		t.Fatalf("padded width = %d, want 6", lipgloss.Width(got))
	}
	if !strings.Contains(got, "    ") {
		t.Fatalf("missing pad spaces: %q", got)
	}
	if got := ocPadTo("abcdef", 4, lipgloss.Color("#ebebeb")); got != "abcdef" {
		t.Fatalf("over-width content must pass through, got %q", got)
	}
}

func TestSanitizeViewKeepsPanelFillInOpencodeMode(t *testing.T) {
	old := ocActive
	t.Cleanup(func() { ocActive = old })
	line := "x\x1b[48;2;235;235;235m    \x1b[0m" // styled trailing spaces = panel fill
	ocActive = true
	if got := sanitizeView(line); !strings.Contains(got, "    ") {
		t.Fatalf("opencode mode must keep styled trailing spaces: %q", got)
	}
	ocActive = false
	if got := sanitizeView(line); strings.Contains(got, "    ") {
		t.Fatalf("default mode must strip styled trailing spaces: %q", got)
	}
}

func TestOcBgShift(t *testing.T) {
	oldCache := bgCache
	mdMu.Lock()
	sl, sk := mdLight, mdKnown
	mdMu.Unlock()
	t.Cleanup(func() {
		bgCache = oldCache
		mdMu.Lock()
		mdLight, mdKnown = sl, sk
		mdMu.Unlock()
	})
	mdMu.Lock()
	mdKnown, mdLight = true, false
	mdMu.Unlock()

	// no RGB captured -> fall back
	bgCache = bgResult{light: false, valid: true}
	if _, ok := ocBgShift(10); ok {
		t.Fatal("no RGB should not derive")
	}
	// dark bg -> lighten by delta
	bgCache = bgResult{light: false, valid: true, r: 0x26, g: 0x28, b: 0x2c, hasRGB: true}
	if c, ok := ocBgShift(10); !ok || c != lipgloss.Color("#303236") {
		t.Fatalf("dark shift = %v %v, want #303236", c, ok)
	}
	// light bg -> darken by 2x delta, clamped at 0..255
	bgCache = bgResult{light: true, valid: true, r: 0xff, g: 0xff, b: 0xf5, hasRGB: true}
	if c, ok := ocBgShift(10); !ok || c != lipgloss.Color("#ebebe1") {
		t.Fatalf("light shift = %v %v, want #ebebe1", c, ok)
	}
	// panels/element derive from the cache when present
	bgCache = bgResult{light: false, valid: true, r: 0x26, g: 0x28, b: 0x2c, hasRGB: true}
	if got := ocPanelBg(); got != lipgloss.Color("#303236") {
		t.Fatalf("panel = %v, want derived", got)
	}
	if got := ocElementBg(); got != lipgloss.Color("#3a3c40") {
		t.Fatalf("element = %v, want derived", got)
	}
	// unknown theme -> no derivation even with RGB
	mdMu.Lock()
	mdKnown = false
	mdMu.Unlock()
	if _, ok := ocBgShift(10); ok {
		t.Fatal("unknown theme should not derive")
	}
}

func TestParseOSCBgRGB(t *testing.T) {
	r, g, b, ok := parseOSCBgRGB("rgb:2626/2828/2c2c")
	if !ok || r != 0x26 || g != 0x28 || b != 0x2c {
		t.Fatalf("rgb parse = %d %d %d %v", r, g, b, ok)
	}
	if _, _, _, ok := parseOSCBgRGB("garbage"); ok {
		t.Fatal("garbage should not parse")
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
	mdMu.Lock()
	sl, sk := mdLight, mdKnown
	mdMu.Unlock()
	t.Cleanup(func() { mdMu.Lock(); mdLight, mdKnown = sl, sk; mdMu.Unlock() })

	mdMu.Lock()
	mdLight, mdKnown = false, true // known dark: full chrome with ▀ shadow
	mdMu.Unlock()
	got := m.opencodePrompt("type here", 40)
	if !strings.Contains(got, "┃") || !strings.Contains(got, "╹") || !strings.Contains(got, "▀") {
		t.Fatal("prompt chrome missing ┃/╹/▀")
	}

	mdMu.Lock()
	mdKnown = false // unknown bg: the ▀ shadow must be skipped (it would render as a black bar on a light terminal)
	mdMu.Unlock()
	if unk := m.opencodePrompt("type here", 40); strings.Contains(unk, "▀") || !strings.Contains(unk, "╹") {
		t.Fatalf("unknown-theme prompt should keep ╹ but drop ▀: %q", unk)
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
	m := &model{cfg: &config.Config{}, agent: &agent.Agent{}, input: newInput()}
	t.Cleanup(func() { m.applyUIMode("") }) // don't leak ocActive into other tests
	m.setUIMode(opencodeMode)               // must not panic even though Save fails
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
	m := &model{cfg: &config.Config{}, agent: &agent.Agent{}, input: newInput()}

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
