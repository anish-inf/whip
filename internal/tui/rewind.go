package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/llm"
)

// Rewind: double-esc while idle opens a picker over the conversation's
// authored user messages. Browsing live-scrolls the transcript (opencode's
// dialog-timeline onMove). enter rewinds the conversation to just before the
// selected message — Agent.Messages and the DB are truncated, the transcript
// is rebuilt, and the message text lands back in the input for editing
// (opencode's undo: "the input restore is what makes it feel good"). The
// clipped tail is kept in memory as a redo stack: reopening the picker while
// rewound lists the clipped messages dimmed below the live ones, and enter on
// one moves forward again. Submitting anything new discards the future.
// Fork from any entry with f.

// rewindEntry is one row of the rewind picker. cut is the conversation index
// the entry points at: for a live message it is its index in agent.Messages,
// for a clipped "future" message it is its original conversation index
// (base + position in the redo stack, where base = len(agent.Messages)).
// enter rewinds to just before cut; f forks the history through cut.
type rewindEntry struct {
	cut    int
	text   string     // single-line preview
	when   *time.Time // when the message was submitted (nil = unknown)
	future bool       // clipped by the active rewind; selecting moves forward
}

type rewindState struct {
	entries []rewindEntry // chronological: oldest first, latest LAST
	sel     int           // direct index into entries; starts at the latest
	savedVP int           // viewport offset on open, restored on cancel
}

// rewindBase is where the conversation was cut. future is kept ordered by
// original conversation index (oldest first), so the boundary is simply
// len(agent.Messages); future[i] holds original index base+i.

// Cuts never split a tool_call from its results: entries sit at user
// messages and an assistant message's tool calls/results always follow it,
// so moving the cut to "before the user message" is boundary-safe.
type escArmMsg struct{} // disarms the idle double-esc window

func (m *model) rewindEntries() []rewindEntry {
	var out []rewindEntry
	for i, msg := range m.agent.Messages {
		if msg.Role == "user" && msg.Authored {
			out = append(out, rewindEntry{cut: i, text: oneLine(msg.TextContent()), when: msg.SentAt})
		}
	}
	for i, msg := range m.future {
		if msg.Role == "user" && msg.Authored {
			out = append(out, rewindEntry{
				cut: len(m.agent.Messages) + i, text: oneLine(msg.TextContent()), when: msg.SentAt, future: true,
			})
		}
	}
	return out
}

func oneLine(s string) string { return truncLine(strings.Join(strings.Fields(s), " "), 100) }

// firstLine is the collapsed tool row's text: the first non-empty line,
// whitespace-collapsed.
func firstLine(s string) string {
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			return truncLine(strings.Join(strings.Fields(l), " "), 120)
		}
	}
	return "(no output)"
}

// toolVerb is the present-participle a running row leads with ("Reading
// file…"-style); the tool name verbatim when there's no nicer verb.
func toolVerb(name string) string {
	switch name {
	case "read":
		return "Reading"
	case "write":
		return "Writing"
	case "edit":
		return "Editing"
	case "bash":
		return "Running"
	case "task":
		return "Delegating"
	case "remember", "forget":
		return "Remembering"
	case "todowrite":
		return "Planning"
	default:
		return name
	}
}

// scrollToMsg live-scrolls the viewport so the block rendering
// agent.Messages[msgIdx] is near the top.
func (m *model) scrollToMsg(msgIdx int) {
	if msgIdx < 0 || msgIdx >= len(m.msgBlock) {
		return
	}
	bi := m.msgBlock[msgIdx]
	if bi < 0 || bi >= len(m.blocks) {
		return
	}
	m.follow = false
	m.vp.SetYOffset(max(m.blocks[bi].y0-1, 0))
}

func (m *model) openRewind() {
	entries := m.rewindEntries()
	if len(entries) == 0 {
		m.append(dimStyle.Render("(nothing to rewind to yet)"))
		return
	}
	m.rew = &rewindState{entries: entries, sel: len(entries) - 1, savedVP: m.vp.YOffset}
	m.scrollToMsg(entries[len(entries)-1].cut) // selection starts on the latest
}

func (m *model) rewindKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	r := m.rew
	sel := func() rewindEntry { return r.entries[r.sel] }
	switch msg.Type {
	case tea.KeyEsc:
		m.vp.SetYOffset(r.savedVP) // put the scroll back where the user had it
		m.rew = nil
	case tea.KeyUp: // up the list = toward the oldest (top)
		r.sel = max(r.sel-1, 0)
		m.scrollToMsg(sel().cut)
	case tea.KeyDown: // down the list = toward the latest (bottom)
		r.sel = min(r.sel+1, len(r.entries)-1)
		m.scrollToMsg(sel().cut)
	case tea.KeyEnter:
		e := sel()
		text := m.applyRewind(e.cut)
		m.rew = nil
		if !e.future {
			m.input.SetValue(text) // restore the rewound message for editing
			m.input.CursorEnd()
			m.growInput()
		}
	case tea.KeyRunes:
		if string(msg.Runes) == "f" {
			e := sel()
			m.rew = nil
			m.openForkPrompt(e.cut, true) // the copy keeps the selected message
			return m, nil
		}
	}
	return m, nil
}

// applyRewind moves the conversation boundary to cut (an index into
// agent.Messages, clamped to the system prompt). Anything beyond it becomes
// the redo stack; the DB and transcript follow. Returns the authored user
// text at the cut, if any, for restoring into the input.
func (m *model) applyRewind(cut int) string {
	cut = max(cut, 1) // keep the system prompt
	base := len(m.agent.Messages)
	restored, restoreErr := 0, error(nil)
	switch {
	case cut > base: // forward: pull clipped messages back in
		m.agent.Messages = append(m.agent.Messages, m.future[:cut-base]...)
		m.future = append([]llm.Message(nil), m.future[cut-base:]...)
	case cut < base: // back: clip the tail into the redo stack (oldest first)
		clipped := append([]llm.Message(nil), m.agent.Messages[cut:]...)
		m.future = append(clipped, m.future...)
		m.agent.Messages = m.agent.Messages[:cut]
		m.saved = min(m.saved, cut)
		if m.store != nil && m.sessionID != "" {
			if err := m.store.DeleteFrom(m.sessionID, cut); err != nil {
				m.append(errStyle.Render("session save failed: " + err.Error()))
			}
		}
		// restore the workspace to the earliest snapshot being rewound past
		// (the state before the oldest clipped turn ran). Consumed snapshots
		// are dropped from map and DB (DeleteFrom trimmed the rows above) so
		// a later rewind doesn't re-apply them.
		best, bestIdx := "", -1
		for idx, ref := range m.snapshots {
			if idx >= cut && (bestIdx == -1 || idx < bestIdx) {
				best, bestIdx = ref, idx
			}
		}
		if best != "" {
			restored, restoreErr = restoreWorkspace(best)
			for idx := range m.snapshots {
				if idx >= cut {
					delete(m.snapshots, idx)
				}
			}
		}
	}
	m.persist() // re-save any rows pulled back in; no-op otherwise
	m.rebuildTranscript()
	// the workspace note lands AFTER the rebuild — rebuildTranscript resets
	// the block list, so anything appended before it is wiped
	switch {
	case restoreErr != nil:
		m.append(errStyle.Render("workspace rewind failed: " + restoreErr.Error()))
	case restored > 0:
		m.append(dimStyle.Render(fmt.Sprintf("⟲ workspace rewound — %d file(s) restored", restored)))
	}
	text := ""
	if cut < len(m.agent.Messages)+len(m.future) {
		if msg := m.messageAt(cut); msg.Role == "user" && msg.Authored {
			text = msg.TextContent()
		}
	}
	return text
}

// messageAt reads conversation index i across the live/redo boundary.
func (m *model) messageAt(i int) llm.Message {
	if i < len(m.agent.Messages) {
		return m.agent.Messages[i]
	}
	return m.future[i-len(m.agent.Messages)]
}

// rebuildTranscript resets the block list from agent.Messages (rewind moves
// the boundary, so blocks beyond the cut must go).
func (m *model) rebuildTranscript() {
	m.blocks = nil
	m.msgBlock = nil
	m.seedTranscript(m.agent.Messages[1:], 1) // skip the system prompt
}

// rewindView renders the picker strip above the input: oldest at the top,
// latest at the bottom, so ↑ moves toward older and ↓ toward newer. Each entry
// takes two rows — the preview line, then a dimmed timestamp beneath it.
func (m *model) rewindView() string {
	r := m.rew
	const maxRows = 8 // entry rows; each renders as 2 lines
	// window over entries; sel starts at the latest (bottom) so anchor there
	start := max(0, min(r.sel-maxRows/2, len(r.entries)-maxRows))
	end := min(start+maxRows, len(r.entries))
	var b strings.Builder
	b.WriteString(dimStyle.Render("⏪ rewind — enter: rewind here · f: fork from here · esc: cancel"))
	for row := start; row < end; row++ {
		e := r.entries[row]
		b.WriteString("\n")
		if row == r.sel {
			b.WriteString(youStyle.Render("❯ " + e.text))
		} else if e.future {
			b.WriteString(dimStyle.Render("  " + e.text + " (rewound)"))
		} else {
			b.WriteString("  " + e.text)
		}
		b.WriteString("\n    " + dimStyle.Render(rewindWhen(e.when)))
	}
	fmt.Fprintf(&b, "\n%s", dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑ older · ↓ newer", r.sel+1, len(r.entries))))
	return b.String()
}

// rewindWhen renders an entry's submission time for the picker. Pre-SentAt
// sessions have no per-message timestamp; show an em dash rather than a wrong
// or blank line.
func rewindWhen(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04") + " · " + ago(*t)
}

// discardFuture drops the redo stack: any new activity while rewound makes
// the clipped tail unreachable (branch-point semantics).
func (m *model) discardFuture() { m.future = nil }
