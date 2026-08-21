package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandMentions(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "main.go")
	os.WriteFile(f, []byte("x"), 0o644)

	// absolute path with a range, trailing punctuation stripped
	out := expandMentions("look at @" + f + "#10-40, please")
	if !strings.Contains(out, f+" (lines 10-40)") || !strings.Contains(out, "not inlined") {
		t.Fatalf("absolute+range: %q", out)
	}
	// single-line range
	out = expandMentions("check @" + f + "#7")
	if !strings.Contains(out, "(lines 7)") {
		t.Fatalf("single line: %q", out)
	}
	// relative path resolves against cwd
	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)
	out = expandMentions("see @main.go here")
	if !strings.Contains(out, f) {
		t.Fatalf("relative: %q", out)
	}
	// ~ expansion
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.WriteFile(filepath.Join(home, "notes.md"), []byte("x"), 0o644)
	out = expandMentions("read @~/notes.md")
	if !strings.Contains(out, filepath.Join(home, "notes.md")) {
		t.Fatalf("tilde: %q", out)
	}
	// nonexistent paths and bare @ are left alone
	for _, in := range []string{"email me @ 5pm", "ping @nonexistent-file-xyz", "no mentions at all"} {
		if got := expandMentions(in); got != in {
			t.Fatalf("should be unchanged: %q -> %q", in, got)
		}
	}
}

func TestAtMentionCompletion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.txt"), nil, 0o644)
	_, cs := completions("fix @"+dir+"/al", nil, nil)
	if len(cs) != 1 || cs[0].Text != "@"+filepath.Join(dir, "alpha.txt") {
		t.Fatalf("@ completion: %v", texts(cs))
	}
}
