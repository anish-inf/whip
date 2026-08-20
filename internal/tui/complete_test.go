package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func texts(cs []cand) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

func TestCompletions(t *testing.T) {
	models := []cand{{"kimi-k3-fast", ""}, {"glm-5.2-fast", ""}}
	provs := []cand{{"inference", ""}}

	// slash commands
	head, cs := completions("/m", models, provs)
	if head != "" || len(cs) != 1 || cs[0].Text != "/model" {
		t.Fatalf("command completion: %q %v", head, texts(cs))
	}
	// /model first arg
	head, cs = completions("/model k", models, provs)
	if head != "/model " || len(cs) != 1 || cs[0].Text != "kimi-k3-fast" {
		t.Fatalf("model completion: %q %v", head, texts(cs))
	}
	// /model second arg
	_, cs = completions("/model kimi-k3-fast inf", models, provs)
	if len(cs) != 1 || cs[0].Text != "inference" {
		t.Fatalf("provider completion: %v", texts(cs))
	}
	// paths
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.txt"), nil, 0o644)
	os.Mkdir(filepath.Join(dir, "alphadir"), 0o755)
	head, cs = completions("fix "+dir+"/al", models, provs)
	if head != "fix " || len(cs) != 2 {
		t.Fatalf("path completion: %q %v", head, texts(cs))
	}
	if cs[1].Text != filepath.Join(dir, "alphadir")+"/" {
		t.Fatalf("dir should get trailing slash: %v", texts(cs))
	}
	// no match
	_, cs = completions("/nope", models, provs)
	if len(cs) != 0 {
		t.Fatalf("expected no candidates, got %v", texts(cs))
	}
}
