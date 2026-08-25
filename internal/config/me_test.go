package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeSeedsTemplateAndStripsComments(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())

	// first read seeds the file with the commented template
	if got := MeInstructions(); got != "" {
		t.Fatalf("a fresh seed is all comments — nothing to inject, got %q", got)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("WHIP_HOME"), "me.md"))
	if err != nil || !strings.Contains(string(data), "# Your standing instructions") {
		t.Fatalf("seed file should exist with the template: %v\n%s", err, data)
	}

	// user edits land in the injection; comments and blanks stay out
	os.WriteFile(filepath.Join(os.Getenv("WHIP_HOME"), "me.md"),
		[]byte("# hi\n\n- Always pnpm.\n- Ask before force-push.\n"), 0o644)
	got := MeInstructions()
	if !strings.Contains(got, "- Always pnpm.") || strings.Contains(got, "# hi") {
		t.Fatalf("instructions should carry user lines only:\n%s", got)
	}

	// the built-in operating rules are unaffected — the file APPENDS
	if !strings.Contains(MeSeed, "/me opens this file") {
		t.Fatal("seed should tell the user how to edit")
	}
}
