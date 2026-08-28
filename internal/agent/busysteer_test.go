package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/llm"
)

// In-flight counts rise while a tool runs and drain when it finishes. Driving
// a registry emitter directly keeps the window deterministic (an httptest
// tool call returns too fast to observe).
func TestInFlightToolsTracking(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	ag.trackTool("read", 1)
	ag.trackTool("read", 1)
	ag.trackTool("bash", 1)
	if got := ag.InFlightTools(); len(got) != 2 {
		t.Fatalf("in-flight names = %v, want 2 distinct tools", got)
	}
	ag.trackTool("read", -2)
	if got := ag.InFlightTools(); len(got) != 1 || got[0] != "bash" {
		t.Fatalf("after reads finish, in-flight = %v, want [bash]", got)
	}
	ag.trackTool("bash", -1)
	if got := ag.InFlightTools(); len(got) != 0 {
		t.Fatalf("in-flight set should drain, got %v", got)
	}
}

// WaitingOnSubagents is true only while a turn runs AND every in-flight tool
// is a subagent; a bash in flight flips it false.
func TestWaitingOnSubagentsGating(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")

	if ag.WaitingOnSubagents() {
		t.Fatal("no turn running → not waiting")
	}
	ag.running.Store(true)
	if ag.WaitingOnSubagents() {
		t.Fatal("turn running but nothing in flight → mid-generation, not waiting")
	}
	ag.trackTool("subagent", 1)
	if !ag.WaitingOnSubagents() {
		t.Fatal("only a subagent in flight → waiting")
	}
	ag.trackTool("subagent", 1) // two subagents still qualifies
	if !ag.WaitingOnSubagents() {
		t.Fatal("multiple subagents in flight → waiting")
	}
	ag.trackTool("bash", 1)
	if ag.WaitingOnSubagents() {
		t.Fatal("a bash in flight → not waiting on subagents")
	}
	ag.trackTool("bash", -1)
	ag.trackTool("subagent", -2)
	if ag.WaitingOnSubagents() {
		t.Fatal("all tools finished → not waiting")
	}
}

// End-to-end: during a real turn blocked on a foreground subagent, the parent
// reports WaitingOnSubagents; once the subagent's report lands and the turn
// continues, it flips false. The subagent's HTTP call blocks on a latch so the
// window is deterministic.
func TestWaitingOnSubagentsDuringForegroundSubagent(t *testing.T) {
	release := make(chan struct{})
	subStarted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.Request
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case len(req.Messages) > 0 && strings.HasPrefix(req.Messages[0].Content, "You are a subagent"):
			close(subStarted)
			<-release // hold the subagent's turn until the test has observed the parent
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"sub report"},"finish_reason":"stop"}]}`+"\n\n")
		case len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Role == "user" && req.Messages[len(req.Messages)-1].Content == "go":
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"subagent","arguments":"{\"prompt\":\"explore\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		default:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"parent done"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ag := New(llm.New(srv.URL, "k"), "m", 100, "sys")
	done := make(chan error, 1)
	go func() {
		_, err := ag.Turn(t.Context(), "go", Events{})
		done <- err
	}()

	select {
	case <-subStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("subagent never started")
	}
	if !ag.WaitingOnSubagents() {
		t.Fatal("parent blocked on a foreground subagent must report WaitingOnSubagents")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ag.WaitingOnSubagents() {
		t.Fatal("turn finished → no longer waiting")
	}
}
