package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/llm"
	"github.com/context-labs/loopy/internal/session"
)

// rewindModel builds an idle model with an authored conversation and a real
// (temp-dir) session store. msgs excludes the system prompt; the session is
// created and fully persisted.
func rewindModel(t *testing.T, msgs ...llm.Message) *model {
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
	m.agent.Messages = append([]llm.Message{{Role: "system", Content: "sys"}}, msgs...)
	m.vp.SetContent("x")
	// persisted as if turns had run
	if err := st.Save(m.sessionIDC(t), 1, m.agent.Messages, "m", "p"); err != nil {
		t.Fatal(err)
	}
	m.saved = len(m.agent.Messages)
	m.rebuildTranscript()
	return m
}

func (m *model) sessionIDC(t *testing.T) string {
	t.Helper()
	id, err := m.store.Create("/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	m.sessionID = id
	return id
}

func esc(m *model) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }

func TestDoubleEscOpensRewind(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
	)
	press(t, m, esc(m)) // first: arms
	if m.rew != nil {
		t.Fatal("single esc must not open the picker")
	}
	if !m.esc1 {
		t.Fatal("first idle esc should arm")
	}
	press(t, m, esc(m)) // second: opens
	if m.rew == nil {
		t.Fatal("double esc should open the rewind picker")
	}
	if len(m.rew.entries) != 1 || m.rew.entries[0].text != "q1" {
		t.Fatalf("entries: %+v", m.rew.entries)
	}
}

func TestBusyEscStillInterrupts(t *testing.T) {
	m := rewindModel(t, llm.Message{Role: "user", Content: "q1", Authored: true})
	m.busy = true
	called := false
	m.cancel = func() { called = true }
	press(t, m, esc(m))
	if !called || m.rew != nil {
		t.Fatal("busy esc must interrupt, never open rewind")
	}
}

// Regression: esc with a draft typed while the agent is running must clear
// the DRAFT (double-esc, into input history), not interrupt the agent.
func TestBusyEscWithDraftClearsInputNotAgent(t *testing.T) {
	m := rewindModel(t, llm.Message{Role: "user", Content: "q1", Authored: true})
	m.busy = true
	called := false
	m.cancel = func() { called = true }
	m.input.SetValue("half-written follow-up")

	press(t, m, esc(m)) // first: arms the clear, warning shows
	if called {
		t.Fatal("esc with a draft must not interrupt the agent")
	}
	if !m.escClr {
		t.Fatal("first esc with a draft should arm the clear")
	}
	if m.input.Value() == "" {
		t.Fatal("first esc must not clear the draft yet")
	}

	press(t, m, esc(m)) // second: clears the draft into input history
	if called {
		t.Fatal("double-esc with a draft must never interrupt the agent")
	}
	if m.input.Value() != "" {
		t.Fatalf("double-esc should clear the draft, got %q", m.input.Value())
	}
	if got := m.hist[len(m.hist)-1]; got != "half-written follow-up" {
		t.Fatalf("cleared draft should land in input history, got %q", got)
	}
	if m.histIdx != len(m.hist) {
		t.Fatalf("histIdx should sit at the newest edge, got %d of %d", m.histIdx, len(m.hist))
	}
	if m.rew != nil {
		t.Fatal("clearing a draft must not open the rewind picker")
	}
	// chat history untouched: agent messages are exactly what we seeded
	if len(m.agent.Messages) != 2 { // system + q1
		t.Fatalf("chat history must be untouched, got %d messages", len(m.agent.Messages))
	}
}

// The cleared draft is recallable with ↑, in case the clear was an accident.
func TestClearedDraftRecallsWithUp(t *testing.T) {
	m := rewindModel(t, llm.Message{Role: "user", Content: "q1", Authored: true})
	m.input.SetValue("oops i cleared it")
	press(t, m, esc(m))
	press(t, m, esc(m))
	if m.input.Value() != "" {
		t.Fatal("double-esc should clear the draft")
	}
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyUp})
	m = tm.(*model)
	if m.input.Value() != "oops i cleared it" {
		t.Fatalf("↑ should recall the cleared draft, got %q", m.input.Value())
	}
}

// A single esc with a draft, then a pause past the arming window, leaves the
// draft intact and requires a fresh double-esc.
func TestSingleEscKeepsDraft(t *testing.T) {
	m := rewindModel(t, llm.Message{Role: "user", Content: "q1", Authored: true})
	m.input.SetValue("still thinking")
	press(t, m, esc(m))
	if !m.escClr {
		t.Fatal("first esc should arm the clear")
	}
	// the arming window closes on escArmMsg (the tea.Tick firing)
	tm, _ := m.Update(escArmMsg{})
	m = tm.(*model)
	if m.escClr {
		t.Fatal("arming window should have closed")
	}
	if m.input.Value() != "still thinking" {
		t.Fatalf("draft must survive a lone esc, got %q", m.input.Value())
	}
}

// Idle with NO draft: double-esc still opens the rewind picker (unchanged).
func TestDoubleEscWithoutDraftStillRewinds(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
	)
	press(t, m, esc(m))
	if m.escClr {
		t.Fatal("no draft: the draft-clear arm must stay off")
	}
	if !m.esc1 {
		t.Fatal("first idle esc should arm the rewind")
	}
	press(t, m, esc(m))
	if m.rew == nil {
		t.Fatal("double esc with no draft should open the rewind picker")
	}
}

// Dismissing UI (e.g. an open completion menu) consumes the esc and must not
// arm either the clear or the rewind.
func TestEscDismissalDoesNotArm(t *testing.T) {
	m := rewindModel(t, llm.Message{Role: "user", Content: "q1", Authored: true})
	m.input.SetValue("/mo")
	m.menu = &menu{head: "/", cands: []cand{{Text: "/model"}}}
	press(t, m, esc(m))
	if m.menu != nil {
		t.Fatal("esc should dismiss the menu")
	}
	if m.escClr || m.esc1 {
		t.Fatal("a dismissal must not arm clear or rewind")
	}
	if m.input.Value() != "/mo" {
		t.Fatalf("dismissing the menu keeps the draft, got %q", m.input.Value())
	}
}

// Regression: the picker lists oldest at the TOP and the latest message at
// the BOTTOM, with the selection starting on the latest. ↑ moves up toward
// older, ↓ down toward newer. The bug was newest-first rendering plus a
// "distance from newest" selection index, which read reversed.
func TestRewindPickerOrderAndArrows(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true},
		llm.Message{Role: "assistant", Content: "a2"},
		llm.Message{Role: "user", Content: "q3", Authored: true},
	)
	press(t, m, esc(m))
	press(t, m, esc(m))

	// entries are chronological; the selection starts on the LATEST (bottom)
	if got := len(m.rew.entries); got != 3 {
		t.Fatalf("entries: %d", got)
	}
	if m.rew.sel != 2 || m.rew.entries[m.rew.sel].text != "q3" {
		t.Fatalf("selection should start on the latest q3: sel=%d", m.rew.sel)
	}

	// rendered top-to-bottom: q1, q2, q3 — q3 (the latest) last
	view := m.rewindView()
	i1, i2, i3 := strings.Index(view, "q1"), strings.Index(view, "q2"), strings.Index(view, "q3")
	if !(i1 >= 0 && i1 < i2 && i2 < i3) {
		t.Fatalf("list should read oldest→latest top→bottom (q1 q2 q3)\n%s", view)
	}
	// exactly one row carries the cursor marker, and it is q3's
	if strings.Count(view, "❯") != 1 {
		t.Fatalf("exactly one selected row should be marked\n%s", view)
	}
	// ↑ walks toward older (up the list): q3 → q2 → q1, then clamps
	for _, want := range []string{"q2", "q1", "q1"} {
		press(t, m, tea.KeyMsg{Type: tea.KeyUp})
		if got := m.rew.entries[m.rew.sel].text; got != want {
			t.Fatalf("↑ should move to %s, on %s", want, got)
		}
	}
	// ↓ walks back toward newer: q1 → q2 → q3, then clamps at the latest
	for _, want := range []string{"q2", "q3", "q3"} {
		press(t, m, tea.KeyMsg{Type: tea.KeyDown})
		if got := m.rew.entries[m.rew.sel].text; got != want {
			t.Fatalf("↓ should move to %s, on %s", want, got)
		}
	}
}

// Each entry renders its submission timestamp dimmed on the line below the
// preview. Messages predating SentAt show an em dash, never a wrong time.
func TestRewindPickerShowsTimestamps(t *testing.T) {
	t1 := time.Date(2025, 6, 1, 14, 30, 0, 0, time.Local)
	t2 := time.Date(2025, 6, 1, 15, 45, 0, 0, time.Local)
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true, SentAt: &t1},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true, SentAt: &t2},
		llm.Message{Role: "assistant", Content: "a2"},
		llm.Message{Role: "user", Content: "q3-old", Authored: true}, // no SentAt: legacy row
	)
	press(t, m, esc(m))
	press(t, m, esc(m))

	view := m.rewindView()
	for _, ts := range []string{"2025-06-01 14:30", "2025-06-01 15:45"} {
		if !strings.Contains(view, ts) {
			t.Errorf("picker should show timestamp %q\n%s", ts, view)
		}
	}
	// the legacy row (no SentAt) renders a dash, and each timestamp sits on
	// the line below its own preview
	lines := strings.Split(view, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, "q1") && !strings.Contains(lines[i+1], "14:30") {
			t.Errorf("q1's timestamp should sit directly below it\n%s", view)
		}
		if strings.Contains(ln, "q3-old") && !strings.Contains(lines[i+1], "—") {
			t.Errorf("q3-old (no SentAt) should show a dash below it\n%s", view)
		}
	}
}

func TestRewindTruncatesAndRestoresInput(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true},
		llm.Message{Role: "assistant", Content: "a2"},
	)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // sel starts on the latest (q2); ↑ moves to q1
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// rewound to just before q1: only the system prompt survives
	if len(m.agent.Messages) != 1 {
		t.Fatalf("messages after rewind: %+v", m.agent.Messages)
	}
	if m.input.Value() != "q1" {
		t.Fatalf("input should restore the rewound message, got %q", m.input.Value())
	}
	if len(m.future) != 4 { // q1..a2 clipped
		t.Fatalf("redo stack: %+v", m.future)
	}
	if m.saved != 1 {
		t.Fatalf("saved=%d", m.saved)
	}
	// DB rows at/after the cut are gone
	_, stored, err := m.store.Load(m.sessionID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("stored after rewind: %v %+v", err, stored)
	}
	// transcript rebuilt: no message blocks remain
	var texts []string
	for _, b := range m.blocks {
		texts = append(texts, b.text)
	}
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, "q1") || strings.Contains(joined, "q2") {
		t.Fatalf("blocks: %q", joined)
	}
}

func TestRewindForwardTravel(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true},
		llm.Message{Role: "assistant", Content: "a2"},
	)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // q2 → q1, rewind to just before it
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m.input.Reset()
	if len(m.agent.Messages) != 1 || len(m.future) != 4 {
		t.Fatalf("after rewind: msgs=%d future=%d", len(m.agent.Messages), len(m.future))
	}

	// reopen: both clipped user messages appear as dimmed future entries
	press(t, m, esc(m))
	press(t, m, esc(m))
	if len(m.rew.entries) != 2 || !m.rew.entries[0].future || !m.rew.entries[1].future {
		t.Fatalf("entries: %+v", m.rew.entries)
	}
	// sel 0 is q2 (the newest future entry): enter goes forward to just before it
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	// forward to just before q2: q1/a1 restored, q2/a2 still clipped
	if len(m.agent.Messages) != 3 || len(m.future) != 2 || m.future[0].Content != "q2" {
		t.Fatalf("forward: msgs=%d future=%+v", len(m.agent.Messages), m.future)
	}
	// the restored rows are persisted again
	if _, stored, _ := m.store.Load(m.sessionID); len(stored) != 2 {
		t.Fatalf("stored after forward: %+v", stored)
	}
	// forward travel does not clobber the input
	if m.input.Value() != "" {
		t.Fatalf("input: %q", m.input.Value())
	}
}

func TestRewindNeverCutsToolCallPairs(t *testing.T) {
	// entries sit at user messages; tool results always travel with their
	// assistant message, so no cut can orphan a tool result from its call
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1"}}},
		llm.Message{Role: "tool", ToolCallID: "c1", Content: "out"},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true},
	)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // q1
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.agent.Messages) != 1 { // only the system prompt survives
		t.Fatalf("messages: %+v", m.agent.Messages)
	}
	if _, stored, _ := m.store.Load(m.sessionID); len(stored) != 0 {
		t.Fatalf("stored: %+v", stored)
	}
}

func TestRewindCancelLeavesConversation(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
	)
	before := len(m.agent.Messages)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	press(t, m, esc(m)) // cancel
	if m.rew != nil || len(m.agent.Messages) != before || len(m.future) != 0 {
		t.Fatal("cancel must not touch the conversation")
	}
}

func TestPartialRewindKeepsPrefixInDB(t *testing.T) {
	// rewind to a middle cut: the retained prefix must survive in the DB
	// exactly (seq == conversation index; a cut at 3 keeps seq 1 and 2)
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true},
		llm.Message{Role: "assistant", Content: "a2"},
		llm.Message{Role: "user", Content: "q3", Authored: true},
	)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp}) // sel starts on q3 (latest); ↑ moves to q2
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.agent.Messages) != 3 { // sys, q1, a1
		t.Fatalf("messages: %+v", m.agent.Messages)
	}
	_, stored, err := m.store.Load(m.sessionID)
	if err != nil || len(stored) != 2 || stored[0].Content != "q1" || stored[1].Content != "a1" {
		t.Fatalf("stored prefix: %v %+v", err, stored)
	}
}

func TestEscArmDoesNotLeakAcrossModalDismiss(t *testing.T) {
	m := forkModel(t)
	press(t, m, esc(m))  // arm
	m.command("/rename") // opens the name prompt
	press(t, m, esc(m))  // dismisses the prompt — must not count toward rewind
	if m.esc1 {
		t.Fatal("modal dismissal must clear the esc arm")
	}
	press(t, m, esc(m)) // one more: arms again, picker must NOT open
	if m.rew != nil {
		t.Fatal("picker opened from a stale arm")
	}
}

func TestNamePromptPreservesDraft(t *testing.T) {
	m := forkModel(t)
	m.input.SetValue("my half-typed thought")
	m.command("/rename") // prompt takes over the input box
	if m.input.Value() == "my half-typed thought" {
		t.Fatal("prompt should replace the input")
	}
	press(t, m, esc(m)) // cancel: the draft comes back
	if m.input.Value() != "my half-typed thought" {
		t.Fatalf("draft lost: %q", m.input.Value())
	}
}

func TestResumeAfterRewindMatches(t *testing.T) {
	m := rewindModel(t,
		llm.Message{Role: "user", Content: "q1", Authored: true},
		llm.Message{Role: "assistant", Content: "a1"},
		llm.Message{Role: "user", Content: "q2", Authored: true},
	)
	press(t, m, esc(m))
	press(t, m, esc(m))
	press(t, m, tea.KeyMsg{Type: tea.KeyUp})
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // rewind to before q1

	// a fresh load of the session sees exactly the rewound history (nothing)
	_, stored, err := m.store.Load(m.sessionID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("resumed history: %v %+v", err, stored)
	}
}
