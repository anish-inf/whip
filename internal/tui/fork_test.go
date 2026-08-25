package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/session"
)

func forkModel(t *testing.T) *model {
	t.Helper()
	st, err := session.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &model{
		input:    newInput(),
		agent:    &agent.Agent{},
		store:    st,
		queueSel: -1,
	}
	m.width = 80
	m.input.SetWidth(m.width - 2)
	m.agent.Messages = []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1", Authored: true},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2", Authored: true},
		{Role: "assistant", Content: "a2"},
	}
	id, err := st.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id
	m.saved = len(m.agent.Messages)
	if err := st.Save(id, 1, m.agent.Messages, "m", "p"); err != nil {
		t.Fatal(err)
	}
	m.rebuildTranscript()
	return m
}

func tailBlock(m *model) string { return m.blocks[len(m.blocks)-1].text }

func TestForkWithArg(t *testing.T) {
	m := forkModel(t)
	m.command("/fork experiment")

	if m.sessionID == "" || strings.Contains(tailBlock(m), "fork failed") {
		t.Fatalf("fork failed: %q", tailBlock(m))
	}
	meta, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "experiment" || len(msgs) != 4 { // the full conversation
		t.Fatalf("fork: %+v (%d msgs)", meta, len(msgs))
	}
	if !strings.Contains(tailBlock(m), "⑂ forked") {
		t.Fatalf("confirmation: %q", tailBlock(m))
	}
	// the original session survives untouched under /resume
	recent, err := m.store.Recent(10)
	if err != nil || len(recent) != 2 {
		t.Fatalf("recent: %v %+v", err, recent)
	}
}

func TestForkBareOpensNamePrompt(t *testing.T) {
	m := forkModel(t)
	m.command("/fork")

	if m.namePrompt == nil || m.input.Value() != "q1 (fork #1)" {
		t.Fatalf("prompt: %+v input=%q", m.namePrompt, m.input.Value())
	}
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // accept the suggestion
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil || meta.Title != "q1 (fork #1)" {
		t.Fatalf("fork title: %+v %v", meta, err)
	}
	// a second bare fork unwraps the suffix and suggests #2 off the base
	m.command("/fork")
	if m.input.Value() != "q1 (fork #2)" {
		t.Fatalf("suggestion: %q", m.input.Value())
	}
	// esc cancels without forking
	press(t, m, esc(m))
	if m.namePrompt != nil {
		t.Fatal("esc should cancel the prompt")
	}
	if recent, _ := m.store.Recent(10); len(recent) != 2 {
		t.Fatalf("cancelled fork created a session: %+v", recent)
	}
}

func TestForkFromRewindPicker(t *testing.T) {
	m := forkModel(t)
	press(t, m, esc(m))
	press(t, m, esc(m)) // open rewind picker, selection on "q2"
	press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})

	if m.rew != nil || m.namePrompt == nil {
		t.Fatalf("f should swap picker for name prompt: rew=%v np=%v", m.rew, m.namePrompt)
	}
	m.input.SetValue("at-q2")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	meta, msgs, err := m.store.Load(m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "at-q2" {
		t.Fatalf("title: %+v", meta)
	}
	// forked at q2: the copy keeps the selected message (q1, a1, q2);
	// rewinding instead would drop q2
	if len(msgs) != 3 || msgs[2].Content != "q2" {
		t.Fatalf("prefix: %+v", msgs)
	}
	if len(m.agent.Messages) != 4 {
		t.Fatalf("live messages: %+v", m.agent.Messages)
	}
}

func TestForkWhileRewoundIntoFuture(t *testing.T) {
	// rewind to before q1, then fork from the picker at the future q2 entry:
	// the clipped tail up to the cut comes along
	m := forkModel(t)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // select q1
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m.input.Reset()
	if len(m.agent.Messages) != 1 || len(m.future) != 4 {
		t.Fatalf("rewound: msgs=%d future=%d", len(m.agent.Messages), len(m.future))
	}

	press(t, m, esc(m))
	press(t, m, esc(m)) // reopen: both entries are future
	if len(m.rew.entries) != 2 {
		t.Fatalf("entries: %+v", m.rew.entries)
	}
	// sel 0 = newest = q2 (cut = 1+2 = 3); fork through it
	press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m.input.SetValue("redo-fork")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.agent.Messages) != 4 || m.agent.Messages[3].Content != "q2" {
		t.Fatalf("fork through future: %+v", m.agent.Messages)
	}
	_, msgs, err := m.store.Load(m.sessionID)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("stored: %v %+v", err, msgs)
	}
	// fork consumes the redo stack — the new session starts at the picked
	// point with no forward tail
	if len(m.future) != 0 {
		t.Fatalf("remaining future: %+v", m.future)
	}
}

func TestRename(t *testing.T) {
	m := forkModel(t)
	m.command("/rename better-name")
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil || meta.Title != "better-name" {
		t.Fatalf("rename: %+v %v", meta, err)
	}

	// bare rename prompts prefilled with the current title
	m.command("/rename")
	if m.namePrompt == nil || m.input.Value() != "better-name" {
		t.Fatalf("prompt: %q", m.input.Value())
	}
	m.input.SetValue("final-name")
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	meta, _, _ = m.store.Load(m.sessionID)
	if meta.Title != "final-name" {
		t.Fatalf("prompt rename: %+v", meta)
	}
}
