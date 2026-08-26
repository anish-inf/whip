package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(p, 0o755)
	os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(content), 0o644)
}

func TestScanAndPromptBlock(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "go-style", "---\nname: go-style\ndescription: \"Go style rules\"\n---\nbody")
	writeSkill(t, dir, "unnamed", "---\ndescription: 'no name here'\n---\n")
	writeSkill(t, dir, "no-frontmatter", "just a plain file")
	os.MkdirAll(filepath.Join(dir, "empty-dir"), 0o755) // no SKILL.md

	sk := Scan(dir, filepath.Join(dir, "does-not-exist"))
	if len(sk) != 2 {
		t.Fatalf("expected 2 skills, got %+v", sk)
	}
	byName := map[string]Skill{}
	for _, s := range sk {
		byName[s.Name] = s
	}
	if byName["go-style"].Description != "Go style rules" {
		t.Fatalf("quoted description: %+v", byName["go-style"])
	}
	if _, ok := byName["unnamed"]; !ok { // falls back to dir name
		t.Fatalf("dir-name fallback missing: %+v", sk)
	}

	block := PromptBlock(sk)
	if !strings.Contains(block, "<available_skills>") || !strings.Contains(block, "<name>go-style</name>") || !strings.Contains(block, "<description>Go style rules</description>") {
		t.Fatalf("prompt block: %q", block)
	}
	if PromptBlock(nil) != "" {
		t.Fatal("empty scan must produce no block")
	}

	// descriptions up to the spec's 1024 pass through intact (no truncation —
	// the spec limit is a validity ceiling, not a prompt budget)
	long := Skill{Name: "x", Description: strings.Repeat("d", 400), Path: "p"}
	if b := PromptBlock([]Skill{long}); !strings.Contains(b, strings.Repeat("d", 400)) {
		t.Fatalf("spec-legal description must not be truncated")
	}
}

// DefaultDirs: project .agents/skills (from the cwd) then user ~/.whip/skills.
func TestDefaultDirs(t *testing.T) {
	wd := t.TempDir()
	home := t.TempDir()
	t.Chdir(wd)
	t.Setenv("HOME", home)

	dirs := DefaultDirs()
	if len(dirs) != 2 {
		t.Fatalf("DefaultDirs() = %v", dirs)
	}
	if dirs[0] != filepath.Join(wd, ".agents", "skills") {
		t.Fatalf("project dir: %q", dirs[0])
	}
	if dirs[1] != filepath.Join(home, ".whip", "skills") {
		t.Fatalf("user dir: %q", dirs[1])
	}
}
