package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/mcp"
)

// TestMCPOnChangeNeverBlocksUI pins the frozen-ctrl-p regression. A palette
// import toggle (mcpSetImport → AddServers/RemoveServers → fireOnChange) runs
// ON the UI goroutine from inside Update. The manager's OnChange callback
// then calls prog.Send, which parks on the program's unbuffered msgs channel
// — and calling it from the event loop itself deadlocks the TUI forever
// (goroutine 1: bubbletea eventLoop → Send → fireOnChange → mcpSetImport →
// panelKey → Update). The callback must detach the Send.
//
// This exercises the REAL m.mcpOnChange() (shared by Run and mcpSetImport) —
// not an inline copy — so reverting it to a synchronous Send fails this test.
func TestMCPOnChangeNeverBlocksUI(t *testing.T) {
	m := tasksModel("http://unused")
	// A real program whose event loop never runs = a wedged UI: a synchronous
	// Send would block forever on the undrained queue (same harness as
	// TestSendTaskMsgNeverBlocksWorker).
	p := tea.NewProgram(m, tea.WithoutRenderer())
	defer p.Kill()
	m.prog = p

	m.mcpMgr = mcp.NewManager(nil)
	m.mcpMgr.SetOnChange(m.mcpOnChange()) // the production callback

	done := make(chan struct{})
	go func() {
		// The palette-enable path: adding a server fires OnChange synchronously
		// on the calling (UI) goroutine.
		m.mcpMgr.AddServers(context.Background(), map[string]mcp.ServerConfig{
			"docs": {Command: []string{"true"}},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the OnChange callback blocked on prog.Send — it must detach the Send (go m.prog.Send)")
	}
}

// TestMCPLazyManagerOnChangeDetached pins that the manager mcpSetImport builds
// lazily (when none exists) wires the same detached-Send callback.
func TestMCPLazyManagerOnChangeDetached(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	m := tasksModel("http://unused")
	m.cfg = &config.Config{}
	p := tea.NewProgram(m, tea.WithoutRenderer())
	defer p.Kill()
	m.prog = p

	// mcpSetImport builds the manager lazily when nil; its OnChange must also
	// detach the Send.
	m.mcpSetImport("claude", true)
	if m.mcpMgr == nil {
		t.Fatal("mcpSetImport should have built a manager")
	}
	done := make(chan struct{})
	go func() {
		m.mcpMgr.FireOnChangeForTest()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the lazily-built manager's OnChange callback blocked on Send — it must detach")
	}
}
