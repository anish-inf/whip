package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abe/loopy/internal/agent"
	"github.com/abe/loopy/internal/llm"
	"github.com/abe/loopy/internal/skills"
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
	_, cs := completions("fix @"+dir+"/al", nil, nil, nil)
	if len(cs) != 1 || cs[0].Text != "@"+filepath.Join(dir, "alpha.txt") {
		t.Fatalf("@ completion: %v", texts(cs))
	}
}

func TestExpandSkills(t *testing.T) {
	sk := []skills.Skill{{Name: "go-style", Description: "d", Path: "/x/go-style/SKILL.md"}}
	out := expandSkills("apply $go-style, thanks", sk)
	if !strings.Contains(out, "go-style (/x/go-style/SKILL.md)") || !strings.Contains(out, "follow its instructions") {
		t.Fatalf("skill note: %q", out)
	}
	for _, in := range []string{"costs $5 now", "run $unknown-skill", "no tokens"} {
		if got := expandSkills(in, sk); got != in {
			t.Fatalf("should be unchanged: %q -> %q", in, got)
		}
	}
}

func TestSkillCompletion(t *testing.T) {
	sk := []cand{{"$go-style", "style rules"}, {"$go-testing", "test rules"}, {"$other", ""}}
	_, cs := completions("apply $go-", nil, nil, sk)
	if len(cs) != 2 || cs[0].Text != "$go-style" && cs[1].Text != "$go-testing" {
		t.Fatalf("$ completion: %v", texts(cs))
	}
}

func TestPrepareTurnReloadsSkillsEveryTurn(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)
	t.Setenv("HOME", t.TempDir()) // isolate ~/.loopy/skills

	os.MkdirAll(filepath.Join(dir, ".agents/skills/demo"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents/skills/demo/SKILL.md"),
		[]byte("---\nname: demo\ndescription: live demo skill\n---\n"), 0o644)

	m := &model{agent: agent.New(llm.New("http://unused", "k"), "m", 1, "overwritten"), sysPrompt: "BASE"}
	out := m.prepareTurn("use $demo now")
	sys := m.agent.Messages[0].Content
	if !strings.HasPrefix(sys, "BASE") || !strings.Contains(sys, "demo: live demo skill") {
		t.Fatalf("system prompt: %q", sys)
	}
	if !strings.Contains(out, "invoked skill(s): demo") {
		t.Fatalf("expansion: %q", out)
	}

	// a skill added AFTER startup appears on the next turn — no restart
	os.MkdirAll(filepath.Join(dir, ".agents/skills/fresh"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agents/skills/fresh/SKILL.md"),
		[]byte("---\nname: fresh\ndescription: added mid-session\n---\n"), 0o644)
	m.prepareTurn("hello")
	if !strings.Contains(m.agent.Messages[0].Content, "fresh: added mid-session") {
		t.Fatalf("new skill not picked up: %q", m.agent.Messages[0].Content)
	}
}
