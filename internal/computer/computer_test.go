package computer

import (
	"errors"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	// mack's flaw: unescaped quotes break the script. Ours escapes.
	if got := quote(`a"b`); got != `"a\"b"` {
		t.Errorf("quote: %q", got)
	}
	if got := quote(`back\slash`); got != `"back\\slash"` {
		t.Errorf("quote backslash: %q", got)
	}
	if got := quote("plain"); got != `"plain"` {
		t.Errorf("quote plain: %q", got)
	}
}

func TestPolicyCheck(t *testing.T) {
	p := NewPolicy([]string{"Google Chrome"}, []string{"Finder"}, true)
	if err := p.Check("Google Chrome"); err != nil {
		t.Errorf("allowed app blocked: %v", err)
	}
	if err := p.Check("google chrome"); err != nil { // case-insensitive
		t.Errorf("case-normalized allow blocked: %v", err)
	}
	if err := p.Check("Finder"); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Errorf("denied app must fail with policy error, got %v", err)
	}
	err := p.Check("Safari")
	if err == nil {
		t.Fatal("unlisted app under default-deny must need approval")
	}
	approvalNeeded := &ApprovalNeeded{}
	if !errors.As(err, &approvalNeeded) {
		t.Fatalf("want ApprovalNeeded, got %T", err)
	}
	p.Approve("Safari")
	if err := p.Check("Safari"); err != nil {
		t.Errorf("session approval must unblock: %v", err)
	}
}

func TestPolicyDefaultAllow(t *testing.T) {
	p := NewPolicy(nil, []string{"Finder"}, false)
	if err := p.Check("Safari"); err != nil {
		t.Errorf("default-allow must pass unlisted apps: %v", err)
	}
	if err := p.Check("Finder"); err == nil {
		t.Error("deny list wins even under default-allow")
	}
}

func TestChromeTabsParse(t *testing.T) {
	// The ￨ separator survives titles containing | or newlines in fields.
	p := &Policy{}
	_ = p
	// (parse logic lives in ChromeTabs; here we pin the separator choice)
	line := "1￨2￨https://example.com￨a title with | pipe"
	f := strings.SplitN(line, "￨", 4)
	if len(f) != 4 || f[3] != "a title with | pipe" {
		t.Fatalf("separator parse: %v", f)
	}
}
