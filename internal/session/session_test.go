package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/context-labs/loopy/internal/llm"
)

func TestTaskRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	// start writes the running row…
	if err := st.SaveTask(id, Task{ID: "task-1", Description: "probe", Prompt: "look around", Status: "running", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	// …settle upserts the same row with the final state
	end := time.Now()
	if err := st.SaveTask(id, Task{ID: "task-1", Description: "probe", Prompt: "look around", Status: "done", Report: "the report", StartedAt: start, EndedAt: end}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTask(id, Task{ID: "task-2", Description: "other", Prompt: "p", Status: "error", Report: "boom", StartedAt: start.Add(time.Second), EndedAt: end}); err != nil {
		t.Fatal(err)
	}

	tasks, err := st.LoadTasks(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (the upsert must not duplicate), got %d", len(tasks))
	}
	if tasks[0].ID != "task-1" || tasks[0].Status != "done" || tasks[0].Report != "the report" {
		t.Fatalf("task-1 should hold the settled state, got %+v", tasks[0])
	}
	if tasks[0].EndedAt.IsZero() {
		t.Fatal("ended_at should round-trip")
	}
	if tasks[1].ID != "task-2" || tasks[1].Status != "error" {
		t.Fatalf("task-2: %+v", tasks[1])
	}
	// tasks belong to their session only
	if other, _ := st.Create("/tmp", "m", "p"); true {
		if got, _ := st.LoadTasks(other); len(got) != 0 {
			t.Fatalf("a fresh session should have no tasks, got %d", len(got))
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "kimi-k3-fast", "inference")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first question here"},
		{Role: "assistant", Content: "the answer"},
		{Role: "user", Content: "follow-up"},
		{Role: "assistant", Content: "final\nanswer"},
	}
	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}

	meta, got, err := st.Load(id[:4]) // prefix resolution
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.Title != "first question here" {
		t.Fatalf("meta: %+v", meta)
	}
	if len(got) != 4 || got[0].Role != "user" || got[3].Content != "final\nanswer" {
		t.Fatalf("messages: %+v", got)
	}

	u, a := st.LastExchange(id)
	if u != "follow-up" || a != "final\nanswer" {
		t.Fatalf("last exchange: %q %q", u, a)
	}

	recent, err := st.Recent(10)
	if err != nil || len(recent) != 1 || recent[0].ID != id {
		t.Fatalf("recent: %v %v", recent, err)
	}

	if _, _, err := st.Load("zzzz"); err == nil {
		t.Fatal("expected not-found error")
	}

	// idempotent re-save must not duplicate
	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}
	if _, got, _ = st.Load(id); len(got) != 4 {
		t.Fatalf("re-save duplicated rows: %d", len(got))
	}
}

func TestUserHistory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// two sessions in different folders; the newer one typed last
	a, _ := st.Create("/proj/a", "m", "p")
	st.Save(a, 0, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "from folder A", Authored: true},
		{Role: "assistant", Content: "ans"},
	}, "m", "p")
	b, _ := st.Create("/proj/b", "m", "p")
	st.Save(b, 0, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "from folder B", Authored: true},
		{Role: "assistant", Content: "ans"},
		{Role: "user", Content: "from folder A", Authored: true}, // duplicate of A's message
	}, "m", "p")

	hist, err := st.UserHistory(0)
	if err != nil {
		t.Fatal(err)
	}
	// newest session first, its newest message first; the cross-session
	// duplicate collapses to one entry
	want := []string{"from folder A", "from folder B"}
	if strings.Join(hist, "|") != strings.Join(want, "|") {
		t.Fatalf("UserHistory = %v, want %v", hist, want)
	}

	// limit respected
	lim, _ := st.UserHistory(1)
	if len(lim) != 1 {
		t.Fatalf("limit: got %d", len(lim))
	}
}

// History recall must skip messages loopy injected on the user's behalf
// (steered background-task results, goal-continuation prompts) — only genuinely
// typed submissions are recalled.
func TestUserHistorySkipsInjected(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, _ := st.Create("/proj/x", "m", "p")
	st.Save(id, 0, []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "real question I typed", Authored: true},
		{Role: "assistant", Content: "ans"},
		{Role: "user", Content: "[background task task-1 done] some report\n\n…"}, // injected, Authored=false
		{Role: "user", Content: "[goal check] The session goal is:\n…"},           // injected, Authored=false
		{Role: "user", Content: "another typed message", Authored: true},
	}, "m", "p")

	hist, err := st.UserHistory(0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"another typed message", "real question I typed"}
	if strings.Join(hist, "|") != strings.Join(want, "|") {
		t.Fatalf("UserHistory = %v, want only typed messages %v", hist, want)
	}
}

func TestStoreEdgeCases(t *testing.T) {
	if _, err := Open("/nonexistent-dir/x.db"); err == nil {
		t.Fatal("expected open error")
	}
	if truncate(strings.Repeat("a", 100), 10) != strings.Repeat("a", 9)+"…" {
		t.Fatal("truncate long")
	}

	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id1, _ := st.Create("/tmp", "m", "p")
	id2, _ := st.Create("/tmp", "m", "p")
	msgs := []llm.Message{{Role: "system"}, {Role: "user", Content: "q"}}
	st.Save(id1, 1, msgs, "m", "p")
	st.Save(id2, 1, msgs, "m", "p")
	if _, _, err := st.Load(""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous, got %v", err)
	}
	// LastExchange on a session with no assistant messages
	u, a := st.LastExchange(id1)
	if u != "q" || a != "" {
		t.Fatalf("last exchange: %q %q", u, a)
	}
	// corrupt message row surfaces a load error
	st.db.Exec(`UPDATE messages SET content='{bad' WHERE session_id=?`, id1)
	if _, _, err := st.Load(id1); err == nil {
		t.Fatal("expected corrupt-row error")
	}
}

func TestGoalPersistence(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, _ := st.Create("/tmp", "m", "p")
	st.Save(id, 1, []llm.Message{{Role: "system"}, {Role: "user", Content: "q"}}, "m", "p")

	if err := st.SetGoal(id, "finish the thing"); err != nil {
		t.Fatal(err)
	}
	meta, _, err := st.Load(id)
	if err != nil || meta.Goal != "finish the thing" {
		t.Fatalf("goal not restored: %+v %v", meta, err)
	}
	st.SetGoal(id, "")
	if meta, _, _ = st.Load(id); meta.Goal != "" {
		t.Fatalf("goal not cleared: %+v", meta)
	}
}
