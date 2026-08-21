package tui

import (
	"testing"

	"github.com/abe/loopy/internal/agent"
	"github.com/abe/loopy/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

func modelCmdModel() *model {
	m := &model{
		input: newInput(),
		agent: &agent.Agent{},
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
	}
	m.width = 80
	m.input.SetWidth(78)
	return m
}

func typeStr(t *testing.T, m *model, s string) *model {
	t.Helper()
	for _, r := range s {
		tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = tm.(*model)
	}
	return m
}

// Regression: typing /model and pressing enter must open the interactive
// picker, NOT insert a newline. (The newline bug was KeyCtrlM == KeyEnter
// being forwarded to the textarea; this guards against its return.)
func TestModelBareEnterOpensPicker(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/model")
	if m.menu == nil {
		t.Fatal("typing /model should focus the completion menu")
	}
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.mpicker == nil {
		t.Fatalf("/model + enter should open the model picker; input=%q LineCount=%d", m.input.Value(), m.input.LineCount())
	}
	if m.input.Value() != "" || m.input.LineCount() != 1 {
		t.Errorf("enter must not leave a newline in the input: value=%q LineCount=%d", m.input.Value(), m.input.LineCount())
	}
}

// Regression: the same via the ctrl+p palette (menu pre-focused, /model selected).
func TestModelPaletteEnterOpensPicker(t *testing.T) {
	m := modelCmdModel()
	// ctrl+p prefills "/" and opens the menu
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = tm.(*model)
	_ = m
	if m.menu == nil {
		t.Fatal("ctrl+p should open the command menu")
	}
	// navigate down to /model (sorted: clear, compact, effort, goal, help, model)
	for i := 0; i < 5; i++ {
		tm, _ := m.key(tea.KeyMsg{Type: tea.KeyDown})
		m = tm.(*model)
	}
	if got := m.menu.cands[m.menu.idx].Text; got != "/model" {
		t.Fatalf("expected /model selected, got %q", got)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.mpicker == nil {
		t.Fatalf("palette /model + enter should open the picker; input=%q", m.input.Value())
	}
}

// Selecting a model name completes it on the first enter; the second enter
// submits. Neither may insert a newline into the input.
func TestModelArgEnterNeverNewlines(t *testing.T) {
	m := modelCmdModel()
	m = typeStr(t, m, "/model glm")
	if m.menu == nil {
		t.Fatal("expected model-name completion menu")
	}
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // complete the name
	m = tm.(*model)
	if m.input.LineCount() != 1 {
		t.Fatalf("completing a model name must not newline: value=%q", m.input.Value())
	}
	if m.input.Value() == "/model glm" {
		t.Fatalf("enter should have accepted the completion, still %q", m.input.Value())
	}
}
