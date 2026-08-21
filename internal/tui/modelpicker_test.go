package tui

import (
	"testing"

	"github.com/context-labs/loopy/internal/config"
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
