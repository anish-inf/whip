package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A Client with CacheKey stamps prompt_cache_key on every request; without
// one the field is omitted entirely (providers that don't know it must not
// see it).
func TestPromptCacheKeyStampedFromClient(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	c.SetCacheKey("session-abc")
	if _, _, err := c.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"prompt_cache_key":"session-abc"`) {
		t.Fatalf("prompt_cache_key missing from request: %s", body)
	}

	// No CacheKey → no field on the wire.
	c2 := New(srv.URL, "k")
	if _, _, err := c2.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "prompt_cache_key") {
		t.Fatalf("prompt_cache_key should be omitted when unset: %s", body)
	}
}

// The prefix-cache contract: two consecutive requests in a session must share
// a byte-identical prefix up through the last message of the earlier request.
// This guards regressions that silently break provider prefix caching (e.g.
// someone adding a per-turn timestamp to the system prompt).
func TestConsecutiveRequestsSharePrefix(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	c.SetCacheKey("sess")
	msgs := []Message{
		{Role: "system", Content: "You are whip."},
		{Role: "user", Content: "first"},
	}
	// Turn 1.
	resp, _, err := c.Stream(context.Background(), Request{Model: "m", Messages: msgs}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Turn 2 appends assistant + user; the system+first-user prefix is frozen.
	msgs = append(msgs, Message{Role: "assistant", Content: resp.Content})
	msgs = append(msgs, Message{Role: "user", Content: "second"})
	if _, _, err := c.Stream(context.Background(), Request{Model: "m", Messages: msgs}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	if len(bodies) != 2 {
		t.Fatalf("captured %d requests, want 2", len(bodies))
	}
	b1, b2 := string(bodies[0]), string(bodies[1])
	// Turn 1's messages array is a strict prefix of turn 2's: extract the
	// messages payload (between `"messages":[` and the closing `]`) and assert
	// turn 2's payload begins with turn 1's, minus the trailing bracket.
	open := `"messages":[`
	i1 := strings.Index(b1, open) + len(open)
	j1 := strings.LastIndex(b1, `]`)
	i2 := strings.Index(b2, open) + len(open)
	j2 := strings.LastIndex(b2, `]`)
	if i1 < len(open) || j1 <= i1 || i2 < len(open) || j2 <= i2 {
		t.Fatalf("could not locate messages payload.\nturn1: %s\nturn2: %s", b1, b2)
	}
	m1, m2 := b1[i1:j1], b2[i2:j2]
	if !strings.HasPrefix(m2, m1) {
		t.Fatalf("turn-2 messages do not begin with turn-1's byte-identically.\nturn1 msgs: %s\nturn2 msgs: %s", m1, m2)
	}
	// Both carry the same cache key.
	if !strings.Contains(b1, `"prompt_cache_key":"sess"`) || !strings.Contains(b2, `"prompt_cache_key":"sess"`) {
		t.Fatalf("cache key must be stable across turns:\n%s\n%s", b1, b2)
	}
}

// The client's cache key is set on one goroutine (the TUI's session swap)
// while another goroutine is mid-request reading it. Run under -race: the
// atomic.Pointer must make this safe.
func TestCacheKeyConcurrentSetAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New(srv.URL, "k")
	done := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
				c.SetCacheKey("sess-" + strconv.Itoa(i%3))
			}
		}
	}()
	for range 50 {
		if _, _, err := c.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
}
