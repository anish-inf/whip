package other

import tea "github.com/charmbracelet/bubbletea"

// notTUI is outside internal/tui — Send here is not the analyzer's concern.
func notTUI(p *tea.Program, m tea.Msg) {
	p.Send(m) // no diagnostic: not internal/tui
}
