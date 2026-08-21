package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// The internal Authored marker (input-history bookkeeping) must never reach the
// provider — the request body must not contain it even when a message carries it.
func TestStreamStripsAuthoredFlag(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	msgs := []Message{{Role: "user", Content: "typed by me", Authored: true}}
	if _, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m", Messages: msgs}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "authored") {
		t.Fatalf("Authored flag leaked to provider: %s", body)
	}
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
	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, func(d string) { streamed.WriteString(d) }, nil)
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

	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil)
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
	_, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"}, nil, nil)
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
	msg, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{Model: "m"},
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
	_, _, err := c.Stream(context.Background(), Request{Model: "m"}, nil, nil)
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
	if _, _, err := New("http://\x7f", "k").Stream(context.Background(), Request{}, nil, nil); err == nil {
		t.Fatal("expected bad-url error")
	}
	srv := sseServer(t)
	srv.Close() // connection refused
	if _, _, err := New(srv.URL, "test-key").Stream(context.Background(), Request{}, nil, nil); err == nil {
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

func TestComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"a summary"}}]}`))
	}))
	defer srv.Close()

	got, _, err := New(srv.URL, "test-key").Complete(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a summary" {
		t.Fatalf("complete: %q", got)
	}
}

func TestCompleteStreamOmitted(t *testing.T) {
	var req Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&req)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	if _, _, err := New(srv.URL, "k").Complete(context.Background(), Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if req.Stream {
		t.Fatalf("Complete must send stream:false, got %v", req.Stream)
	}
}

func TestStreamUsageParsed(t *testing.T) {
	var reqSeen Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&reqSeen)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":45,"prompt_tokens_details":{"cached_tokens":800}}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	_, u, err := New(srv.URL, "k").Stream(context.Background(), Request{Model: "m"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.PromptTokens != 1200 || u.CompletionTokens != 45 || u.Cached() != 800 {
		t.Fatalf("usage: %+v", u)
	}
	if reqSeen.StreamOptions == nil || !reqSeen.StreamOptions.IncludeUsage {
		t.Fatal("Stream must request stream_options.include_usage")
	}
}

func TestCompleteUsageParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}`))
	}))
	defer srv.Close()

	_, u, err := New(srv.URL, "k").Complete(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if u.PromptTokens != 10 || u.CompletionTokens != 5 || u.Cached() != 4 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestIsContextLimit(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&HTTPError{Status: "400", Body: `{"error":{"code":"context_length_exceeded"}}`}, true},
		{&HTTPError{Status: "400", Body: "This model's maximum context length is 8192 tokens."}, true},
		{&HTTPError{Status: "413", Body: "prompt_too_long"}, true},
		{&HTTPError{Status: "400", Body: "bad request: unknown model"}, false},
		{&HTTPError{Status: "401", Body: "unauthorized"}, false},
		{errors.New("context_length_exceeded"), true},
		{errors.New("rate limited"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsContextLimit(c.err); got != c.want {
			t.Errorf("IsContextLimit(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
