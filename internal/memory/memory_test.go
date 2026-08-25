package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agreed contract: markdown bullets in both scopes, remember appends,
// forget strikes (doesn't delete), prompt block carries only open entries,
// and the cap is enforced.
func TestScopeRoundTrip(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "memory.md"), Name: "installation"}

	if got := s.Entries(); got != nil {
		t.Fatalf("missing file should be an empty list, got %v", got)
	}
	if err := s.Remember("prefers pnpm over npm"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("deploy with ./scripts/ship.sh"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(s.Path)
	if string(data) != "- [ ] prefers pnpm over npm\n- [ ] deploy with ./scripts/ship.sh\n" {
		t.Fatalf("file must be plain markdown bullets:\n%s", data)
	}

	if err := s.Forget(1); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(s.Path)
	if !strings.Contains(string(data), "- [x] prefers pnpm over npm") {
		t.Fatalf("forget should strike, not delete:\n%s", data)
	}
	if err := s.Forget(1); err == nil {
		t.Fatal("forgetting a done entry should say so")
	}
	if err := s.Forget(9); err == nil {
		t.Fatal("forgetting a missing entry should error")
	}

	// injection carries only open entries, numbered as in the file
	block := PromptBlock(s)
	if strings.Contains(block, "pnpm") {
		t.Fatalf("done entries must not be injected:\n%s", block)
	}
	if !strings.Contains(block, "2. deploy with ./scripts/ship.sh") {
		t.Fatalf("open entry should be injected with its number:\n%s", block)
	}

	// empty scopes inject nothing
	if b := PromptBlock(Scope{}, Scope{}); b != "" {
		t.Fatalf("no scopes should inject nothing, got %q", b)
	}
}

func TestRememberValidationAndCap(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "m.md"), Name: "session"}
	if err := s.Remember("   "); err == nil {
		t.Fatal("blank entries should be rejected")
	}
	if err := s.Remember(strings.Repeat("x", maxEntryLength+1)); err == nil {
		t.Fatal("overlong entries should be rejected")
	}
	for range maxEntries {
		if err := s.Remember("fact"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remember("one too many"); err == nil {
		t.Fatal("the cap must reject; the model is told to forget first")
	}
	// forgetting one opens a slot again
	if err := s.Forget(1); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("fits now"); err != nil {
		t.Fatal(err)
	}
}

// Prose the user writes by hand (headers, notes) survives a forget rewrite.
func TestForgetPreservesUserProse(t *testing.T) {
	s := Scope{Path: filepath.Join(t.TempDir(), "m.md"), Name: "session"}
	os.WriteFile(s.Path, []byte("# my notes\nremember to water the plants\n- [ ] prefers dark mode\n"), 0o644)
	if err := s.Forget(1); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	if !strings.Contains(string(data), "# my notes") || !strings.Contains(string(data), "water the plants") {
		t.Fatalf("hand-written prose must survive:\n%s", data)
	}
}
