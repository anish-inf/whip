package tui

import (
	"fmt"
	"strings"
)

// /fork copies the conversation (whole, or up to a rewind-picker selection)
// into a NEW session with a chosen title and switches to it — "copy
// conversation with new name"; the original stays untouched and /resume-able
// (opencode's Session.fork, packages/opencode/src/session/session.ts:691).
// /rename retitles the current session. Both share one inline prompt: the
// input box is repurposed with a prefixed label, enter commits, esc cancels.

type namePrompt struct {
	label string // input prefix, e.g. "⑂ fork name:"
	draft string // input content stashed while the prompt owns the box
	mask  bool   // render the value as ••• (secret entry, e.g. /auth)
	onOK  func(string)
}

// openNamePrompt repurposes the input box as a one-shot text prompt. The
// in-progress draft is stashed and restored when the prompt closes.
func (m *model) openNamePrompt(label, value string, onOK func(string)) {
	m.namePrompt = &namePrompt{label: label, draft: m.input.Value(), onOK: onOK}
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.growInput()
}

// closeNamePrompt dismisses the prompt, restoring the stashed draft.
func (m *model) closeNamePrompt() {
	m.input.SetValue(m.namePrompt.draft)
	m.input.CursorEnd()
	m.namePrompt = nil
	m.growInput()
}

// maskedValue renders the input value for the prompt line: ••• when the
// prompt masks (auth keys never echo), the raw value otherwise.
func (p *namePrompt) maskedValue(v string) string {
	if !p.mask {
		return v
	}
	return strings.Repeat("•", len([]rune(v)))
}

// forkCommand implements /fork [name].
func (m *model) forkCommand(arg string) {
	if m.store == nil {
		m.append(errStyle.Render("no session store"))
		return
	}
	if arg != "" {
		m.fork(len(m.agent.Messages), arg)
		return
	}
	// bare: suggest "<title> (fork #N)" and let the user rename inline
	suggest := "session (fork #1)"
	if m.sessionID != "" {
		if meta, _, err := m.store.Load(m.sessionID); err == nil {
			if t, err := m.store.ForkTitle(meta.Title); err == nil {
				suggest = t
			}
		}
	}
	m.openForkPrompt(len(m.agent.Messages), false, suggest)
}

// openForkPrompt asks for a name, then forks at cut. picker notes when the
// prompt came from the rewind picker, for the confirmation line.
func (m *model) openForkPrompt(cut int, picker bool, suggest ...string) {
	name := ""
	if len(suggest) > 0 {
		name = suggest[0]
	}
	m.openNamePrompt("⑂ fork name:", name, func(title string) {
		m.fork(cut, title)
	})
	if picker {
		m.append(dimStyle.Render("⑂ forking from the selected message — name the copy (enter) or esc"))
	}
}

// fork copies the history through conversation index cut (inclusive) into a
// new session and switches to it.
func (m *model) fork(cut int, title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		m.append(errStyle.Render("fork needs a name"))
		return
	}
	if len(m.agent.Messages)+len(m.future) <= 1 {
		m.append(dimStyle.Render("(nothing to fork yet)"))
		return
	}
	// picker cuts may point into the redo stack (beyond the live messages):
	// the clipped tail up to the cut comes along. Rewind to just after the
	// entry first so persist() writes those rows before the copy.
	if len(m.future) > 0 {
		if cut+1 <= len(m.agent.Messages) {
			m.future = nil
		} else {
			m.applyRewind(cut + 1)
		}
	}
	m.persist() // every row must exist in the DB before the copy
	if m.sessionID == "" {
		return // persist failed; it already reported why
	}
	cut = min(max(cut, 0), len(m.agent.Messages)-1)
	oldID := m.sessionID
	oldTitle := oldID
	if meta, _, err := m.store.Load(oldID); err == nil && meta.Title != "" {
		oldTitle = meta.Title
	}
	newID, err := m.store.Fork(oldID, cut, title) // copies stored rows seq < cut
	if err != nil {
		m.append(errStyle.Render("fork failed: " + err.Error()))
		return
	}
	m.sessionID = newID
	m.agent.Tasks().SetSessionID(newID)
	m.agent.Messages = m.agent.Messages[:cut+1]
	m.future = nil
	m.saved = cut + 1
	m.rebuildTranscript()
	m.append(dimStyle.Render(fmt.Sprintf("⑂ forked %q → %q (%s) — the original is under /resume", oldTitle, title, newID)))
}

// renameCommand implements /rename [title].
func (m *model) renameCommand(arg string) {
	if m.store == nil {
		m.append(errStyle.Render("no session store"))
		return
	}
	if arg != "" {
		m.rename(arg)
		return
	}
	cur := ""
	if m.sessionID != "" {
		if meta, _, err := m.store.Load(m.sessionID); err == nil {
			cur = meta.Title
		}
	}
	m.openNamePrompt("✎ session name:", cur, m.rename)
}

func (m *model) rename(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		m.append(errStyle.Render("rename needs a title"))
		return
	}
	m.persist() // a session row must exist before it can be titled
	if m.sessionID == "" {
		return
	}
	if err := m.store.SetTitle(m.sessionID, title); err != nil {
		m.append(errStyle.Render("rename failed: " + err.Error()))
		return
	}
	m.append(dimStyle.Render("✎ session renamed: " + title))
}
