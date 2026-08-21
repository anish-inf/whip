package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/loopy/internal/agent"
	"github.com/context-labs/loopy/internal/config"
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

// The ctrl+p palette's first suggestion is Switch model; enter opens the
// interactive model picker.
func TestModelPaletteEnterOpensPicker(t *testing.T) {
	m := modelCmdModel()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = tm.(*model)
	if m.palette == nil {
		t.Fatal("ctrl+p should open the command palette")
	}
	if len(m.palette.items) == 0 || m.palette.items[0].title != "Switch model" {
		t.Fatalf("first suggestion should be Switch model, got %+v", m.palette.items)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.mpicker == nil {
		t.Fatalf("palette Switch model + enter should open the picker; input=%q", m.input.Value())
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
