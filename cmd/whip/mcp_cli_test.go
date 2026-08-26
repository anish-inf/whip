package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/mcp"
)

func TestMCPCLIAddListRemove(t *testing.T) {
	wd := importFixture(t, "") // codex fixture provides node_repl + paper
	chdir(t, wd)

	// dispatch and argument validation
	if err := mcpCLI(nil, "v"); err == nil {
		t.Error("bare `whip mcp` should print usage")
	}
	if err := mcpCLI([]string{"bogus"}, "v"); err == nil {
		t.Error("unknown subcommand should error")
	}
	if err := mcpCLI([]string{"add"}, "v"); err == nil {
		t.Error("add without a name should error")
	}
	if err := mcpCLI([]string{"add", "x", "oops"}, "v"); err == nil {
		t.Error("add without -- or --url should error")
	}
	if err := mcpCLI([]string{"add", "bad", "--url", "ftp://x"}, "v"); err == nil {
		t.Error("a non-http url is an invalid server")
	}

	// add a stdio server and a remote server
	var err error
	out := captureStdout(t, func() { err = mcpCLI([]string{"add", "local", "--", "echo", "hi"}, "v") })
	if err != nil || !strings.Contains(out, `added mcp server "local"`) {
		t.Fatalf("add stdio: %v %q", err, out)
	}
	if err := mcpCLI([]string{"add", "remote", "--url", "http://127.0.0.1:9/mcp"}, "v"); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	// list shows both new servers, the imported ones, and their sources
	out = captureStdout(t, func() { err = mcpCLI([]string{"list"}, "v") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"local", "echo hi", "remote", "http://127.0.0.1:9/mcp", "paper", "whip config", "codex config"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}

	// remove: own entry works, imported and unknown names explain themselves
	if err := mcpCLI([]string{"remove", "local"}, "v"); err != nil {
		t.Fatalf("remove own server: %v", err)
	}
	out = captureStdout(t, func() { _ = mcpCLI([]string{"list"}, "v") })
	if strings.Contains(out, "local") {
		t.Errorf("removed server still listed:\n%s", out)
	}
	if err := mcpCLI([]string{"remove", "paper"}, "v"); err == nil || !strings.Contains(err.Error(), "edit that file") {
		t.Errorf("removing an imported server should point at its source file, got %v", err)
	}
	if err := mcpCLI([]string{"remove", "nosuch"}, "v"); err == nil || !strings.Contains(err.Error(), "no mcp server") {
		t.Errorf("removing an unknown server: %v", err)
	}
	if err := mcpCLI([]string{"remove"}, "v"); err == nil {
		t.Error("remove without a name should error")
	}
}

func TestMCPCLIBlockedServer(t *testing.T) {
	wd := importFixture(t, `, "mcpImport": { "codex": { "exclude": ["node_repl"] } }`)
	chdir(t, wd)

	out := captureStdout(t, func() { _ = mcpCLI([]string{"list"}, "v") })
	if !strings.Contains(out, "node_repl") || !strings.Contains(out, "blocked") {
		t.Errorf("list should mark the excluded server blocked:\n%s", out)
	}
	if err := mcpCLI([]string{"remove", "node_repl"}, "v"); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("removing a blocked server should explain the policy, got %v", err)
	}
	var err error
	_ = captureStdout(t, func() { err = mcpTestCLI("node_repl") })
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("testing a blocked server should explain the policy, got %v", err)
	}
}

func TestMCPTestCLIUnknownAndDisabled(t *testing.T) {
	whipHome := t.TempDir()
	t.Setenv("WHIP_HOME", whipHome)
	chdir(t, t.TempDir()) // no .mcp.json in the working directory
	orig := mcp.CodexPath
	mcp.CodexPath = func() string { return filepath.Join(whipHome, "no-codex.toml") }
	t.Cleanup(func() { mcp.CodexPath = orig })

	cfg := `{
  "defaultModel": "m1",
  "providers": { "a": { "baseUrl": "https://a", "api": "openai-completions" } },
  "models": { "m1": { "providers": ["a"] } },
  "mcp": { "off": { "command": ["true"], "enabled": false } }
}`
	if err := os.WriteFile(filepath.Join(whipHome, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mcpCLI([]string{"test"}, "v"); err == nil {
		t.Error("`mcp test` without a name should print usage")
	}
	var err error
	_ = captureStdout(t, func() { err = mcpTestCLI("nosuch") })
	if err == nil || !strings.Contains(err.Error(), "no mcp server named") {
		t.Errorf("unknown server: %v", err)
	}
	// a disabled server reports without ever launching the command
	out := captureStdout(t, func() { err = mcpTestCLI("off") })
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled server should error: %v", err)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("disabled status not printed:\n%s", out)
	}
	// list marks it disabled too
	out = captureStdout(t, func() { _ = mcpCLI([]string{"list"}, "v") })
	if !strings.Contains(out, "off") || !strings.Contains(out, "disabled") {
		t.Errorf("list should show the disabled server:\n%s", out)
	}
}
