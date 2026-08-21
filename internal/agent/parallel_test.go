package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/context-labs/loopy/internal/llm"
	"github.com/context-labs/loopy/internal/tools"
)

// slowTool returns a tool that records how many copies of itself are running
// concurrently, to prove parallel execution actually overlaps.
func slowTool(name string, conc *atomic.Int32, maxConc *atomic.Int32) tools.Tool {
	return tools.Tool{
		Def: llm.NewTool(name, "slow", `{"type":"object","properties":{"s":{"type":"string"}}}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			n := conc.Add(1)
			for {
				m := maxConc.Load()
				if n <= m || maxConc.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			conc.Add(-1)
			return name + "-done", nil
		},
	}
}

// parallelServer emits three tool calls in one assistant turn, then a final answer.
func parallelServer(t *testing.T) *httptest.Server {
	t.Helper()
	call := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "text/event-stream")
		if call == 1 {
			for i, id := range []string{"a", "b", "c"} {
				args := fmt.Sprintf(`{\"s\":%q}`, id)
				fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":%d,"id":%q,"type":"function","function":{"name":"slow","arguments":%q}}]}}]}`+"\n\n", i, id, args)
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestToolCallsRunInParallel(t *testing.T) {
	srv := parallelServer(t)
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	var conc, maxConc atomic.Int32
	// one shared tool named "slow" — all three calls hit it
	ag.Tools = []tools.Tool{slowTool("slow", &conc, &maxConc)}

	if _, err := ag.Turn(context.Background(), "go", Events{}); err != nil {
		t.Fatal(err)
	}
	if maxConc.Load() < 2 {
		t.Fatalf("tool calls did not overlap: max concurrency %d", maxConc.Load())
	}
}

// Two edits to the SAME path must serialize (per-path lock), even though
// unrelated calls run in parallel.
func TestSamePathEditsSerialize(t *testing.T) {
	// craft an agent whose runTools we drive directly
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")

	var conc, maxConc atomic.Int32
	write := tools.Tool{
		Def: llm.NewTool("write", "w", `{"type":"object","properties":{"path":{"type":"string"}}}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			n := conc.Add(1)
			for {
				m := maxConc.Load()
				if n <= m || maxConc.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			conc.Add(-1)
			return "ok", nil
		},
	}
	ag.Tools = []tools.Tool{write}

	calls := []llm.ToolCall{
		{ID: "1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "write", Arguments: `{"path":"/tmp/same.go"}`}},
		{ID: "2", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "write", Arguments: `{"path":"/tmp/same.go"}`}},
	}
	ag.runTools(context.Background(), calls, Events{})
	if maxConc.Load() != 1 {
		t.Fatalf("same-path writes must serialize (max concurrency 1), got %d", maxConc.Load())
	}
}

// Background tasks run concurrently with the parent and deliver their report
// via the Done channel + a steered message.
func TestBackgroundTaskDeliversReport(t *testing.T) {
	srv := textServer(t, func(n int, req llm.Request) string { return "report-body" })
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground(context.Background(), "probe", "do the thing")

	// wait on the Done channel — closes exactly once on settle
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	snap, ok := ag.Tasks().Get(task.ID)
	if !ok {
		t.Fatal("task not in registry")
	}
	if snap.Status != TaskDone || snap.Report != "report-body" {
		t.Fatalf("settled task: %+v", snap)
	}

	// the report should be queued for steering into the parent. Steer runs in
	// the task goroutine right after settle closes Done, so poll briefly.
	var pending []string
	for i := 0; i < 100; i++ {
		if pending = ag.drainPending(); len(pending) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(pending) != 1 || !strings.Contains(pending[0], "report-body") {
		t.Fatalf("expected steered report, got %v", pending)
	}
}

// Multiple waiters all get woken by the single channel close — the property
// that makes this cheap in Go (opencode needs a per-waiter Deferred).
func TestBackgroundTaskBroadcastsToManyWaiters(t *testing.T) {
	srv := textServer(t, func(n int, req llm.Request) string {
		time.Sleep(50 * time.Millisecond) // give waiters time to attach
		return "ok"
	})
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground(context.Background(), "d", "p")

	const waiters = 8
	var woken atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-task.Done:
				woken.Add(1)
			case <-time.After(5 * time.Second):
			}
		}()
	}
	wg.Wait()
	if woken.Load() != waiters {
		t.Fatalf("only %d/%d waiters woke on close", woken.Load(), waiters)
	}
}

// Cancel marks the task cancelled and closes Done.
func TestBackgroundTaskCancel(t *testing.T) {
	// a server that hangs until cancelled
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		<-r.Context().Done() // block until the client (subagent ctx) is cancelled
	}))
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground(context.Background(), "d", "p")
	if !ag.Tasks().Cancel(task.ID) {
		t.Fatal("cancel should succeed on a running task")
	}
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled task never settled")
	}
	snap, _ := ag.Tasks().Get(task.ID)
	if snap.Status != TaskCancelled {
		t.Fatalf("status: %s", snap.Status)
	}
	if ag.Tasks().Cancel(task.ID) {
		t.Fatal("cancel on a settled task should report false")
	}
}

// Per-path keys canonicalize so ./x.go and x.go share one lock.
func TestCanonicalPathKey(t *testing.T) {
	a := canonicalPathKey("foo/../bar/baz.go")
	b := canonicalPathKey("bar/baz.go")
	if a != b {
		t.Fatalf("canonical keys differ: %q vs %q", a, b)
	}
}

// toolMutationPath pulls the path out of write/edit args and reports
// non-path-scoped for everything else.
func TestToolMutationPath(t *testing.T) {
	if p, ok := toolMutationPath("write", `{"path":"/a/b.go"}`); !ok || p != "/a/b.go" {
		t.Fatalf("write: %q %v", p, ok)
	}
	if p, ok := toolMutationPath("edit", `{"path":"rel.go"}`); !ok || p != "rel.go" {
		t.Fatalf("edit: %q %v", p, ok)
	}
	if _, ok := toolMutationPath("bash", `{"command":"ls"}`); ok {
		t.Fatal("bash must be global (not path-scoped)")
	}
	if _, ok := toolMutationPath("read", `{"path":"/a"}`); ok {
		t.Fatal("read is not a mutation")
	}
	if _, ok := toolMutationPath("write", `{bad`); ok {
		t.Fatal("malformed write args fall back to global")
	}
}

// Subscribers registered via Subscribe receive the task's live event stream
// (fanned in with usage accounting); a settled task rejects new subscribers.
func TestBackgroundTaskSubscribersSeeLiveStream(t *testing.T) {
	srv := textServer(t, func(n int, req llm.Request) string {
		time.Sleep(50 * time.Millisecond) // let the subscriber attach
		return "stream-body"
	})
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	task := ag.StartBackground(context.Background(), "d", "p")

	var got atomic.Int32
	ok := ag.Tasks().Subscribe(task.ID, Events{OnText: func(s string) { got.Add(int32(len(s))) }})
	if !ok {
		t.Fatal("Subscribe on a running task should succeed")
	}
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task never settled")
	}
	if got.Load() == 0 {
		t.Fatal("subscriber saw no text events")
	}
	if ag.Tasks().Subscribe(task.ID, Events{}) {
		t.Fatal("Subscribe on a settled task should report false")
	}
}

// FanIn forwards each fired callback to every source that implements it.
func TestFanIn(t *testing.T) {
	var a, b, usage atomic.Int32
	ev := FanIn(
		Events{OnText: func(string) { a.Add(1) }, OnUsage: func(llm.Usage) { usage.Add(1) }},
		Events{OnText: func(string) { b.Add(1) }},
	)
	ev.OnText("x")
	ev.OnThink("y") // nobody implements it: no panic
	ev.OnUsage(llm.Usage{})
	if a.Load() != 1 || b.Load() != 1 || usage.Load() != 1 {
		t.Fatalf("fan-in miscounted: a=%d b=%d usage=%d", a.Load(), b.Load(), usage.Load())
	}
}
