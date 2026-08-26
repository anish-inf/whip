package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseTOMLValue pins the value grammar directly: escapes, literal vs
// basic strings, arrays, nested inline tables, numbers and booleans. The
// hand-written reader is the risky half of codex import, so it gets a table.
func TestParseTOMLValue(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want any
	}{
		{`true`, true},
		{`false`, false},
		{`42`, int64(42)},
		{`-7`, int64(-7)},
		{`1.5`, 1.5},
		{`"plain"`, "plain"},
		{`""`, ""},
		{`"a\nb\tc\rd\\e\"f"`, "a\nb\tc\rd\\e\"f"},
		{`'lit\nno-escape'`, `lit\nno-escape`}, // literal strings keep backslashes
		{`''`, ""},
		{`[]`, []string(nil)},
		{`["a", "b"]`, []string{"a", "b"}},
		{`["a, b"]`, []string{"a, b"}}, // comma inside quotes is not a separator
		{`{}`, map[string]any{}},
		{`{ a = "1", b = 2, c = true }`, map[string]any{"a": "1", "b": int64(2), "c": true}},
		{`{outer = {inner = "x"}, after = "y"}`, map[string]any{"outer": map[string]any{"inner": "x"}, "after": "y"}},
		{`{"quoted key" = "v"}`, map[string]any{"quoted key": "v"}},
	} {
		got, err := parseTOMLValue(tc.in)
		if err != nil {
			t.Errorf("parseTOMLValue(%s) error: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseTOMLValue(%s) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
}

// TestParseTOMLValueErrors: malformed input must fail loudly, never parse
// wrong — each case names the construct it rejects.
func TestParseTOMLValueErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{``, "unsupported value"},
		{`nope`, "unsupported value"},
		{`"`, "unterminated string"},
		{`'`, "unterminated string"},
		{`'abc`, "unterminated string"},
		{`"abc`, "unterminated string"},
		{`"a"b"`, "trailing data after string"},
		{`"a\`, "unterminated escape"},
		{`"a\q"`, `unsupported escape \q`},
		{`[1, 2`, "unterminated array"},
		{`[nope]`, "unsupported value"},
		{`[["x"]]`, "array elements must be strings"},
		{`{a = "1"`, "unterminated inline table"},
		{`{a}`, "expected key = value in inline table"},
		{`{a = nope}`, "unsupported value"},
	} {
		got, err := parseTOMLValue(tc.in)
		if err == nil {
			t.Errorf("parseTOMLValue(%s) = %#v, want error %q", tc.in, got, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("parseTOMLValue(%s) error = %q, want it to contain %q", tc.in, err, tc.want)
		}
	}
}

// TestParseCodexScalarKeys: the keys that map onto ServerConfig scalars,
// including the _ms → seconds round-up.
func TestParseCodexScalarKeys(t *testing.T) {
	cfgs, err := ParseCodex([]byte(`
[mcp_servers.x]
command = "srv"
cwd = "/srv/root"
enabled = true
startup_timeout_ms = 1500
tool_timeout_sec = 90
unknown_codex_key = "ignored"
`))
	if err != nil {
		t.Fatal(err)
	}
	x := cfgs["x"]
	if x.Cwd != "/srv/root" {
		t.Errorf("cwd = %q", x.Cwd)
	}
	if x.Enabled == nil || !*x.Enabled || x.Disabled() {
		t.Errorf("enabled = %v", x.Enabled)
	}
	if x.StartupTimeout != 2 { // 1500ms rounds up to 2s
		t.Errorf("startup_timeout_ms 1500 → %ds, want 2s", x.StartupTimeout)
	}
	if x.ToolTimeout != 90 {
		t.Errorf("tool_timeout_sec = %d", x.ToolTimeout)
	}
}

// TestParseCodexTypeErrors: a well-formed value of the wrong type names the
// table and the key, so a user can fix their config.toml.
func TestParseCodexTypeErrors(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{"[mcp_servers.x]\nargs = \"notanarray\"\n", "args must be an array of strings"},
		{"[mcp_servers.x]\nenv = \"notatable\"\n", "env must be an inline table"},
		{"[mcp_servers.x]\nenvironment = 3\n", "environment must be an inline table"},
		{"[mcp_servers.x]\nheaders = 3\n", "headers must be an inline table"},
		{"[mcp_servers.x]\nurl = 3\n", "url must be a string"},
		{"[mcp_servers.x]\ncwd = 3\n", "cwd must be a string"},
		{"[mcp_servers.x]\nstartup_timeout_sec = \"soon\"\n", "startup_timeout_sec must be an integer"},
		{"[mcp_servers.x]\nstartup_timeout_ms = \"soon\"\n", "startup_timeout_ms must be an integer"},
		{"[mcp_servers.x]\ntool_timeout_sec = true\n", "tool_timeout_sec must be an integer"},
		{"[mcp_servers.x]\njust-a-bare-key\n", "expected key = value"},
		{"[mcp_servers.x]\ncommand = \"a\nargs = [\"b\"]\n", "unterminated string"},
	} {
		cfgs, err := ParseCodex([]byte(tc.doc))
		if err == nil {
			t.Errorf("ParseCodex(%q) = %v, want error %q", tc.doc, cfgs, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseCodex(%q) error = %q, want it to contain %q", tc.doc, err, tc.want)
		}
	}
}

func TestLoadCodex(t *testing.T) {
	// An unset path is the "no codex config" signal in the discovery flow.
	if _, err := LoadCodex(""); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadCodex(\"\") = %v, want os.ErrNotExist", err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[mcp_servers.d]\ncommand = \"srv\"\nargs = ['--stdio']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgs, err := LoadCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfgs["d"].Command, []string{"srv", "--stdio"}) {
		t.Errorf("d command = %v", cfgs["d"].Command)
	}
	if _, err := LoadCodex(filepath.Join(t.TempDir(), "missing.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file = %v, want os.ErrNotExist", err)
	}
}
