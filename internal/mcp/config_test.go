package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/config"
)

func TestParseClaudeStdio(t *testing.T) {
	t.Setenv("MCP_TEST_KEY", "sekret")
	cfgs, err := ParseClaude([]byte(`{
		"mcpServers": {
			"docs": {
				"type": "stdio",
				"command": "npx",
				"args": ["-y", "@docs/mcp"],
				"env": {"API_KEY": "${MCP_TEST_KEY}"}
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	c := cfgs["docs"]
	want := []string{"npx", "-y", "@docs/mcp"}
	if !reflect.DeepEqual(c.Command, want) {
		t.Errorf("command = %v, want %v", c.Command, want)
	}
	if c.Env["API_KEY"] != "sekret" {
		t.Errorf("env expansion failed: %q", c.Env["API_KEY"])
	}
	if c.Remote() || c.Disabled() {
		t.Errorf("unexpected remote/disabled: %+v", c)
	}
	if c.Valid() != "" {
		t.Errorf("Valid() = %q", c.Valid())
	}
}

func TestParseClaudeRemoteAndSSE(t *testing.T) {
	cfgs, err := ParseClaude([]byte(`{
		"mcpServers": {
			"web": {"type": "http", "url": "https://mcp.example.com/x", "headers": {"Authorization": "Bearer tok"}},
			"old": {"type": "sse", "url": "https://old.example.com/sse"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	web := cfgs["web"]
	if !web.Remote() || web.URL != "https://mcp.example.com/x" || web.Headers["Authorization"] != "Bearer tok" {
		t.Errorf("bad remote: %+v", web)
	}
	old := cfgs["old"]
	if !old.Disabled() {
		t.Error("sse entry should import as disabled")
	}
	if old.Note == "" {
		t.Error("sse entry should carry an explanatory note")
	}
}

func TestParseClaudeInfersTypeAndMissingVar(t *testing.T) {
	os.Unsetenv("NO_SUCH_VAR_WHIP_TEST")
	cfgs, err := ParseClaude([]byte(`{
		"mcpServers": {
			"a": {"command": "srv", "env": {"X": "$NO_SUCH_VAR_WHIP_TEST"}},
			"b": {"url": "http://localhost:9000/mcp"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfgs["a"].Remote() {
		t.Error("command-only entry should be stdio")
	}
	if cfgs["a"].Env["X"] != "" {
		t.Errorf("missing env var should expand to empty, got %q", cfgs["a"].Env["X"])
	}
	if !cfgs["b"].Remote() {
		t.Error("url-only entry should be remote")
	}
}

func TestParseCodex(t *testing.T) {
	t.Setenv("CODEX_TEST_TOKEN", "tok123")
	cfgs, err := ParseCodex([]byte(`
model = "gpt-5"

[mcp_servers.docs]
command = "npx"
args = ["-y", "@docs/mcp", "--fast"] # trailing comment
startup_timeout_sec = 20
tool_timeout_sec = 120
env = { API_KEY = "$CODEX_TEST_TOKEN", PLAIN = 'lit#eral' }

[mcp_servers.local]
command = ["/usr/bin/srv", "--stdio"]

[mcp_servers.remote]
url = "https://mcp.example.com/mcp"
headers = { Authorization = "Bearer $CODEX_TEST_TOKEN" }
enabled = false

[mcp_servers.withsub.env]
FOO = "bar"
[mcp_servers.withsub]
command = "sub-srv"
`))
	if err != nil {
		t.Fatal(err)
	}
	docs := cfgs["docs"]
	if !reflect.DeepEqual(docs.Command, []string{"npx", "-y", "@docs/mcp", "--fast"}) {
		t.Errorf("docs command = %v", docs.Command)
	}
	if docs.StartupTimeoutDuration() != 20*time.Second || docs.ToolTimeoutDuration() != 120*time.Second {
		t.Errorf("docs timeouts = %v/%v", docs.StartupTimeoutDuration(), docs.ToolTimeoutDuration())
	}
	if docs.Env["API_KEY"] != "tok123" || docs.Env["PLAIN"] != "lit#eral" {
		t.Errorf("docs env = %v", docs.Env)
	}
	if !reflect.DeepEqual(cfgs["local"].Command, []string{"/usr/bin/srv", "--stdio"}) {
		t.Errorf("local command = %v", cfgs["local"].Command)
	}
	remote := cfgs["remote"]
	if !remote.Remote() || !remote.Disabled() {
		t.Errorf("remote = %+v", remote)
	}
	if remote.Headers["Authorization"] != "Bearer tok123" {
		t.Errorf("remote headers = %v", remote.Headers)
	}
	if cfgs["withsub"].Env["FOO"] != "bar" || len(cfgs["withsub"].Command) != 1 {
		t.Errorf("withsub = %+v", cfgs["withsub"])
	}
	if _, ok := cfgs["model"]; ok {
		t.Error("top-level keys must not leak into servers")
	}
}

func TestParseCodexErrors(t *testing.T) {
	for _, doc := range []string{
		"[mcp_servers.x]\nargs = [1, 2]\n",
		"[mcp_servers.x]\ncommand = { nope = 1 }\n",
		"[mcp_servers.x]\nenabled = \"yes\"\n",
		"[[mcp_servers.x]]\n",
	} {
		if _, err := ParseCodex([]byte(doc)); err == nil {
			t.Errorf("expected error for %q", doc)
		}
	}
}

func TestMergePrecedence(t *testing.T) {
	disabled := false
	whip := map[string]ServerConfig{"a": {Command: []string{"whip-a"}}, "b": {Enabled: &disabled, Command: []string{"whip-b"}}}
	codex := map[string]ServerConfig{"a": {Command: []string{"codex-a"}}, "c": {Command: []string{"codex-c"}}}
	claude := map[string]ServerConfig{"a": {Command: []string{"claude-a"}}, "c": {Command: []string{"claude-c"}}}
	m := Merge(whip, codex, claude)
	if m["a"].Command[0] != "whip-a" {
		t.Error("whip config must win over codex and claude")
	}
	if m["c"].Command[0] != "codex-c" {
		t.Error("codex must win over claude")
	}
	if !m["b"].Disabled() {
		t.Error("whip-only entry should survive with enabled=false")
	}
}

func TestLoadMergedDiscovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers": {"proj": {"command": "proj-srv"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	codexFile := filepath.Join(dir, "codex.toml")
	if err := os.WriteFile(codexFile, []byte("[mcp_servers.cdx]\ncommand = \"cdx-srv\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := CodexPath
	CodexPath = func() string { return codexFile }
	defer func() { CodexPath = orig }()

	merged, errs := LoadMerged(dir, map[string]ServerConfig{"mine": {Command: []string{"my-srv"}}})
	if len(errs) != 0 {
		t.Fatalf("unexpected discovery errors: %v", errs)
	}
	for _, name := range []string{"proj", "cdx", "mine"} {
		if _, ok := merged[name]; !ok {
			t.Errorf("missing server %q in merged config", name)
		}
	}

	// A malformed .mcp.json reports an error but does not kill the merge.
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, errs = LoadMerged(dir, nil)
	if _, ok := errs[".mcp.json"]; !ok {
		t.Error("expected a parse error for .mcp.json")
	}
	if _, ok := merged["cdx"]; !ok {
		t.Error("codex servers should still merge when .mcp.json is broken")
	}
}

// TestLoadMergedFilteredPolicy pins the mcpImport gating: a policy-filtered
// import (the ChatGPT desktop app's node_repl from ~/.codex/config.toml)
// lands in Blocked as disabled+noted — never connected, never silently
// dropped — while admitted imports and whip's own entries merge normally.
func TestLoadMergedFilteredPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(
		`{"mcpServers": {"proj": {"command": "proj-srv"}, "ghost": {"command": "ghost-srv"}}}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	codexFile := filepath.Join(dir, "codex.toml")
	if err := os.WriteFile(codexFile, []byte(
		"[mcp_servers.node_repl]\ncommand = \"/app/bin/node_repl\"\n[mcp_servers.paper]\nurl = \"http://127.0.0.1:29979/mcp\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := CodexPath
	CodexPath = func() string { return codexFile }
	defer func() { CodexPath = orig }()

	// The node_repl scenario: codex source on, node_repl excluded.
	policy := ImportPolicy{Codex: ImportSourcePolicy{
		Enabled: true,
		Exclude: map[string]bool{"node_repl": true},
	}}
	f := LoadMergedFiltered(dir, nil, policy)
	if _, ok := f.Merged["paper"]; !ok {
		t.Error("admitted codex server should merge")
	}
	if _, ok := f.Merged["node_repl"]; ok {
		t.Error("excluded server must not merge")
	}
	b, ok := f.Blocked["node_repl"]
	if !ok {
		t.Fatal("excluded server must appear in Blocked, not vanish")
	}
	if !b.Disabled() || !strings.HasPrefix(b.Note, "blocked by mcpImport config") {
		t.Errorf("blocked entry should be disabled with a note, got %+v", b)
	}
	if f.Sources["node_repl"] != "codex" || f.Sources["proj"] != ".mcp.json" || f.Sources["paper"] != "codex" {
		t.Errorf("source attribution wrong: %v", f.Sources)
	}

	// Source off: everything from claude drops into Blocked.
	f = LoadMergedFiltered(dir, nil, ImportPolicy{
		Claude: ImportSourcePolicy{Enabled: false},
		Codex:  ImportSourcePolicy{Enabled: true},
	})
	if len(f.Blocked) != 2 {
		t.Errorf("both claude servers should be blocked, got %v", f.Blocked)
	}
	if _, ok := f.Merged["proj"]; ok {
		t.Error("disabled source must not merge")
	}

	// Only-allowlist, and exclude beating only when both are set.
	f = LoadMergedFiltered(dir, nil, ImportPolicy{
		Claude: ImportSourcePolicy{
			Enabled: true, Only: map[string]bool{"proj": true, "ghost": true},
			Exclude: map[string]bool{"ghost": true},
		},
		Codex: ImportSourcePolicy{Enabled: true},
	})
	if _, ok := f.Merged["proj"]; !ok {
		t.Error("allowlisted server should merge")
	}
	if _, ok := f.Blocked["ghost"]; !ok {
		t.Error("exclude must win over only")
	}

	// A whip entry of the same name is never shadowed by a ghost row.
	f = LoadMergedFiltered(dir, map[string]ServerConfig{"node_repl": {Command: []string{"mine"}}}, policy)
	if f.Merged["node_repl"].Command[0] != "mine" {
		t.Error("whip config must still win over a blocked import")
	}
	if _, ok := f.Blocked["node_repl"]; ok {
		t.Error("no ghost row when whip owns the name")
	}

	// Zero policy == LoadMerged (import everything).
	def := LoadMergedFiltered(dir, nil, ImportPolicyFrom(nil))
	if len(def.Merged) != 4 || len(def.Blocked) != 0 {
		t.Errorf("nil policy must import everything, got merged=%v blocked=%v", def.Merged, def.Blocked)
	}

	// Every discovered entry carries the file it came from, so a failed
	// server can point at the config to fix.
	for name, want := range map[string]string{
		"proj":      filepath.Join(dir, ".mcp.json"),
		"node_repl": codexFile,
	} {
		if got := def.Merged[name].Source; got != want {
			t.Errorf("Merged[%q].Source = %q, want %q", name, got, want)
		}
	}
	if got := f.Blocked["ghost"].Source; got != filepath.Join(dir, ".mcp.json") {
		t.Errorf("Blocked[ghost].Source = %q", got)
	}
}

// TestManagerFromBlockedDiscovery pins the original failure scenario end to
// end at the manager level: a codex config carrying the ChatGPT app's
// node_repl, gated by policy, produces a manager with no node_repl server
// (nothing connects, nothing errors) and one visible blocked row.
func TestManagerFromBlockedDiscovery(t *testing.T) {
	dir := t.TempDir()
	codexFile := filepath.Join(dir, "codex.toml")
	if err := os.WriteFile(codexFile, []byte(
		"[mcp_servers.node_repl]\ncommand = \"/app/bin/node_repl\"\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := CodexPath
	CodexPath = func() string { return codexFile }
	defer func() { CodexPath = orig }()

	f := LoadMergedFiltered(dir, nil, ImportPolicyFrom(&config.MCPImport{
		Codex: &config.MCPImportSource{Exclude: []string{"node_repl"}},
	}))
	mgr := NewManager(f.Merged)
	mgr.SetBlocked(f.Blocked)
	if _, ok := mgr.Config("node_repl"); ok {
		t.Error("a blocked server must never reach the manager's live set")
	}
	if !mgr.BlockedByPolicy("node_repl") {
		t.Error("the manager must remember node_repl as policy-blocked")
	}
	blocked := mgr.Blocked()
	if len(blocked) != 1 || blocked[0].Status != StatusDisabled || blocked[0].Note == "" {
		t.Errorf("blocked snapshot: %+v", blocked)
	}
	if blocked[0].Source != codexFile {
		t.Errorf("blocked row should point at its source file, got %q", blocked[0].Source)
	}
}

// TestManagerStatusSource pins the end-to-end handoff: the file a server was
// discovered from survives NewManager and shows up in the Statuses snapshot,
// so the /mcp failure row can name the config to fix.
func TestManagerStatusSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(
		`{"mcpServers": {"proj": {"command": "proj-srv"}}}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	codexFile := filepath.Join(dir, "codex.toml") // absent: keeps the real ~/.codex out
	orig := CodexPath
	CodexPath = func() string { return codexFile }
	defer func() { CodexPath = orig }()
	f := LoadMergedFiltered(dir, nil, ImportPolicyFrom(nil))
	mgr := NewManager(f.Merged)
	sts := mgr.Statuses()
	if len(sts) != 1 || sts[0].Source != filepath.Join(dir, ".mcp.json") {
		t.Errorf("status source: %+v", sts)
	}
}

// TestImportPolicyFrom covers the config-block → policy conversion.
func TestImportPolicyFrom(t *testing.T) {
	off := false
	p := ImportPolicyFrom(&config.MCPImport{
		Claude: &config.MCPImportSource{Enabled: &off},
		Codex:  &config.MCPImportSource{Only: []string{"a"}, Exclude: []string{"b"}},
	})
	if p.Claude.Admits("anything") {
		t.Error("enabled=false must block the whole source")
	}
	if !p.Codex.Admits("a") || p.Codex.Admits("c") {
		t.Error("only must act as an allowlist")
	}
	if p.Codex.Admits("b") {
		t.Error("exclude must beat only")
	}
	// nil block and nil source both mean "on, unfiltered".
	for _, p := range []ImportPolicy{ImportPolicyFrom(nil), ImportPolicyFrom(&config.MCPImport{})} {
		if !p.Claude.Admits("x") || !p.Codex.Admits("x") {
			t.Error("nil policy must admit everything")
		}
	}
}

func TestToolNameRoundTrip(t *testing.T) {
	// Safe names pass through unchanged with claude-style double underscores.
	name := ToolName("my-server", "get_doc.v2")
	if name != "mcp__my-server__get_doc_v2" {
		t.Errorf("ToolName = %q", name)
	}
	srv, tool, ok := ParseToolName(name)
	if !ok || srv != "my-server" || tool != "get_doc_v2" {
		t.Fatalf("ParseToolName(%q) = %q %q %v", name, srv, tool, ok)
	}
	// Underscores inside both names stay recoverable.
	name = ToolName("my_server", "do_thing_now")
	srv, tool, ok = ParseToolName(name)
	if !ok || srv != "my_server" || tool != "do_thing_now" {
		t.Fatalf("ParseToolName(%q) = %q %q %v", name, srv, tool, ok)
	}
	// Unsafe server chars get a hash suffix; the opencode collision class
	// ("a.b" vs "a b") stays distinct.
	if ToolName("a.b", "t") == ToolName("a b", "t") {
		t.Error("sanitized names must not collide")
	}
	srv, _, ok = ParseToolName(ToolName("a.b", "t"))
	if !ok || !strings.HasPrefix(srv, "a-b_") {
		t.Errorf("hashed server key should be unambiguous, got %q", srv)
	}
	if _, _, ok := ParseToolName("bash"); ok {
		t.Error("bash is not an MCP tool")
	}
	if _, _, ok := ParseToolName("mcp__broken"); ok {
		t.Error("mcp__ without server__tool split is invalid")
	}
}

func TestValidAndDefaults(t *testing.T) {
	if (ServerConfig{}).Valid() == "" {
		t.Error("empty config should be invalid")
	}
	c := ServerConfig{Command: []string{"x"}}
	if c.StartupTimeoutDuration() != 30*time.Second || c.ToolTimeoutDuration() != 60*time.Second {
		t.Error("default timeouts wrong")
	}
	if (ServerConfig{URL: "ftp://x"}).Valid() == "" {
		t.Error("non-http url should be invalid")
	}
	if (ServerConfig{URL: "http://x", Command: []string{"y"}}).Valid() == "" {
		t.Error("command+url should be invalid")
	}
}
