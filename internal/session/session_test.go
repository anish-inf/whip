package session

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/llm"
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
	sent := time.Date(2025, 6, 1, 14, 30, 0, 0, time.UTC)
	use := llm.Usage{PromptTokens: 12, CompletionTokens: 4}
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first question here", Authored: true, SentAt: &sent},
		{
			Role: "assistant", Content: "the answer", Usage: &use, Model: "kimi-k3-fast @ inference",
			ToolCalls: []llm.ToolCall{{ID: "c1", DurationMs: 42, ExitCode: 0}},
		},
		{Role: "tool", Content: "c1 result", ToolCallID: "c1", Name: "bash"},
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
	if len(got) != 5 || got[0].Role != "user" || got[4].Content != "final\nanswer" {
		t.Fatalf("messages: %+v", got)
	}
	// the submission timestamp must survive the round trip (rewind picker)
	if got[0].SentAt == nil || !got[0].SentAt.Equal(sent) {
		t.Fatalf("SentAt did not round-trip: %+v", got[0])
	}
	// so must the per-message usage, model, and tool timing
	asst := got[1]
	if asst.Usage == nil || asst.Usage.PromptTokens != 12 || asst.Usage.CompletionTokens != 4 {
		t.Fatalf("usage did not round-trip: %+v", asst.Usage)
	}
	if asst.Model != "kimi-k3-fast @ inference" {
		t.Fatalf("model did not round-trip: %q", asst.Model)
	}
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].DurationMs != 42 {
		t.Fatalf("tool timing did not round-trip: %+v", asst.ToolCalls)
	}

	u, a := st.LastExchange(id)
	if u != "follow-up" || a != "final\nanswer" {
		t.Fatalf("last exchange: %q %q", u, a)
	}
	// fully-answered history passes through Load unchanged (no synthesis).
	// Save(id, 1, …) skips the system row, so one fewer row is stored.
	if len(got) != len(msgs)-1 {
		t.Fatalf("answered history must load verbatim: got %d, saved %d", len(got), len(msgs))
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
	if _, got, _ = st.Load(id); len(got) != 5 {
		t.Fatalf("re-save duplicated rows: %d", len(got))
	}
}

func TestEffortRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(id, 1, []llm.Message{{Role: "system"}, {Role: "user", Content: "q"}}, "m", "p"); err != nil {
		t.Fatal(err)
	}

	// a fresh row has no per-session effort: resume inherits the global default
	meta, _, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Effort != "" {
		t.Fatalf("new session should carry no effort, got %q", meta.Effort)
	}

	if err := st.SetEffort(id, "high"); err != nil {
		t.Fatal(err)
	}
	meta, _, _ = st.Load(id)
	if meta.Effort != "high" {
		t.Fatalf("effort did not round-trip: %q", meta.Effort)
	}

	// a fork inherits the parent's effort
	forkID, err := st.Fork(id, 1, "copy")
	if err != nil {
		t.Fatal(err)
	}
	fmeta, _, err := st.Load(forkID)
	if err != nil {
		t.Fatal(err)
	}
	if fmeta.Effort != "high" {
		t.Fatalf("fork should inherit effort, got %q", fmeta.Effort)
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

// History recall must skip messages whip injected on the user's behalf
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
	st.db.ExecContext(context.Background(), `UPDATE messages SET content='{bad' WHERE session_id=?`, id1)
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

// An interrupted turn (ctrl+c / crash) persists an assistant tool_call with
// no result; Load must synthesize an error result so the resumed conversation
// satisfies the API's tool_call/tool-result pairing contract.
func TestLoadSynthesizesDanglingToolResults(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	call := func(id, name string) llm.ToolCall {
		var tc llm.ToolCall
		tc.ID, tc.Function.Name = id, name
		return tc
	}
	id, _ := st.Create("/tmp", "m", "p")
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},
		// crash between the two parallel calls' results: c1 answered, c2 dangling
		{Role: "assistant", ToolCalls: []llm.ToolCall{call("c1", "read"), call("c2", "bash")}},
		{Role: "tool", Content: "file body", ToolCallID: "c1", Name: "read"},
		{Role: "user", Content: "next"},
		// a whole tool batch lost to the crash
		{Role: "assistant", ToolCalls: []llm.ToolCall{call("c3", "edit"), call("c4", "write")}},
	}
	if err := st.Save(id, 1, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}

	_, got, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	// c2's synthetic result lands right after its assistant message (ahead of
	// c1's real one — result order among a batch's calls is free; only the
	// pairing matters); c3 and c4 each get their own at the end of history.
	wantRoles := []string{"user", "assistant", "tool", "tool", "user", "assistant", "tool", "tool"}
	if len(got) != len(wantRoles) {
		t.Fatalf("loaded %d messages, want %d: %+v", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("message %d: role %q, want %q (%+v)", i, got[i].Role, role, got)
		}
	}
	for i, id := range map[int]string{2: "c2", 6: "c3", 7: "c4"} {
		m := got[i]
		if m.ToolCallID != id || m.Name == "" || !strings.Contains(m.Content, "interrupted") {
			t.Fatalf("synthetic result %d malformed: %+v", i, m)
		}
	}
	// the real result is untouched
	if got[3].Content != "file body" {
		t.Fatalf("answered result changed: %+v", got[3])
	}
}

// Compaction is an event, not a rewrite: the raw log survives, Load derives
// the compacted view, and a bad compaction can be deleted and retried.
func TestCompactionEvent(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, _ := st.Create("/tmp", "m", "p")
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	if err := st.Save(id, 0, msgs, "m", "p"); err != nil {
		t.Fatal(err)
	}
	rawBefore := len(st.RawMessages(id))

	// compact: fold q1/a1/q2 into a summary, keep the tail from seq 4
	if err := st.RecordCompaction(id, 4, "q1/q2 were about testing"); err != nil {
		t.Fatal(err)
	}

	// the raw log is untouched
	if got := len(st.RawMessages(id)); got != rawBefore {
		t.Fatalf("raw log must survive compaction: %d → %d", rawBefore, got)
	}

	// Load derives the view: system + summary + tail from cutoff
	// (raw: sys q1 a1 q2 a2 q3 a3; cutoff 4 keeps a2 q3 a3)
	_, got, err := st.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []string{"system", "system", "assistant", "user", "assistant"}
	if len(got) != len(wantRoles) {
		t.Fatalf("derived view: %d messages, want %d: %+v", len(got), len(wantRoles), got)
	}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("view message %d: role %q, want %q", i, got[i].Role, role)
		}
	}
	if !strings.Contains(got[1].Content, "q1/q2 were about testing") {
		t.Fatalf("summary message: %q", got[1].Content)
	}

	// a later turn appends new rows to the raw log (the TUI only ever saves
	// the compacted in-memory history's NEW tail, which is raw rows)
	if err := st.Save(id, 7, []llm.Message{
		{},
		{},
		{},
		{},
		{},
		{},
		{}, // placeholder rows 0..6 (already stored)
		{Role: "user", Content: "q4"},
		{Role: "assistant", Content: "a4"},
	}, "m", "p"); err != nil {
		t.Fatal(err)
	}
	if raw := st.RawMessages(id); len(raw) != 9 {
		t.Fatalf("post-compaction save should append, not rewrite: %d raw rows", len(raw))
	}
	// the view still holds (cutoff still points at the raw boundary)
	_, got, _ = st.Load(id)
	if len(got) != 7 || got[2].Content != "a2" || got[6].Content != "a4" {
		t.Fatalf("view after save: %+v", got)
	}

	// the event is inspectable
	events := st.Compactions(id)
	if len(events) != 1 || events[0].Cutoff != 4 || events[0].Summary != "q1/q2 were about testing" {
		t.Fatalf("compaction events: %+v", events)
	}

	// retry: delete the bad event, the raw log loads verbatim again
	if err := st.DeleteCompaction(id, 1); err != nil {
		t.Fatal(err)
	}
	_, got, _ = st.Load(id)
	if len(got) != 9 || got[1].Content != "q1" || got[8].Content != "a4" {
		t.Fatalf("after deleting the event, raw history should load: %+v", got)
	}
}
