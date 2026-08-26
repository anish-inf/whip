package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/llm"
)

// runFixture writes a config pointing the default model at an SSE test
// server that replies with reply (and records each request into reqs).
func runFixture(t *testing.T, reply string, reqs *[]llm.Request) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.Request
		json.NewDecoder(r.Body).Decode(&req)
		if reqs != nil {
			*reqs = append(*reqs, req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := json.Marshal(reply)
		fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}]}`+"\n\n", body)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("WHIP_HOME", home)
	cfg := fmt.Sprintf(`{
		"defaultModel": "test",
		"providers": {"testprov": {"baseUrl": %q, "api": "openai-completions", "apiKey": "k"}},
		"models": {"test": {"providers": ["testprov"], "maxOut": 100}}
	}`, srv.URL)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runCapture swaps stdout/stdin for the duration of runCLI and returns what
// the run printed on stdout. stdinData is piped in ("" still leaves a
// non-TTY empty stdin, like `whip run "…" < /dev/null`).
func runCapture(t *testing.T, stdinData string, args ...string) (string, error) {
	t.Helper()

	oldIn := os.Stdin
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inW.WriteString(stdinData); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	os.Stdin = inR
	defer func() { os.Stdin = oldIn; inR.Close() }()

	oldOut := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outW
	defer func() { os.Stdout = oldOut }()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&buf, outR); close(done) }()

	runErr := runCLI(args)

	outW.Close()
	<-done
	outR.Close()
	return buf.String(), runErr
}

// text mode streams the assistant reply to stdout.
func TestRunTextOutput(t *testing.T) {
	runFixture(t, "hello world", nil)

	out, err := runCapture(t, "", "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("stdout should stream the reply, got %q", out)
	}
}

// --format json emits newline-delimited events: a text event per delta and a
// final done event carrying the full reply.
func TestRunJSONStream(t *testing.T) {
	runFixture(t, "all done", nil)

	out, err := runCapture(t, "", "--format", "json", "go")
	if err != nil {
		t.Fatal(err)
	}
	var sawText, sawDone bool
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev map[string]string
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line not JSON: %q: %v", line, err)
		}
		switch ev["type"] {
		case "text":
			sawText = true
		case "done":
			sawDone = true
			if ev["text"] != "all done" {
				t.Fatalf("done text: %q", ev["text"])
			}
		}
	}
	if !sawText || !sawDone {
		t.Fatalf("want a text event and a done event, got:\n%s", out)
	}
}

// Piped stdin is appended to the prompt argument in the user message.
func TestRunStdinAppendsToPrompt(t *testing.T) {
	var reqs []llm.Request
	runFixture(t, "ok", &reqs)

	if _, err := runCapture(t, "piped context\n", "summarize this"); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("requests: %d", len(reqs))
	}
	var user string
	for _, m := range reqs[0].Messages {
		if m.Role == "user" {
			user = m.Content
		}
	}
	if !strings.Contains(user, "summarize this") || !strings.Contains(user, "piped context") {
		t.Fatalf("user message should combine the arg prompt and stdin, got %q", user)
	}
}
