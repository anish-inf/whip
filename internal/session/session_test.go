package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/abe/loopy/internal/llm"
)

func TestStoreRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.Create("/tmp", "kimi-k3-fast", "inference")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first question here"},
		{Role: "assistant", Content: "the answer"},
		{Role: "user", Content: "follow-up"},
		{Role: "assistant", Content: "final\nanswer"},
	}
	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}

	meta, got, err := st.Load(id[:4]) // prefix resolution
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.Title != "first question here" {
		t.Fatalf("meta: %+v", meta)
	}
	if len(got) != 4 || got[0].Role != "user" || got[3].Content != "final\nanswer" {
		t.Fatalf("messages: %+v", got)
	}

	u, a := st.LastExchange(id)
	if u != "follow-up" || a != "final\nanswer" {
		t.Fatalf("last exchange: %q %q", u, a)
	}

	recent, err := st.Recent(10)
	if err != nil || len(recent) != 1 || recent[0].ID != id {
		t.Fatalf("recent: %v %v", recent, err)
	}

	if _, _, err := st.Load("zzzz"); err == nil {
		t.Fatal("expected not-found error")
	}

	// idempotent re-save must not duplicate
	if err := st.Save(id, 1, msgs, "kimi-k3-fast", "inference"); err != nil {
		t.Fatal(err)
	}
	if _, got, _ = st.Load(id); len(got) != 4 {
		t.Fatalf("re-save duplicated rows: %d", len(got))
	}
}

func TestStoreEdgeCases(t *testing.T) {
	if _, err := Open("/nonexistent-dir/x.db"); err == nil {
		t.Fatal("expected open error")
	}
	if truncate(strings.Repeat("a", 100), 10) != strings.Repeat("a", 9)+"…" {
		t.Fatal("truncate long")
	}

	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id1, _ := st.Create("/tmp", "m", "p")
	id2, _ := st.Create("/tmp", "m", "p")
	msgs := []llm.Message{{Role: "system"}, {Role: "user", Content: "q"}}
	st.Save(id1, 1, msgs, "m", "p")
	st.Save(id2, 1, msgs, "m", "p")
	if _, _, err := st.Load(""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous, got %v", err)
	}
	// LastExchange on a session with no assistant messages
	u, a := st.LastExchange(id1)
	if u != "q" || a != "" {
		t.Fatalf("last exchange: %q %q", u, a)
	}
	// corrupt message row surfaces a load error
	st.db.Exec(`UPDATE messages SET content='{bad' WHERE session_id=?`, id1)
	if _, _, err := st.Load(id1); err == nil {
		t.Fatal("expected corrupt-row error")
	}
}
