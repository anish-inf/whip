package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/mcp"
	"github.com/context-labs/whip/internal/skills"
)

// /context-doctor — audit what a FRESH session injects before
// the user types anything, and what each piece costs in estimated tokens.
// The audience is someone arriving from claude/codex whose first call carries
// tens of thousands of tokens of skill/MCP/tool-schema bloat they never asked
// for; the doctor names every source and its cost so it can be audited (and
// trimmed) instead of silently paid.

// ctxRow is one line of the audit.
type ctxRow struct {
	label string
	bytes int
	note  string
}

func (r ctxRow) tokens() int { return (r.bytes + 3) / 4 }

// doctorReport builds the audit as pure data (testable), then renders.
func (m *model) doctorReport() string {
	var rows []ctxRow

	// Base system prompt (the static text; skills/MCP blocks are appended per
	// turn in prepareTurn).
	rows = append(rows, ctxRow{"system prompt (base)", len(m.sysPrompt), ""})

	// Skills: block total + the worst offenders.
	sk := skills.Scan(skills.DefaultDirs()...)
	block := skills.PromptBlock(sk)
	row := ctxRow{fmt.Sprintf("skills (%d loaded)", len(sk)), len(block), ""}
	// Per-skill line cost in the block: "- name: desc (path)\n".
	type sc struct {
		name string
		n    int
	}
	var per []sc
	for _, s := range sk {
		n := len(s.Name) + min(len(s.Description), 300) + len(s.Path) + 8
		per = append(per, sc{s.Name, n})
	}
	sort.Slice(per, func(i, j int) bool { return per[i].n > per[j].n })
	var top []string
	for i := 0; i < len(per) && i < 5; i++ {
		top = append(top, fmt.Sprintf("%s ~%dtok", per[i].name, (per[i].n+3)/4))
	}
	if len(top) > 0 {
		row.note = "biggest: " + strings.Join(top, ", ")
	}
	rows = append(rows, row)

	// MCP: per-server tool schemas as they'd appear in the request.
	if m.mcpMgr != nil {
		toolBytes := map[string]int{}
		for _, t := range m.mcpMgr.Tools() {
			n := t.Def.Function.Name
			srv := n
			if i := strings.Index(strings.TrimPrefix(n, "mcp__"), "__"); i >= 0 {
				srv = strings.TrimPrefix(n, "mcp__")[:i]
			}
			schema, _ := json.Marshal(t.Def)
			toolBytes[srv] += len(schema) + len(n) + 8
		}
		for _, st := range m.mcpMgr.Statuses() {
			switch st.Status {
			case mcp.StatusReady:
				b := toolBytes[st.Name]
				rows = append(rows, ctxRow{fmt.Sprintf("mcp: %s (%d tools)", st.Name, st.Tools), b, ""})
			case mcp.StatusFailed:
				rows = append(rows, ctxRow{"mcp: " + st.Name, 0, "failed — contributes 0 tools"})
			case mcp.StatusDisabled:
				rows = append(rows, ctxRow{"mcp: " + st.Name, 0, "disabled"})
			default:
				rows = append(rows, ctxRow{"mcp: " + st.Name, 0, "still connecting — 0 tools yet"})
			}
		}
		if ib := m.mcpMgr.InstructionsBlock(); ib != "" {
			rows = append(rows, ctxRow{"mcp: server instructions", len(ib), ""})
		}
	}

	// Built-in tool schemas (what the provider is sent every request).
	var tb int
	for _, t := range m.agent.AllTools() {
		schema, _ := json.Marshal(t.Def)
		tb += len(schema) + 8
	}
	rows = append(rows, ctxRow{fmt.Sprintf("tool schemas (%d tools)", len(m.agent.AllTools())), tb, "sent with every request"})

	// History: tokens already in the conversation (0 on a fresh session).
	hist := agent.EstimateTokens(m.agent.Messages)
	if hist > 0 {
		rows = append(rows, ctxRow{"conversation history", hist * 4, "estimated"})
	}
	// Session spend so far (real usage, if any request has happened).
	if u := m.agent.Usage(); u.PromptTokens > 0 {
		rows = append(rows, ctxRow{"session spend so far", 0, fmt.Sprintf("%s in / %s out (actual)", tok(u.PromptTokens), tok(u.CompletionTokens))})
	}

	// Render.
	var b strings.Builder
	b.WriteString("Fresh-session context audit (estimated tokens)\n")
	total := 0
	w := 0
	for _, r := range rows {
		if len(r.label) > w {
			w = len(r.label)
		}
		total += r.tokens()
	}
	for _, r := range rows {
		line := fmt.Sprintf("  %-*s %7s", w, r.label, "~"+tok(r.tokens()))
		if r.note != "" {
			line += "  " + r.note
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(&b, "  %-*s %7s\n", w, "TOTAL injected before you type", "~"+tok(total))
	b.WriteString("\nTrim: /mcp <name> disable · remove a skill from .agents/skills · /context-doctor again")
	return b.String()
}

// tok renders a token count compactly (1.2k, 350).
func tok(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
