package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/session"
)

// whip sessions lists stored sessions newest-first with id, title, model, age.
func TestSessionsCLI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WHIP_HOME", dir)

	st, _ := session.Open(filepath.Join(dir, "sessions.db"))
	id, _ := st.Create("/tmp", "kimi-k3-fast", "inference")
	st.Save(id, 0, []llm.Message{
		{Role: "user", Content: "how do I unstage a file", Authored: true},
		{Role: "assistant", Content: "git restore --staged"},
	}, "kimi-k3-fast", "inference")
	st.Close()

	out := captureStdout(t, func() {
		if err := sessionsCLI(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "how do I unstage a file") || !strings.Contains(out, "kimi-k3-fast") {
		t.Fatalf("sessions should list id/title/model, got:\n%s", out)
	}
	if !strings.Contains(out, "just now") && !strings.Contains(out, time.Now().Format("2006-01-02")) {
		t.Fatalf("age column should render, got:\n%s", out)
	}
}
