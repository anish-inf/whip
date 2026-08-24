package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	os.Unsetenv("NO_SUCH_VAR_LOOPY_TEST")
	cfgs, err := ParseClaude([]byte(`{
		"mcpServers": {
			"a": {"command": "srv", "env": {"X": "$NO_SUCH_VAR_LOOPY_TEST"}},
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
	loopy := map[string]ServerConfig{"a": {Command: []string{"loopy-a"}}, "b": {Enabled: &disabled, Command: []string{"loopy-b"}}}
	codex := map[string]ServerConfig{"a": {Command: []string{"codex-a"}}, "c": {Command: []string{"codex-c"}}}
	claude := map[string]ServerConfig{"a": {Command: []string{"claude-a"}}, "c": {Command: []string{"claude-c"}}}
	m := Merge(loopy, codex, claude)
	if m["a"].Command[0] != "loopy-a" {
		t.Error("loopy config must win over codex and claude")
	}
	if m["c"].Command[0] != "codex-c" {
		t.Error("codex must win over claude")
	}
	if !m["b"].Disabled() {
		t.Error("loopy-only entry should survive with enabled=false")
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
