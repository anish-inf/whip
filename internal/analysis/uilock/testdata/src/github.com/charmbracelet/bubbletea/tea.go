// Package bubbletea is a minimal analysistest stub: just the Program type and
// Send method the analyzer matches on, and the Msg alias.
package bubbletea

// Msg is any message.
type Msg interface{}

// Program is the TUI program.
type Program struct{}

// Send delivers a message to the event loop (blocks on its channel).
func (p *Program) Send(m Msg) {}
