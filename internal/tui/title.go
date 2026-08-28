package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/llm"
)

// Auto session titles: after the first exchange, a cheap background call
// names the session so the /resume picker is scannable. Uses the compaction
// model (the cheap/fast one) when set, else the session's own. Never
// overwrites a title the user set with /rename. Failures are silent — a
// title is a nicety, not worth an error line.
type titleMsg struct{ title string }

// maybeTitle fires once per session, when the first turn completes with the
// title still at its auto-derived placeholder (the raw first user message).
func (m *model) maybeTitle() tea.Cmd {
	if m.store == nil || m.sessionID == "" || m.titled {
		return nil
	}
	meta, _, err := m.store.Load(m.sessionID)
	if err != nil || meta.Title == "" {
		return nil
	}
	m.titled = true // one attempt per session, win or lose
	cli, mdl := m.agent.CompactClient, m.agent.CompactModel
	if cli == nil {
		cli = m.agent.Client
	}
	if cli == nil {
		return nil // headless tests build agents without a client
	}
	if mdl == "" {
		mdl = m.agent.Model
	}
	var userTxt, asstTxt string
	for _, msg := range m.agent.Messages {
		if userTxt == "" && msg.Role == "user" {
			userTxt = msg.TextContent()
		} else if msg.Role == "assistant" {
			asstTxt = msg.TextContent()
		}
		if userTxt != "" && asstTxt != "" {
			break
		}
	}
	if userTxt == "" {
		return nil
	}
	p := m.prog
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, _, err := cli.Complete(ctx, llm.Request{
			Model:     mdl,
			MaxTokens: 24,
			Messages: []llm.Message{
				{Role: "system", Content: "You name chat sessions. Reply with a short title (3-6 words, plain text, no quotes, no trailing period) summarizing the user's request."},
				{Role: "user", Content: "Request: " + truncLine(userTxt, 300) + "\nResponse: " + truncLine(asstTxt, 200)},
			},
		})
		if err != nil {
			return
		}
		title := strings.Trim(strings.TrimSpace(out), "\"'.")
		if title == "" || len(title) > 80 {
			return
		}
		if p != nil {
			p.Send(titleMsg{title})
		}
	}()
	return nil
}
