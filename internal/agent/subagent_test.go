package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/llm"
)

// A foreground subagent's report is capped before it lands in the parent's
// context, so one long investigation can't swamp the parent's window. Under
// the cap the report passes through verbatim.
func TestForegroundReportCapped(t *testing.T) {
	long := strings.Repeat("x", subagentReportCap+5000)
	srv, _ := modelRecorder(t, long)
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "parent-model", 100, "sys")
	out, err := findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go"}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) > len(long) {
		t.Fatalf("report should be capped at %d bytes, got %d", subagentReportCap, len(out))
	}
	if !strings.Contains(out, "report truncated") {
		t.Fatalf("capped report should carry a truncation marker, got tail %q", out[len(out)-120:])
	}
	if !strings.HasPrefix(out, strings.Repeat("x", 100)) {
		t.Fatal("capped report should keep the report's head")
	}
}

// Under the cap the report is returned untouched.
func TestForegroundReportUnderCapPassesThrough(t *testing.T) {
	srv, _ := modelRecorder(t, "short report")
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "parent-model", 100, "sys")
	out, err := findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go"}`))
	if err != nil || out != "short report" {
		t.Fatalf("short report should pass through verbatim, got %q, %v", out, err)
	}
}

// taskSlug derives a human-meaningful id from the description with a monotonic
// counter for uniqueness; an empty/punctuation-only description falls back to
// "sub". taskIDNum recovers the trailing counter for stable sorting.
func TestTaskSlug(t *testing.T) {
	cases := []struct{ desc, want string }{
		{"Survey context growth in pi + oh-my-pi", "survey-context-growth-in-pi-3"},
		{"Fix the bug!", "fix-the-bug-3"},
		{"", "sub-3"},
		{"!!!", "sub-3"},
		{"a b c d e f g", "a-b-c-d-e-3"}, // capped at 5 words
	}
	for _, c := range cases {
		if got := taskSlug(c.desc, 3); got != c.want {
			t.Errorf("taskSlug(%q,3) = %q, want %q", c.desc, got, c.want)
		}
	}
	if n := taskIDNum(taskSlug("survey pi", 42)); n != 42 {
		t.Errorf("taskIDNum should recover the trailing counter, got %d", n)
	}
	if n := taskIDNum("task-7"); n != 7 { // legacy id still parses
		t.Errorf("taskIDNum on legacy id: got %d", n)
	}
}

// StartBackground names the task after its description, not a bare sequence.
func TestStartBackgroundSlugID(t *testing.T) {
	srv, _ := modelRecorder(t, "ok")
	defer srv.Close()
	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("Survey context growth in codex", "p", SubModel{})
	<-task.Done
	if !strings.HasPrefix(task.ID, "survey-context-growth-in-codex-") {
		t.Fatalf("task id should be a description slug, got %q", task.ID)
	}
}
