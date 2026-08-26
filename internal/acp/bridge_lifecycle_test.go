package acp

// Lifecycle tests: initialize negotiation, session/new, prompt turns,
// streaming + tool cards, queueing, cancellation.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/context-labs/whip/internal/tools"
)

func TestInitializeCapabilities(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))

	resp := f.initialize(t)
	if resp.ProtocolVersion != acp.ProtocolVersionNumber {
		t.Errorf("protocolVersion = %v", resp.ProtocolVersion)
	}
	caps := resp.AgentCapabilities
	if caps.LoadSession {
		t.Error("loadSession should be false without a store")
	}
	if caps.PromptCapabilities.Image {
		t.Error("image should be false for non-vision bridge")
	}
	if !caps.PromptCapabilities.EmbeddedContext {
		t.Error("embeddedContext should be true")
	}
	if !caps.McpCapabilities.Http {
		t.Error("mcp http should be true")
	}
	if resp.AgentInfo == nil || resp.AgentInfo.Name != "whip" || resp.AgentInfo.Version != "test" {
		t.Errorf("agentInfo = %+v", resp.AgentInfo)
	}
	if len(resp.AuthMethods) != 0 {
		t.Errorf("authMethods = %v, want empty", resp.AuthMethods)
	}
}

func TestNewSessionAdvertisesModes(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)

	resp, err := f.conn.NewSession(context.Background(), acp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.SessionId == "" {
		t.Fatal("empty sessionId")
	}
	if resp.Modes == nil || resp.Modes.CurrentModeId != ModeAuto || len(resp.Modes.AvailableModes) != 2 {
		t.Fatalf("modes = %+v", resp.Modes)
	}
}

func TestPromptStreamsTextAndToolCards(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/note.txt"
	srv := scriptServer(t, []step{
		{toolName: "write", toolArgs: `{"path":"` + target + `","content":"hello acp"}`},
		{text: "all done"},
	})
	f := newFixture(t, nil, nil, factoryFor(srv, tools.All()))
	f.initialize(t)
	id := f.newSession(t, dir)

	resp, err := f.prompt(t, id, "write a file")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("stopReason = %v", resp.StopReason)
	}

	ups := f.client.snapshot()
	kinds := summarizeUpdates(ups)
	if !strings.Contains(kinds, "tool_call(") || !strings.Contains(kinds, "tool_call_update(") {
		t.Errorf("missing tool cards: %s", kinds)
	}
	// Final agent chunk carries the reply.
	last := ups[len(ups)-1]
	if last.Update.AgentMessageChunk == nil || last.Update.AgentMessageChunk.Content.Text == nil ||
		last.Update.AgentMessageChunk.Content.Text.Text != "all done" {
		t.Errorf("final update = %+v", last.Update)
	}
	// The write tool card should end completed with a diff content entry.
	tc := f.client.waitFor(t, func(n acp.SessionNotification) bool {
		u := n.Update.ToolCallUpdate
		return u != nil && u.Status != nil && *u.Status == acp.ToolCallStatusCompleted
	}, "completed tool card")
	foundDiff := false
	for _, c := range tc.Update.ToolCallUpdate.Content {
		if c.Diff != nil && c.Diff.Path == target && c.Diff.NewText == "hello acp" {
			foundDiff = true
		}
	}
	if !foundDiff {
		t.Errorf("no diff content in completed write card: %+v", tc.Update.ToolCallUpdate.Content)
	}
	// And the file actually landed on disk (the loop really ran the tool).
	if data, err := osReadFile(target); err != nil || string(data) != "hello acp" {
		t.Errorf("file = %q, %v", data, err)
	}
}

func TestPromptCancelledMidTurn(t *testing.T) {
	release := make(chan struct{})
	srv := scriptServer(t, []step{
		{text: "partial", toolName: "bash", toolArgs: `{"command":"sleep 5","timeout":10}`},
		{text: "unreachable", block: release},
	})
	defer close(release)

	f := newFixture(t, nil, nil, factoryFor(srv, tools.All()))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())

	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, err := f.prompt(t, id, "run something slow")
		done <- result{r, err}
	}()

	// Wait for the turn to actually be running (tool card started), then cancel.
	f.client.waitFor(t, func(n acp.SessionNotification) bool {
		return n.Update.ToolCall != nil
	}, "tool_call start")
	if err := f.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: id}); err != nil {
		t.Fatalf("cancel notification: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("cancelled prompt must not error: %v", r.err)
		}
		if r.resp.StopReason != acp.StopReasonCancelled {
			t.Errorf("stopReason = %v, want cancelled", r.resp.StopReason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prompt did not return after cancel")
	}
}

func TestPromptsQueueWhileBusy(t *testing.T) {
	release := make(chan struct{})
	srv := scriptServer(t, []step{
		{text: "first", block: release},
		{text: "second"},
	})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())

	var wg sync.WaitGroup
	results := make([]string, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := f.prompt(t, id, "one")
		if err != nil {
			t.Errorf("prompt one: %v", err)
			return
		}
		results[0] = string(resp.StopReason)
	}()

	// Prompt 2 goes out only once turn 1 is provably running (its first chunk
	// arrived), so it deterministically queues behind the turn token.
	f.client.waitFor(t, func(n acp.SessionNotification) bool {
		return n.Update.AgentMessageChunk != nil
	}, "first chunk")
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := f.prompt(t, id, "two")
		if err != nil {
			t.Errorf("prompt two: %v", err)
			return
		}
		results[1] = string(resp.StopReason)
	}()
	close(release)

	wg.Wait()

	if results[0] != string(acp.StopReasonEndTurn) || results[1] != string(acp.StopReasonEndTurn) {
		t.Errorf("results = %v", results)
	}
	// Both messages must have landed in the same conversation, in order.
	s := f.bridge.getSession(id)
	msgs := s.ag.MessagesSnapshot()
	var users []string
	for _, m := range msgs {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	if len(users) != 2 || users[0] != "one" || users[1] != "two" {
		t.Errorf("user messages = %v, want [one two]", users)
	}
	// Replies streamed in turn order too: "first" chunks precede "second".
	var texts []string
	for _, n := range f.client.snapshot() {
		if c := n.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
			texts = append(texts, c.Content.Text.Text)
		}
	}
	joined := strings.Join(texts, "")
	if !strings.Contains(joined, "first") || !strings.Contains(joined, "second") ||
		strings.Index(joined, "first") > strings.Index(joined, "second") {
		t.Errorf("streamed order = %q", joined)
	}
}

func TestPromptUnknownSession(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	_, err := f.prompt(t, "nope", "hi")
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestCancelIsSafeWhenIdle(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	if err := f.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: id}); err != nil {
		t.Fatalf("idle cancel: %v", err)
	}
	// session still usable afterwards
	resp, err := f.prompt(t, id, "hello")
	if err != nil || resp.StopReason != acp.StopReasonEndTurn {
		t.Errorf("prompt after idle cancel: %v %v", resp, err)
	}
}

func TestTodoToolEmitsPlanUpdate(t *testing.T) {
	srv := scriptServer(t, []step{
		{toolName: "todowrite", toolArgs: `{"todos":[{"content":"step one","status":"in_progress"},{"content":"step two","status":"pending"}]}`},
		{text: "done"},
	})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())

	if _, err := f.prompt(t, id, "plan it"); err != nil {
		t.Fatal(err)
	}
	plan := f.client.waitFor(t, func(n acp.SessionNotification) bool {
		return n.Update.Plan != nil
	}, "plan update")
	entries := plan.Update.Plan.Entries
	if len(entries) != 2 {
		t.Fatalf("plan entries = %d, want 2", len(entries))
	}
	if entries[0].Content != "step one" || entries[0].Status != acp.PlanEntryStatusInProgress {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1].Status != acp.PlanEntryStatusPending {
		t.Errorf("entry 1 status = %v", entries[1].Status)
	}
}

// Regression for review finding #9: a context-limit error that survives the
// compaction retry must surface as stopReason max_tokens, not a JSON-RPC
// error — the client can then offer a fresh session.
func TestContextLimitMapsToMaxTokens(t *testing.T) {
	// Two phases: prompt 1 succeeds; prompt 2 always hits the context limit,
	// even after the compaction retry — so the surviving error must map to
	// max_tokens. The agent's Messages are padded directly so compact() has
	// enough history to fold (the turn loop's own guard, not our concern).
	var mu sync.Mutex
	limitAlways := false
	srv := scriptServer(t, nil)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// The compaction summary runs non-streaming (llm.Complete posts no
		// "stream" flag) and must SUCCEED — the retry then re-hits the limit
		// and the error surfaces to the turn.
		var req struct {
			Stream bool `json:"stream"`
		}
		var buf strings.Builder
		_, _ = io.Copy(&buf, r.Body)
		_ = json.Unmarshal([]byte(buf.String()), &req)
		mu.Lock()
		limited := limitAlways
		mu.Unlock()
		switch {
		case limited && !req.Stream: // compaction summary
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"summary"}}]}`)
			return
		case limited:
			fmt.Fprint(w, `data: {"error":{"message":"context_length_exceeded: 200000 tokens > 190000 maximum","type":"invalid_request_error"}}`+"\n\n")
		default:
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())
	if _, err := f.prompt(t, id, "warm up the history"); err != nil {
		t.Fatal(err)
	}
	// Pad past compact()'s keep-back so the retry path actually runs.
	s := f.bridge.getSession(id)
	for range 8 {
		s.ag.AppendUser("padding for compaction history")
	}
	mu.Lock()
	limitAlways = true
	mu.Unlock()

	resp, err := f.prompt(t, id, "overflow")
	if err != nil {
		t.Fatalf("context limit must not be a JSON-RPC error: %v", err)
	}
	if resp.StopReason != acp.StopReasonMaxTokens {
		t.Errorf("stopReason = %v, want max_tokens", resp.StopReason)
	}
}
