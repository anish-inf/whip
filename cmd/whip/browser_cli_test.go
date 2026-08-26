package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/browser/extrelay"
)

func TestBrowserCLIDispatch(t *testing.T) {
	if err := browserCLI(nil); err == nil {
		t.Error("bare `whip browser` should print usage")
	}
	if err := browserCLI([]string{"bogus"}); err == nil {
		t.Error("unknown subcommand should error")
	}
}

// install writes the unpacked extension and the relay state file into an
// isolated HOME. PATH is emptied so the best-effort open of
// chrome://extensions can never launch anything on the test machine.
func TestBrowserInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // xdg-open/open not found: Start fails silently

	var err error
	out := captureStdout(t, func() { err = browserCLI([]string{"install"}) })
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	dir := extrelay.ExtensionDir(home)
	entries, rerr := os.ReadDir(dir)
	if rerr != nil || len(entries) == 0 {
		t.Fatalf("extension dir not written: %v", rerr)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Errorf("manifest.json missing: %v", err)
	}

	// relay state: valid JSON with a non-empty token, private perms
	statePath := extrelay.RelayStatePath(home)
	info, serr := os.Stat(statePath)
	if serr != nil {
		t.Fatalf("relay state missing: %v", serr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("relay state should be 0600, got %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(statePath)
	var state struct{ Addr, Token string }
	if err := json.Unmarshal(data, &state); err != nil || state.Token == "" || state.Addr == "" {
		t.Errorf("relay state should carry addr+token: %v %q", err, data)
	}

	// the instructions name the folder the user must load
	if !strings.Contains(out, dir) || !strings.Contains(out, "Load unpacked") {
		t.Errorf("install output should walk through the manual load:\n%s", out)
	}
}
