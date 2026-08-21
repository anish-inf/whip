package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sseServer(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "bad auth: "+got, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			w.Write([]byte(l + "\n\n"))
		}
	}))
}

func TestModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Method != http.MethodGet {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "bad auth: "+got, http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"object":"list","data":[
			{"id":"claude-fable-5","reasoning_efforts":["none","low","high","max"],"context_length":1000000},
			{"id":"gemini-3.5-flash"}
		]}`))
	}))
	defer srv.Close()

	models, err := New(srv.URL, "test-key").Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models: %+v", models)
	}
	if models[0].ID != "claude-fable-5" || len(models[0].ReasoningEfforts) != 4 || models[0].ReasoningEfforts[3] != "max" {
		t.Fatalf("model 0: %+v", models[0])
	}
	if models[0].ContextLength != 1000000 {
		t.Fatalf("context length: %+v", models[0])
	}
	if len(models[1].ReasoningEfforts) != 0 {
		t.Fatalf("model 1 should have no efforts: %+v", models[1])
	}
}

func TestModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "k").Models(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestStreamTextAndToolCalls(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"content":"hel"}}]}`,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		`: comment to ignore`,
		`data: not-json-is-skipped`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"ba","arguments":"{\"comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"sh","arguments":"and\":\"ls\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	var streamed strings.Builder
	msg, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, func(d string) { streamed.WriteString(d) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "hello" || streamed.String() != "hello" {
		t.Fatalf("content: %q streamed: %q", msg.Content, streamed.String())
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool calls: %+v", msg.ToolCalls)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "c1" || tc.Function.Name != "bash" || tc.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("tool call assembly: %+v", tc)
	}
}

func TestStreamLengthDiscardsToolCalls(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	msg, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 0 {
		t.Fatalf("truncated tool calls must be discarded: %+v", msg.ToolCalls)
	}
	if !strings.Contains(msg.Content, "truncated") {
		t.Fatalf("expected truncation note, got %q", msg.Content)
	}
}

func TestStreamAPIError(t *testing.T) {
	srv := sseServer(t, `data: {"error":{"message":"boom"}}`)
	defer srv.Close()
	_, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected api error, got %v", err)
	}
}

func TestStreamReasoningRoutedToOnThink(t *testing.T) {
	srv := sseServer(t,
		`data: {"choices":[{"delta":{"reasoning_content":"think","role":"assistant"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"ing…"}}]}`,
		`data: {"choices":[{"delta":{"content":"4"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	defer srv.Close()

	var think, text strings.Builder
	msg, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"},
		func(d string) { text.WriteString(d) }, func(d string) { think.WriteString(d) })
	if err != nil {
		t.Fatal(err)
	}
	if think.String() != "thinking…" {
		t.Fatalf("reasoning: %q", think.String())
	}
	if text.String() != "4" || msg.Content != "4" {
		t.Fatalf("content: %q msg: %q", text.String(), msg.Content)
	}
}

func TestStreamHTTPError(t *testing.T) {
	c := New("http://x/", "wrong-key")
	srv := sseServer(t)
	defer srv.Close()
	c.BaseURL = srv.URL
	_, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}

func TestNewTool(t *testing.T) {
	tool := NewTool("x", "desc", `{"type":"object"}`)
	if tool.Type != "function" || tool.Function.Name != "x" || string(tool.Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("%+v", tool)
	}
}

func TestStreamTransportErrors(t *testing.T) {
	if _, err := New("http://\x7f", "k").Stream(context.Background(), Request{}, nil, nil); err == nil {
		t.Fatal("expected bad-url error")
	}
	srv := sseServer(t)
	srv.Close() // connection refused
	if _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{}, nil, nil); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestReasoningEffortSerialized(t *testing.T) {
	b, _ := json.Marshal(Request{Model: "m", ReasoningEffort: "high"})
	if !strings.Contains(string(b), `"reasoning_effort":"high"`) {
		t.Fatalf("missing effort: %s", b)
	}
	b, _ = json.Marshal(Request{Model: "m"})
	if strings.Contains(string(b), "reasoning_effort") {
		t.Fatalf("empty effort must be omitted: %s", b)
	}
}
