package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/config"
	"github.com/context-labs/loopy/internal/llm"
)

func compactCmdModel() *model {
	// serve the compaction summary so a bare /compact completes in-test
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"sim"}}]}`))
	}))
	m := &model{
		input: newInput(),
		agent: agent.New(llm.New(srv.URL, "k"), "kimi-k3-fast", 100, "sys"),
		cfg: &config.Config{
			DefaultModel: "kimi-k3-fast",
			Providers:    map[string]config.Provider{"inference": {BaseURL: "https://x", APIKey: "k"}},
			Models: map[string]config.Model{
				"kimi-k3-fast": {Providers: []string{"inference"}},
				"glm-5.2-fast": {Providers: []string{"inference"}},
			},
		},
		modelName: "kimi-k3-fast",
		provName:  "inference",
		catalogs: map[string]config.Catalog{
			"inference": {Models: []config.ModelInfoLite{
				{ID: "kimi-k3-fast", ContextLength: 131072},
			}},
		},
	}
	m.width = 80
	m.input.SetWidth(78)
	return m
}

func TestCompactCommandSelectsModel(t *testing.T) {
	m := compactCmdModel()
	m.compactCommand([]string{"glm-5.2-fast"})
	if m.compactModel != "glm-5.2-fast" || m.compactProv != "" {
		t.Fatalf("compact model state: %q @ %q", m.compactModel, m.compactProv)
	}
	if m.agent.CompactModel != "glm-5.2-fast" || m.agent.CompactClient == nil {
		t.Fatalf("agent should summarize with glm-5.2-fast on its own client")
	}
	if m.cfg.CompactModel != "glm-5.2-fast" {
		t.Fatalf("config should persist the pick, got %q", m.cfg.CompactModel)
	}
	m.compactCommand([]string{"off"})
	if m.compactModel != "" || m.agent.CompactModel != "" || m.agent.CompactClient != nil {
		t.Fatalf("off should reset compaction to the current model: %q", m.compactModel)
	}
}

func TestCompactCommandRejectsUnknownModel(t *testing.T) {
	m := compactCmdModel()
	m.compactCommand([]string{"nope"})
	if m.compactModel != "" || m.agent.CompactModel != "" {
		t.Fatal("unknown model must not become the compaction model")
	}
	if !strings.Contains(m.blocks[len(m.blocks)-1], "unknown model") {
		t.Fatalf("expected an error note, got %v", m.blocks)
	}
}

func TestContextLimitFromCatalog(t *testing.T) {
	m := compactCmdModel()
	if got := m.contextLimitFor("inference", "kimi-k3-fast"); got != 131072 {
		t.Fatalf("contextLimitFor: %d", got)
	}
	if got := m.contextLimitFor("inference", "unknown"); got != 0 {
		t.Fatalf("unknown model: %d", got)
	}
	// a fresh /models fetch re-resolves the agent's limit
	cats := map[string]config.Catalog{
		"inference": {Models: []config.ModelInfoLite{{ID: "kimi-k3-fast", ContextLength: 262144}}},
	}
	m.updateCatalogs(cats)
	if m.agent.ContextLimit != 262144 {
		t.Fatalf("agent limit should follow the catalog, got %d", m.agent.ContextLimit)
	}
}

// Bare /compact with no history reports there's nothing to fold rather than
// touching the compaction-model selection. (The busy path is exercised
// end-to-end in the running TUI; here m.prog is nil so we stay on the
// synchronous error branch.)
func TestCompactBareKeepsSelection(t *testing.T) {
	m := compactCmdModel()
	m.compactModel, m.compactProv = "glm-5.2-fast", ""
	m.applyCompactModel()
	m.busy = true // busy path: synchronous, never starts the goroutine
	m.command("/compact")
	if m.compactModel != "glm-5.2-fast" || m.agent.CompactModel != "glm-5.2-fast" {
		t.Fatal("bare /compact must not change the compaction-model selection")
	}
}
