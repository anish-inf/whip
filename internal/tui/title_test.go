package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/session"
)

// After the first exchange, maybeTitle names the session via the cheap
// model; a user-set title (or an already-titled session) is left alone.
func TestAutoTitle(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	m := compactCmdModel() // canned server replies "sim" — a valid short title
	st, _ := session.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)
	m.agent.Messages = append(m.agent.Messages,
		llm.Message{Role: "user", Content: "how do I unstage a file", Authored: true},
		llm.Message{Role: "assistant", Content: "git restore --staged <file>"},
	)
	// the stored title is the auto placeholder (first user message, truncated)
	m.persist()
	m.maybeTitle()
	if !m.titled {
		t.Fatal("maybeTitle should mark the attempt")
	}
	// let the goroutine land
	time.Sleep(50 * time.Millisecond)
	// the titleMsg handler fills the placeholder with the model's title
	m.Update(titleMsg{"unstage a file"})
	meta, _, _ := st.Load(m.sessionID)
	if meta.Title != "unstage a file" {
		t.Fatalf("title should be the model's, got %q", meta.Title)
	}

	// a second attempt doesn't fire (titled latched)
	m.maybeTitle()
}

// A user-renamed session keeps its title — the auto-titler only fills the
// placeholder.
func TestAutoTitleRespectsRename(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	m := compactCmdModel()
	st, _ := session.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	m.store = st
	m.sessionID, _ = st.Create("/tmp", m.modelName, m.provName)
	m.agent.Messages = append(m.agent.Messages,
		llm.Message{Role: "user", Content: "original question", Authored: true},
	)
	m.persist()
	m.store.SetTitle(m.sessionID, "my title") // user renamed it
	m.Update(titleMsg{"model title"})
	meta, _, _ := st.Load(m.sessionID)
	if meta.Title != "my title" {
		t.Fatalf("a renamed session must keep its title, got %q", meta.Title)
	}
}
