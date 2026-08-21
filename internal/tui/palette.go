package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// paletteItem is one row in the ctrl+p command palette. It mirrors opencode's
// DialogSelectOption: title + description + category header + a dimmed hint
// (the keybind or slash form — the palette teaches the shortcuts).
type paletteItem struct {
	title     string // display name, e.g. "Switch model"
	desc      string // longer explanation
	category  string // "Agent", "Session", "Display", "App"
	hint      string // keybind or slash form, dimmed on the right
	suggested bool   // pinned into a "Suggested" group when the filter is empty
	run       func(m *model) (tea.Model, tea.Cmd)
}

// palette is the ctrl+p command palette: a modal full-screen dialog with its
// own filter line (opencode's DialogSelect). Typing fuzzy-filters, ↑/↓ moves,
// enter runs, esc pops.
type palette struct {
	items    []paletteItem // filtered
	all      []paletteItem // unfiltered
	idx      int
	filter   string
	suggOnly bool // filter empty → "Suggested" group on top (opencode's suggested:true)
}

// paletteItems builds the registry. Actions dispatch through m.command so the
// palette, slash commands, and /help share one implementation (opencode's
// single command registry → palette + slash + help projection).
func (m *model) paletteItems() []paletteItem {
	quit := func(m *model) (tea.Model, tea.Cmd) { return m, tea.Quit }
	return []paletteItem{
		{title: "Switch model", desc: "Pick a model and provider", category: "Agent", hint: "/model · tab", suggested: true,
			run: func(m *model) (tea.Model, tea.Cmd) { m.openModelPicker(); return m, nil }},
		{title: "Cycle effort", desc: "off → low → medium → high (or click ⚡)", category: "Agent", hint: "/effort",
			run: func(m *model) (tea.Model, tea.Cmd) { return m.command("/effort") }},
		{title: "Resume session", desc: "Browse and resume previous sessions", category: "Session", hint: "/resume", suggested: true,
			run: func(m *model) (tea.Model, tea.Cmd) { return m.command("/resume") }},
		{title: "New session", desc: "Reset the conversation and start fresh", category: "Session", hint: "/clear",
			run: func(m *model) (tea.Model, tea.Cmd) { return m.command("/clear") }},
		{title: "Compact session", desc: "Summarize old turns to free context", category: "Session", hint: "/compact",
			run: func(m *model) (tea.Model, tea.Cmd) { return m.command("/compact") }},
		{title: "Set goal", desc: "Keep working until the goal is met", category: "Session", hint: "/goal",
			run: func(m *model) (tea.Model, tea.Cmd) {
				m.input.SetValue("/goal ")
				m.input.CursorEnd()
				return m, nil
			}},
		{title: "Toggle thinking tokens", desc: "Show or hide model reasoning", category: "Display", hint: "ctrl+o",
			run: func(m *model) (tea.Model, tea.Cmd) { return m.key(tea.KeyMsg{Type: tea.KeyCtrlO}) }},
		{title: "Toggle mouse capture", desc: "Off = native terminal selection", category: "Display", hint: "/mouse",
			run: func(m *model) (tea.Model, tea.Cmd) { return m.command("/mouse") }},
		{title: "Help", desc: "Show all commands and keybindings", category: "App", hint: "/help",
			run: func(m *model) (tea.Model, tea.Cmd) { return m.command("/help") }},
		{title: "Quit", desc: "Exit loopy", category: "App", hint: "/quit · ctrl+c",
			run: quit},
	}
}

func (m *model) openPalette() {
	all := m.paletteItems()
	m.palette = &palette{all: all}
	m.palette.applyFilter()
}

// paletteFilterMatch is a cheap fuzzy match: all query runes must appear in
// order across title+category (case-insensitive). Good enough for ~10 rows
// without pulling in fuzzysort.
func paletteFilterMatch(query string, it paletteItem) bool {
	if query == "" {
		return true
	}
	hay := strings.ToLower(it.title + " " + it.category + " " + it.hint)
	for _, r := range strings.ToLower(query) {
		i := strings.IndexRune(hay, r)
		if i < 0 {
			return false
		}
		hay = hay[i+1:]
	}
	return true
}

// applyFilter recomputes the visible rows. With an empty filter,
// suggested entries pin into a "Suggested" category on top (opencode), then
// everything else grouped by category.
func (p *palette) applyFilter() {
	q := p.filter
	var items []paletteItem
	for _, it := range p.all {
		if paletteFilterMatch(q, it) {
			items = append(items, it)
		}
	}
	// stable category grouping (first-seen order)
	seen := map[string]bool{}
	var cats []string
	for _, it := range items {
		if !seen[it.category] {
			seen[it.category] = true
			cats = append(cats, it.category)
		}
	}
	var grouped []paletteItem
	for _, c := range cats {
		for _, it := range items {
			if it.category == c {
				grouped = append(grouped, it)
			}
		}
	}
	if q == "" {
		var sugg []paletteItem
		for _, it := range grouped {
			if it.suggested {
				sugg = append(sugg, it)
			}
		}
		if len(sugg) > 0 {
			for i := range sugg {
				sugg[i].category = "Suggested"
			}
			grouped = append(sugg, grouped...)
		}
	}
	p.items = grouped
	if p.idx >= len(p.items) {
		p.idx = max(len(p.items)-1, 0)
	}
}

// paletteKey handles input while the palette is open: esc pops, ↑/↓ move,
// enter runs, typing edits the filter. (opencode: dialogs push a mode; esc
// pops one level.)
func (m *model) paletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.palette
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.palette = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if p.idx > 0 {
			p.idx--
		} else {
			p.idx = len(p.items) - 1
		}
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if p.idx < len(p.items)-1 {
			p.idx++
		} else {
			p.idx = 0
		}
	case tea.KeyEnter:
		if len(p.items) == 0 {
			return m, nil
		}
		it := p.items[p.idx]
		m.palette = nil
		return it.run(m)
	case tea.KeyBackspace, tea.KeyDelete:
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.applyFilter()
		}
	case tea.KeyRunes:
		p.filter += string(msg.Runes)
		p.idx = 0
		p.applyFilter()
	}
	return m, nil
}

// paletteView renders the modal dialog: a title bar, the filter line, and
// category-grouped rows with dimmed hints.
func (m *model) paletteView() string {
	p := m.palette
	var b strings.Builder
	b.WriteString(botStyle.Render(" Commands"))
	if p.filter != "" {
		b.WriteString(dimStyle.Render("  — type to filter"))
	}
	b.WriteString("\n\n")
	b.WriteString(" " + youStyle.Render("❯ ") + p.filter + dimStyle.Render("█"))
	b.WriteString("\n\n")

	lastCat := ""
	hintW := 0
	for _, it := range p.items {
		hintW = max(hintW, len(it.hint))
	}
	for i, it := range p.items {
		if it.category != lastCat {
			if lastCat != "" {
				b.WriteString("\n")
			}
			b.WriteString(dimStyle.Render("  " + it.category))
			b.WriteString("\n")
			lastCat = it.category
		}
		hint := dimStyle.Render(fmt.Sprintf("%*s", hintW, it.hint))
		line := " " + it.title
		if it.desc != "" {
			line += dimStyle.Render("  — " + it.desc)
		}
		if i == p.idx {
			b.WriteString(botStyle.Render("→") + line + "  " + hint)
		} else {
			b.WriteString(" " + line + "  " + hint)
		}
		b.WriteString("\n")
	}
	if len(p.items) == 0 {
		b.WriteString(dimStyle.Render("  (no matches)"))
		b.WriteString("\n")
	}
	b.WriteString("\n" + dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑/↓ select · enter run · esc close", min(p.idx+1, len(p.items)), len(p.items))))
	return b.String()
}
