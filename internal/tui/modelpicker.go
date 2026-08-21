package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/loopy/internal/config"
)

// modelItem is one selectable model@provider route.
type modelItem struct {
	model    string
	provider string
	url      string
}

// modelPicker is the /model browser: models grouped, providers indented under them.
type modelPicker struct {
	items []modelItem
	idx   int
}

// buildModelItems flattens the config into selectable routes, models sorted
// alphabetically, providers in each model's declared order.
func buildModelItems(cfg *config.Config) []modelItem {
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	var items []modelItem
	for _, name := range names {
		for _, p := range cfg.Models[name].Providers {
			url := ""
			if prov, ok := cfg.Providers[p]; ok {
				url = prov.BaseURL
			}
			items = append(items, modelItem{model: name, provider: p, url: url})
		}
	}
	return items
}

func (m *model) openModelPicker() {
	items := buildModelItems(m.cfg)
	if len(items) == 0 {
		m.append(errStyle.Render("no models configured in ~/.loopy/config.json"))
		return
	}
	mp := &modelPicker{items: items}
	for i, it := range items { // start on the active route
		if it.model == m.modelName && it.provider == m.provName {
			mp.idx = i
			break
		}
	}
	m.mpicker = mp
}

func (m *model) modelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.mpicker
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.mpicker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab:
		if p.idx > 0 {
			p.idx--
		}
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if p.idx < len(p.items)-1 {
			p.idx++
		}
	case tea.KeyEnter:
		it := p.items[p.idx]
		m.mpicker = nil
		m.switchModel(it.model, it.provider)
	}
	return m, nil
}

func (m *model) modelPickerView() string {
	p := m.mpicker
	var rows []string
	lastModel := ""
	for i, it := range p.items {
		if it.model != lastModel {
			rows = append(rows, " "+it.model)
			lastModel = it.model
		}
		cur := ""
		if it.model == m.modelName && it.provider == m.provName {
			cur = dimStyle.Render("  (current)")
		}
		line := fmt.Sprintf("%-12s  ", it.provider) + dimStyle.Render(it.url)
		if i == p.idx {
			rows = append(rows, botStyle.Render("   → "+line)+cur)
		} else {
			rows = append(rows, "     "+line+cur)
		}
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑/↓ select · enter switch · esc cancel", p.idx+1, len(p.items))))
	avail := m.height - 1
	if avail < 1 { // terminal size unknown: no padding or windowing
		return strings.Join(rows, "\n")
	}
	for len(rows) < avail {
		rows = append(rows, "")
	}
	if len(rows) > avail { // small terminals: keep the selection visible
		start := max(min(p.idx-2, len(rows)-avail), 0)
		rows = rows[start : start+avail]
	}
	return strings.Join(rows, "\n")
}
