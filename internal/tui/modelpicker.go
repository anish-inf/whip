package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/config"
)

// dimNew marks catalog-advertised routes that have no config entry yet.
const dimNew = "  (new)"

// modelItem is one selectable model@provider route.
type modelItem struct {
	model    string
	provider string
	url      string
	// fromCatalog marks routes advertised by the provider's /models catalog
	// rather than configured in ~/.whip/config.json — rendered dim with a
	// (new) marker.
	fromCatalog bool
}

// modelPicker is the /model browser: models grouped, providers indented under them.
type modelPicker struct {
	items      []modelItem
	idx        int
	staleHints []string // providers whose cached catalog is past its TTL
}

// buildModelItems flattens the config into selectable routes, models sorted
// alphabetically, providers in each model's declared order. Models advertised
// by a provider's cached /models catalog but absent from cfg.Models follow in
// a dim "(new)" section — selecting one resolves through the catalog fallback
// and persists to config only via switchModel.
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
	return appendCatalogRoutes(items, cfg, config.LoadCatalogs())
}

// appendCatalogRoutes adds one route per catalog-advertised model that has no
// cfg.Models entry, sorted by model name. Configured models win: a catalog id
// already in cfg.Models adds nothing.
func appendCatalogRoutes(items []modelItem, cfg *config.Config, cats map[string]config.Catalog) []modelItem {
	provs := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		provs = append(provs, name)
	}
	sort.Strings(provs)
	var extra []modelItem
	for _, p := range provs {
		cat, ok := cats[p]
		if !ok {
			continue
		}
		for _, mi := range cat.Models {
			if _, configured := cfg.Models[mi.ID]; configured {
				continue
			}
			extra = append(extra, modelItem{model: mi.ID, provider: p, url: cat.BaseURL, fromCatalog: true})
		}
	}
	sort.Slice(extra, func(a, b int) bool {
		if extra[a].model != extra[b].model {
			return extra[a].model < extra[b].model
		}
		return extra[a].provider < extra[b].provider
	})
	return append(items, extra...)
}

// staleCatalogs names configured providers whose cached catalog is missing or
// past its TTL — the picker's hint that freshly announced models may not show.
func staleCatalogs(cfg *config.Config, cats map[string]config.Catalog) []string {
	var out []string
	for name := range cfg.Providers {
		if cat, ok := cats[name]; !ok || cat.Stale() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (m *model) openModelPicker() {
	items := buildModelItems(m.cfg)
	if len(items) == 0 {
		m.append(errStyle.Render("no models configured in ~/.whip/config.json"))
		return
	}
	mp := &modelPicker{items: items, staleHints: staleCatalogs(m.cfg, config.LoadCatalogs())}
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
		heading := " " + it.model
		if it.fromCatalog {
			heading = dimStyle.Render(heading + dimNew)
		}
		if it.model != lastModel {
			rows = append(rows, heading)
			lastModel = it.model
		}
		cur := ""
		if it.model == m.modelName && it.provider == m.provName {
			cur = dimStyle.Render("  (current)")
		}
		line := fmt.Sprintf("%-12s  ", it.provider) + dimStyle.Render(it.url)
		if it.fromCatalog {
			line = dimStyle.Render(line)
		}
		if i == p.idx {
			rows = append(rows, botStyle.Render("   → "+line)+cur)
		} else {
			rows = append(rows, "     "+line+cur)
		}
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑/↓ select · enter switch · esc cancel", p.idx+1, len(p.items))))
	if len(p.staleHints) > 0 {
		rows = append(rows, dimStyle.Render("  catalog stale for "+strings.Join(p.staleHints, ", ")+" — /model refresh to pull newly announced models"))
	}
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
