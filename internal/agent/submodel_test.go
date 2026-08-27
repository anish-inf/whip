package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/tools"
)

// modelRecorder is a text server that also records the model id of every
// request, so routing tests can assert which model a subagent ran on.
func modelRecorder(t *testing.T, reply string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.Request
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(reply)
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}]}`+"\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), models...)
	}
}

// findTool digs a named tool out of an agent's built-in set.
func findTool(t *testing.T, a *Agent, name string) tools.Tool {
	t.Helper()
	for _, tl := range a.Tools {
		if tl.Def.Function.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not registered", name)
	return tools.Tool{}
}

// A task-call model override routes the subagent through ResolveModel.
func TestTaskModelOverride(t *testing.T) {
	srv, models := modelRecorder(t, "sub-report")
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "parent-model", 100, "sys")
	ag.ResolveModel = func(model, provider string) (SubModel, error) {
		if model != "pick" || provider != "prov" {
			t.Errorf("resolver got (%q,%q)", model, provider)
		}
		return SubModel{Client: llm.New(srv.URL, "k"), Model: "override-model"}, nil
	}
	out, err := findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go","model":"pick","provider":"prov"}`))
	if err != nil || out != "sub-report" {
		t.Fatalf("task run: %q, %v", out, err)
	}
	if got := models(); len(got) != 1 || got[0] != "override-model" {
		t.Fatalf("subagent should run on the override model, requests saw %v", got)
	}
}

// TaskDefault routes background subagents when no override is given; the
// zero TaskDefault falls back to the parent's model.
func TestTaskDefaultRoutesSubagents(t *testing.T) {
	srv, models := modelRecorder(t, "ok")
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "parent-model", 100, "sys")
	ag.TaskDefault = SubModel{Client: llm.New(srv.URL, "k"), Model: "task-default-model"}
	task := ag.StartBackground("d", "p", SubModel{})
	<-task.Done
	if got := models(); len(got) != 1 || got[0] != "task-default-model" {
		t.Fatalf("background subagent should run on TaskDefault, requests saw %v", got)
	}

	ag.TaskDefault = SubModel{}
	task = ag.StartBackground("d", "p", SubModel{})
	<-task.Done
	if got := models(); len(got) != 2 || got[1] != "parent-model" {
		t.Fatalf("zero TaskDefault should fall back to the parent model, requests saw %v", got)
	}
}

// An unresolvable model override is tool output (the turn survives), and a
// nil resolver rejects overrides the same way.
func TestTaskModelOverrideErrors(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	out, err := findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go","model":"nope"}`))
	if err != nil || !strings.HasPrefix(out, "Error:") {
		t.Fatalf("nil resolver should reject via tool output, got %q, %v", out, err)
	}
	ag.ResolveModel = func(model, provider string) (SubModel, error) {
		return SubModel{}, fmt.Errorf("unknown model %q", model)
	}
	out, err = findTool(t, ag, "subagent").Run(context.Background(),
		json.RawMessage(`{"prompt":"go","model":"nope"}`))
	if err != nil || !strings.Contains(out, `unknown model "nope"`) {
		t.Fatalf("resolver error should be tool output, got %q, %v", out, err)
	}
}

// SteerTask lands in the running subagent's conversation at its next loop
// boundary; the task_steer tool reports errors as tool output.
func TestSteerTaskReachesRunningSubagent(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var reqs []llm.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.Request
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		n := len(reqs)
		mu.Unlock()
		if n == 1 {
			<-release // hold the first response until the steer is queued
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "p", SubModel{})
	if err := ag.SteerTask(task.ID, "change course"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("steer should force a second round, saw %d requests", len(reqs))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "change course") {
		t.Fatalf("steered text should be the second request's last message, got %+v", last)
	}

	if err := ag.SteerTask("task-nope", "x"); err == nil {
		t.Fatal("unknown task should error")
	}
	if err := ag.SteerTask(task.ID, "x"); err == nil {
		t.Fatal("steering a settled task should error")
	}
	out, err := findTool(t, ag, "subagent_steer").Run(context.Background(),
		json.RawMessage(`{"id":"task-nope","message":"x"}`))
	if err != nil || !strings.HasPrefix(out, "Error:") {
		t.Fatalf("subagent_steer errors must be tool output, got %q, %v", out, err)
	}
}

// FollowupTask chats on the settled subagent's retained context without
// disturbing the settled lifecycle (status, report, closed Done).
func TestFollowupTaskChatsOnRetainedContext(t *testing.T) {
	srv, _ := modelRecorder(t, "reply")
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground("d", "the original prompt", SubModel{})
	<-task.Done

	if _, err := ag.FollowupTask(context.Background(), task.ID, "and now?", Events{}); err != nil {
		t.Fatal(err)
	}
	snap, _ := ag.Tasks().Get(task.ID)
	if snap.Status != TaskDone || snap.Report != "reply" {
		t.Fatalf("follow-up must not disturb the settled task's status/report: %+v", snap)
	}
	if _, err := ag.FollowupTask(context.Background(), "task-nope", "x", Events{}); err == nil {
		t.Fatal("unknown task should error")
	}
	restored := BackgroundTask{ID: "task-r", Status: TaskDone, Restored: true}
	ag.RestoreTask(restored)
	if _, err := ag.FollowupTask(context.Background(), "task-r", "x", Events{}); err == nil {
		t.Fatal("restored task (no live subagent) should error")
	}
}

// ClearSettled(keep) protects the named ids from the sweep.
func TestClearSettledKeep(t *testing.T) {
	r := newTaskRegistry()
	for _, id := range []string{"task-a", "task-b"} {
		r.tasks[id] = &BackgroundTask{ID: id, Status: TaskDone, Done: make(chan struct{}), cancel: func() {}}
	}
	if n := r.ClearSettled("task-a"); n != 1 {
		t.Fatalf("cleared %d, want 1", n)
	}
	if _, ok := r.Get("task-a"); !ok {
		t.Fatal("kept id must survive the sweep")
	}
	if _, ok := r.Get("task-b"); ok {
		t.Fatal("unkept settled id must be swept")
	}
}
