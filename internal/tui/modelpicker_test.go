package tui

import (
	"testing"

	"github.com/context-labs/whip/internal/config"
)

func TestBuildModelItems(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"a": {BaseURL: "https://a"},
			"b": {BaseURL: "https://b"},
		},
		Models: map[string]config.Model{
			"zeta":  {Providers: []string{"b", "a"}}, // declared order kept
			"alpha": {Providers: []string{"a", "ghost"}},
		},
	}
	items := buildModelItems(cfg)
	if len(items) != 4 {
		t.Fatalf("items: %+v", items)
	}
	// models sorted alphabetically
	if items[0].model != "alpha" || items[2].model != "zeta" {
		t.Fatalf("model order: %+v", items)
	}
	// provider order per model preserved
	if items[2].provider != "b" || items[3].provider != "a" {
		t.Fatalf("provider order: %+v", items)
	}
	if items[0].url != "https://a" {
		t.Fatalf("url: %+v", items[0])
	}
	if items[1].provider != "ghost" || items[1].url != "" {
		t.Fatalf("unknown provider should keep empty url: %+v", items[1])
	}
	if got := buildModelItems(&config.Config{}); len(got) != 0 {
		t.Fatalf("empty config: %+v", got)
	}
}

func TestModelPickerFilter(t *testing.T) {
	p := &modelPicker{items: []modelItem{
		{model: "alpha", provider: "a"},
		{model: "beta", provider: "a"},
		{model: "beta", provider: "b"},
		{model: "gamma", provider: "c"},
	}}

	// no query: full list
	if got := len(p.view()); got != 4 {
		t.Fatalf("unfiltered view: %d", got)
	}

	// substring on model name, case-insensitive
	p.query = "BET"
	p.applyQuery()
	if got := p.view(); len(got) != 2 || got[0].model != "beta" {
		t.Fatalf("filter by model: %+v", got)
	}

	// substring on provider name
	p.query = "c"
	p.applyQuery()
	if got := p.view(); len(got) != 1 || got[0].provider != "c" {
		t.Fatalf("filter by provider: %+v", got)
	}

	// no match: empty view, not a crash
	p.query = "zzz"
	p.applyQuery()
	if got := p.view(); len(got) != 0 {
		t.Fatalf("no-match view: %+v", got)
	}

	// clearing the query restores everything
	p.query = ""
	p.applyQuery()
	if got := len(p.view()); got != 4 {
		t.Fatalf("cleared query view: %d", got)
	}
}
