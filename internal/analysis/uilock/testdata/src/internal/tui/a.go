package tui

import tea "github.com/charmbracelet/bubbletea"

// syncSend calls Send synchronously — the deadlock hazard the analyzer flags.
func syncSend(p *tea.Program, m tea.Msg) {
	p.Send(m) // want `synchronous .{0,2}tea.Program..Send can deadlock`
}

// detachedSend wraps Send in a go statement — safe.
func detachedSend(p *tea.Program, m tea.Msg) {
	go p.Send(m)
}

// closureSend spawns a goroutine whose body sends — safe.
func closureSend(p *tea.Program, m tea.Msg) {
	go func() { p.Send(m) }()
}

// whitelistedSend runs on a background goroutine and says so.
func whitelistedSend(p *tea.Program, m tea.Msg) {
	p.Send(m) //nolint:uilock // background: the permission-gate tool goroutine
}
