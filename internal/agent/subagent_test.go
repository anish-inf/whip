package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// A background subagent registers in the task registry (dock row + badge via
// OnChange) BEFORE worktree provisioning runs — the synchronous git call must
// not delay the visible spawn signal. With isolation on, the provisioned path
// is steered into the already-live task.
func TestBackgroundWorktreeRegistersBeforeProvisioning(t *testing.T) {
	if os.Getenv("WHIP_SKIP_WORKTREE_TEST") == "1" {
		t.Skip("skipped via WHIP_SKIP_WORKTREE_TEST")
	}
	ctx := context.Background()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	t.Chdir(repo)

	srv, _ := modelRecorder(t, "done")
	defer srv.Close()
	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	ag.WorktreeSubagents = true

	// The registry must see the task before the tool call returns — and the
	// worktree path is delivered by steering the live subagent, not by baking
	// it into the initial prompt.
	out, err := findTool(t, ag, "subagent").Run(ctx, json.RawMessage(`{"prompt":"go","background":true}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	tasks := ag.Tasks().List()
	if len(tasks) != 1 {
		t.Fatalf("expected the task registered before the call returned, got %d", len(tasks))
	}
	if !strings.Contains(out, "worktree") {
		t.Fatalf("with isolation on, the result should name the worktree: %q", out)
	}
	// The worktree instruction reaches the subagent: queued on its pending
	// list (drained at its first loop boundary) rather than a mid-run steer
	// that a fast-settling task would lose. Depending on timing it's either
	// still pending or already delivered to Messages — both prove delivery.
	sub := tasks[0].sub
	<-tasks[0].Done // settle so pending drains deterministically
	found := false
	for _, msg := range sub.Messages {
		if msg.Role == "user" && strings.Contains(msg.Content, "git worktree at") {
			found = true
		}
	}
	for _, p := range sub.PendingSteers() {
		if strings.Contains(p, "git worktree at") {
			found = true
		}
	}
	if !found {
		t.Fatal("worktree path should reach the subagent (queued or delivered)")
	}
	ag.Tasks().Cancel(tasks[0].ID)
}
