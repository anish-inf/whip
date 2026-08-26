package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/mcp"
)

// mcpCommand handles "/mcp [name] [reconnect|enable|disable]".
func (m *model) mcpCommand(fields []string) (tea.Model, tea.Cmd) {
	if m.mcpMgr == nil {
		m.append(dimStyle.Render("no MCP servers configured — add one with `whip mcp add <name> -- <cmd...>`, a .mcp.json, or ~/.codex/config.toml"))
		return m, nil
	}
	if len(fields) == 1 {
		m.append(m.mcpStatusView())
		return m, nil
	}
	name := fields[1]
	action := "reconnect"
	if len(fields) > 2 {
		action = fields[2]
	}
	switch action {
	case "reconnect":
		if !m.mcpMgr.Reconnect(name) {
			m.append(errStyle.Render("no MCP server named " + name))
			return m, nil
		}
		m.append(dimStyle.Render(fmt.Sprintf("↻ reconnecting mcp server %s…", name)))
	case "disable", "enable":
		m.mcpSetEnabled(name, action == "enable")
	default:
		m.append(errStyle.Render("usage: /mcp [name] [reconnect|enable|disable]"))
	}
	return m, nil
}

// mcpSetEnabled persists a toggle into whip's own config and applies it
// live. For imported (claude/codex) servers the FULL definition is copied
// into whip's config first — otherwise a bare {enabled:false} entry would
// shadow the import on next launch and lose the command/url for re-enable.
func (m *model) mcpSetEnabled(name string, enabled bool) {
	if m.mcpMgr.BlockedByPolicy(name) {
		m.append(errStyle.Render(fmt.Sprintf("mcp server %s is blocked by the mcpImport config — edit ~/.whip/config.json (or remove the gate) to enable it", name)))
		return
	}
	live, ok := m.mcpMgr.Config(name)
	if !ok {
		m.append(errStyle.Render("no MCP server named " + name))
		return
	}
	entry := config.MCPServer{
		Command: live.Command, Env: live.Env, Cwd: live.Cwd,
		URL: live.URL, Headers: live.Headers,
		StartupTimeout: live.StartupTimeout, ToolTimeout: live.ToolTimeout,
		Enabled: &enabled,
	}
	if m.cfg.MCPServers == nil {
		m.cfg.MCPServers = map[string]config.MCPServer{}
	}
	m.cfg.MCPServers[name] = entry
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
		return
	}
	if enabled {
		m.mcpMgr.Enable(name)
	} else {
		m.mcpMgr.Disable(name)
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	m.append(dimStyle.Render(fmt.Sprintf("mcp server %s: %s (persisted)", name, state)))
	m.append(m.mcpStatusView())
}

// mcpStatusView renders the /mcp table: one row per server with status,
// tool count, and failure detail.
func (m *model) mcpStatusView() string {
	servers := append(m.mcpMgr.Statuses(), m.mcpMgr.Blocked()...)
	if len(servers) == 0 {
		return dimStyle.Render("no MCP servers")
	}
	var b strings.Builder
	b.WriteString("MCP servers:\n")
	for _, s := range servers {
		icon := "◌"
		detail := ""
		switch s.Status {
		case mcp.StatusReady:
			icon = "●"
			detail = fmt.Sprintf("%d tools", s.Tools)
		case mcp.StatusFailed:
			icon = "✗"
			detail = s.Err
			if s.Source != "" {
				detail += " (" + s.Source + ")"
			}
		case mcp.StatusDisabled:
			icon = "○"
			detail = "disabled"
			if s.Note != "" {
				detail = "disabled — " + s.Note
			}
		case mcp.StatusConnecting:
			icon = "◌"
			detail = "connecting…"
		}
		line := fmt.Sprintf("  %s %-20s %s", icon, s.Name, detail)
		switch s.Status {
		case mcp.StatusReady:
			b.WriteString(line + "\n")
		case mcp.StatusFailed:
			b.WriteString(errStyle.Render(line) + "\n")
		default:
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
