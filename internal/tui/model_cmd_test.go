package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/config"
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

// The ctrl+p palette's first suggestion is Model; enter drills into its
// interactive panel without leaving the palette.
func TestModelPaletteEnterOpensPicker(t *testing.T) {
	m := modelCmdModel()
	tm, _ := m.key(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = tm.(*model)
	if m.palette == nil {
		t.Fatal("ctrl+p should open the command palette")
	}
	if len(m.palette.items) == 0 || m.palette.items[0].title != "Model" {
		t.Fatalf("first suggestion should be Model, got %+v", m.palette.items)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	pp := m.palette.top()
	if pp == nil || pp.kind != panelModel {
		t.Fatalf("palette Model + enter should push the model panel; input=%q", m.input.Value())
	}
	if len(pp.items) == 0 {
		t.Fatal("model panel should list the configured routes")
	}
}

// /model-for-session switches the active model but leaves the saved default
// untouched, so the next launch still opens on the configured model.
// (compactCmdModel: switchModel carries history, so the fixture needs a real
// agent — modelCmdModel's bare &agent.Agent{} has no system prompt to keep.)
func TestModelForSessionDoesNotPersist(t *testing.T) {
	m := compactCmdModel()
	m.command("/model-for-session glm-5.2-fast")
	if m.modelName != "glm-5.2-fast" {
		t.Fatalf("session switch should change the active model, got %q", m.modelName)
	}
	if m.cfg.DefaultModel != "kimi-k3-fast" {
		t.Errorf("saved default must not move, got %q", m.cfg.DefaultModel)
	}
	last := m.blocks[len(m.blocks)-1].text
	if !strings.Contains(last, "this session only") {
		t.Errorf("session-only switch should say so, got %q", last)
	}
}

// The same switch through /model persists as the new default.
func TestModelPersists(t *testing.T) {
	m := compactCmdModel()
	m.command("/model glm-5.2-fast")
	if m.cfg.DefaultModel != "glm-5.2-fast" {
		t.Fatalf("/model should store the switch as the default, got %q", m.cfg.DefaultModel)
	}
}

// Bare /model-for-session opens the picker flagged session-only, and a picker
// selection still doesn't persist.
func TestModelForSessionPicker(t *testing.T) {
	m := compactCmdModel()
	m.command("/model-for-session")
	if m.mpicker == nil {
		t.Fatal("bare /model-for-session should open the picker")
	}
	if !m.mpicker.sessionOnly {
		t.Fatal("picker should be flagged session-only")
	}
	p := m.mpicker
	p.idx = 0 // items are sorted; glm-5.2-fast precedes kimi-k3-fast
	if p.items[p.idx].model != "glm-5.2-fast" {
		p.idx = 1
	}
	tm, _ := m.modelPickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = tm.(*model)
	if m.modelName != "glm-5.2-fast" {
		t.Fatalf("picker enter should switch the active model, got %q", m.modelName)
	}
	if m.cfg.DefaultModel != "kimi-k3-fast" {
		t.Errorf("session-only picker must not move the saved default, got %q", m.cfg.DefaultModel)
	}
}

// /model-for-session completes model names like /model.
func TestModelForSessionCompletion(t *testing.T) {
	m := modelCmdModel()
	_, cs := completions("/model-for-session glm", m.modelCands(), m.providerCands(), nil, nil)
	if len(cs) != 1 || cs[0].Text != "glm-5.2-fast" {
		t.Fatalf("model names should complete under /model-for-session, got %+v", cs)
	}
	_, cs = completions("/model-for-session glm-5.2-fast inf", m.modelCands(), m.providerCands(), nil, nil)
	if len(cs) != 1 || cs[0].Text != "inference" {
		t.Fatalf("providers should complete as the second arg, got %+v", cs)
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
