// Package tui is loopy's interactive bubbletea session (fullscreen alt-screen).
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/abe/loopy/internal/agent"
	"github.com/abe/loopy/internal/config"
	"github.com/abe/loopy/internal/llm"
	"github.com/abe/loopy/internal/session"
	"github.com/abe/loopy/internal/skills"
)

var (
	youStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	botStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	toolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// messages sent from the agent goroutine
type textMsg string
type toolStartMsg struct{ name, args string }
type toolEndMsg struct{ name, result string }
type steeredMsg string
type turnDoneMsg struct {
	final string
	err   error
}
type catalogsMsg map[string]config.Catalog // background /models fetch result

// menu is the open completion dropdown.
type menu struct {
	head  string // input before the token being completed
	cands []cand
	idx   int
}

type model struct {
	cfg       *config.Config
	agent     *agent.Agent
	modelName string
	provName  string
	sysPrompt string

	input  textarea.Model
	spin   spinner.Model
	vp     viewport.Model
	blocks []string // finalized transcript, pre-wrapped
	follow bool     // auto-scroll to bottom on new content
	width  int
	height int

	busy    bool
	current string // in-flight partial assistant line
	inMsg   bool   // "● " prefix already printed for this assistant segment
	menu    *menu
	picker  *picker
	mpicker *modelPicker
	cancel  context.CancelFunc
	prog    *tea.Program

	store     *session.Store
	sessionID string
	saved     int // messages already persisted (index into agent.Messages)

	hist    []string // submitted inputs, for up/down recall
	histIdx int      // len(hist) == not navigating
	draft   string   // in-progress input saved while navigating history

	queue      []string // messages typed while busy, sent after the turn ends
	interrupt1 bool     // first ctrl+c pressed while busy; second cancels

	goal       string // active /goal; the loop continues until GOAL_MET
	goalRounds int    // continuation turns spent on the current goal

	effortX  int                       // screen column where the clickable ⚡ effort control starts
	catalogs map[string]config.Catalog // provider model lists (capabilities)
}

// picker is the /resume session browser. metas is newest-first; the list is
// rendered oldest-at-top so newest sits at the bottom.
type picker struct {
	metas    []session.Meta
	idx      int                  // selected index into metas (0 = newest)
	previews map[string][2]string // id -> last user, last assistant
}

// Run starts the interactive session. It returns the id of the session that
// was active on exit ("" if nothing was said).
func Run(cfg *config.Config, modelName, provName, sysPrompt, resumeID string) (string, error) {
	ag, mn, pn, err := buildAgent(cfg, modelName, provName, sysPrompt)
	if err != nil {
		return "", err
	}

	ti := textarea.New()
	ti.Placeholder = "Ask loopy anything… (/ for commands, tab completes)"
	ti.Prompt = "┃ "
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	// The default adaptive styles misdetect the background over mosh/tmux;
	// use plain ANSI colors and no cursor-line background.
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ti.FocusedStyle.Placeholder = dimStyle
	ti.BlurredStyle.Placeholder = dimStyle
	ti.FocusedStyle.Prompt = botStyle
	ti.BlurredStyle.Prompt = dimStyle
	ti.Focus()

	ag.Effort = cfg.DefaultEffort
	m := &model{
		cfg: cfg, agent: ag, modelName: mn, provName: pn, sysPrompt: sysPrompt,
		input: ti, spin: spinner.New(spinner.WithSpinner(spinner.Dot)), follow: true, saved: 1,
		catalogs: config.LoadCatalogs(),
	}
	if dir, derr := config.Dir(); derr == nil {
		if st, serr := session.Open(dir + "/sessions.db"); serr == nil {
			m.store = st
			defer st.Close()
		} else {
			m.append(errStyle.Render("sessions disabled: " + serr.Error()))
		}
	}
	if resumeID != "" {
		if m.store == nil {
			return "", fmt.Errorf("cannot resume: session store unavailable")
		}
		if err := m.resume(resumeID); err != nil {
			return "", err
		}
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m.prog = p
	go m.fetchCatalogs()
	_, err = p.Run()
	return m.sessionID, err
}

// fetchCatalogs refreshes each provider's cached model list in the background
// and sends the merged result to the UI.
func (m *model) fetchCatalogs() {
	cats := config.LoadCatalogs()
	dirty := false
	for name, prov := range m.cfg.Providers {
		if c, ok := cats[name]; ok && !c.Stale() && c.BaseURL == prov.BaseURL {
			continue
		}
		key := prov.Key()
		if key == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		infos, err := llm.New(prov.BaseURL, key).Models(ctx)
		cancel()
		if err != nil {
			continue // keep any stale cache
		}
		models := make([]config.ModelInfoLite, len(infos))
		for i, mi := range infos {
			models[i] = config.ModelInfoLite{ID: mi.ID, ReasoningEfforts: mi.ReasoningEfforts}
		}
		cats[name] = config.Catalog{FetchedAt: time.Now(), BaseURL: prov.BaseURL, Models: models}
		dirty = true
	}
	if dirty {
		config.SaveCatalogs(cats) // best-effort; the TUI still gets the fresh data
	}
	m.prog.Send(catalogsMsg(cats))
}

// resume replaces the conversation with a stored session.
func (m *model) resume(id string) error {
	meta, msgs, err := m.store.Load(id)
	if err != nil {
		return err
	}
	// prefer the session's model/provider; fall back to current on error
	effort := m.agent.Effort
	if ag, mn, pn, err := buildAgent(m.cfg, meta.Model, meta.Provider, m.sysPrompt); err == nil {
		m.agent, m.modelName, m.provName = ag, mn, pn
	} else {
		m.agent = agent.New(m.agent.Client, m.agent.Model, m.agent.MaxTokens, m.sysPrompt)
		m.agent.ModelName, m.agent.Provider = m.modelName, m.provName
	}
	m.agent.Messages = append(m.agent.Messages, msgs...)
	if contains(m.effortsFor(), effort) {
		m.agent.Effort = effort
	}
	m.sessionID = meta.ID
	m.saved = len(m.agent.Messages)
	for _, msg := range msgs {
		if msg.Role == "user" {
			m.hist = append(m.hist, msg.Content)
		}
	}
	m.histIdx = len(m.hist)
	m.blocks = nil
	m.goal = meta.Goal
	m.goalRounds = 0
	m.append(dimStyle.Render(fmt.Sprintf("resumed %s · %s · %s @ %s", meta.ID, meta.Title, m.modelName, m.provName)))
	if m.goal != "" {
		m.append(dimStyle.Render("◎ goal restored — /goal resume to keep working on it"))
	}
	m.seedTranscript(msgs)
	return nil
}

// seedTranscript re-renders stored messages into the viewport.
func (m *model) seedTranscript(msgs []llm.Message) {
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			m.append(wrap(youStyle.Render("❯ ")+msg.Content, m.width))
		case "assistant":
			if strings.TrimSpace(msg.Content) != "" {
				m.append(wrap(botStyle.Render("● ")+strings.TrimRight(msg.Content, "\n"), m.width))
			}
			for _, tc := range msg.ToolCalls {
				args := tc.Function.Arguments
				if len(args) > 120 {
					args = args[:120] + "…"
				}
				m.append(toolStyle.Render("⚒ "+tc.Function.Name+" ") + dimStyle.Render(args))
			}
		}
	}
}

// persist writes any unsaved messages to the session store.
func (m *model) persist() {
	if m.store == nil || len(m.agent.Messages) <= m.saved {
		return
	}
	if m.sessionID == "" {
		id, err := m.store.Create(cwd(), m.modelName, m.provName)
		if err != nil {
			m.append(errStyle.Render("session save failed: " + err.Error()))
			return
		}
		m.sessionID = id
	}
	if err := m.store.Save(m.sessionID, m.saved, m.agent.Messages, m.modelName, m.provName); err != nil {
		m.append(errStyle.Render("session save failed: " + err.Error()))
		return
	}
	m.store.SetGoal(m.sessionID, m.goal)
	m.saved = len(m.agent.Messages)
}

// setEffort changes the reasoning effort and stores it as the new default.
func (m *model) setEffort(lv string) {
	m.agent.Effort = lv
	m.cfg.DefaultEffort = lv
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
}

// setGoal updates the active goal and persists it with the session.
func (m *model) setGoal(goal string) {
	m.goal = goal
	m.goalRounds = 0
	if m.store != nil && m.sessionID != "" {
		m.store.SetGoal(m.sessionID, goal)
	}
}

func buildAgent(cfg *config.Config, modelName, provName, sysPrompt string) (*agent.Agent, string, string, error) {
	prov, mdl, apiID, err := cfg.Resolve(modelName, provName)
	if err != nil {
		return nil, "", "", err
	}
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	if provName == "" {
		provName = cfg.DefaultProvider
		if provName == "" && len(mdl.Providers) > 0 {
			provName = mdl.Providers[0]
		}
	}
	key := prov.Key()
	if key == "" {
		return nil, "", "", fmt.Errorf("no API key for provider %q (set apiKey/apiKeyEnv in ~/.loopy/config.json)", provName)
	}
	ag := agent.New(llm.New(prov.BaseURL, key), apiID, mdl.MaxTokens, sysPrompt)
	ag.ModelName, ag.Provider = modelName, provName
	return ag, modelName, provName, nil
}

// append adds finalized blocks to the transcript.
func (m *model) append(blocks ...string) {
	m.blocks = append(m.blocks, blocks...)
	m.follow = true
	m.refreshVP()
}

// refreshVP rebuilds the viewport content, bottom-anchored: short transcripts
// are padded from the top so messages grow upward from the input.
// ponytail: O(transcript) re-join per append; rope/ring buffer if huge sessions drag
func (m *model) refreshVP() {
	content := strings.Join(m.blocks, "\n")
	if n := lipgloss.Height(content); n < m.vp.Height {
		content = strings.Repeat("\n", m.vp.Height-n) + content
	}
	m.vp.SetContent(content)
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m *model) Init() tea.Cmd {
	return textarea.Blink
}

func cwd() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "?"
}

// layout gives the viewport whatever height the chrome doesn't need.
func (m *model) layout() {
	chrome := 5 // header + tips + top pad + input + bottom pad
	if m.busy {
		chrome++
	}
	if m.current != "" {
		chrome += lipgloss.Height(m.currentView())
	}
	if m.menu != nil {
		chrome += min(len(m.menu.cands), menuRows) + 1
	}
	if len(m.queue) > 0 {
		chrome += len(m.queue) + 1
	}
	w, h := m.width, max(m.height-chrome, 1)
	if m.vp.Width != w || m.vp.Height != h {
		m.vp.Width, m.vp.Height = w, h
		m.refreshVP()
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.layout()

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(msg.Width - 2)
		return m, nil

	case tea.KeyMsg:
		return m.key(msg)

	case tea.MouseMsg:
		// clicking the ⚡ control in the header cycles reasoning effort
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft &&
			msg.Y == 0 && msg.X >= m.effortX {
			m.setEffort(nextEffort(m.effortsFor(), m.agent.Effort))
			return m, nil
		}
		if m.picker == nil && m.mpicker == nil {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			m.follow = m.vp.AtBottom()
			return m, cmd
		}
		return m, nil

	case textMsg:
		m.current += string(msg)
		// Move complete lines into the transcript so the streaming area
		// only ever re-renders the last partial line.
		if i := strings.LastIndexByte(m.current, '\n'); i >= 0 {
			done := m.current[:i]
			m.current = m.current[i+1:]
			m.appendAssistant(done)
		}
		return m, nil

	case toolStartMsg:
		m.flushCurrent()
		args := msg.args
		if len(args) > 120 {
			args = args[:120] + "…"
		}
		m.append(toolStyle.Render("⚒ "+msg.name+" ") + dimStyle.Render(args))
		return m, nil

	case toolEndMsg:
		lines := strings.Split(strings.TrimRight(msg.result, "\n"), "\n")
		preview := lines
		if len(preview) > 5 {
			preview = preview[:5]
		}
		out := dimStyle.Render("  " + strings.Join(preview, "\n  "))
		if len(lines) > 5 {
			out += dimStyle.Render(fmt.Sprintf("\n  … +%d lines", len(lines)-5))
		}
		m.append(out)
		return m, nil

	case steeredMsg:
		m.flushCurrent()
		m.append(wrap(youStyle.Render("❯ ")+string(msg)+dimStyle.Render("  (steered)"), m.width))
		return m, nil

	case turnDoneMsg:
		m.flushCurrent()
		m.busy = false
		m.cancel = nil
		m.interrupt1 = false
		if msg.err != nil && msg.err != context.Canceled {
			m.append(errStyle.Render("error: " + msg.err.Error()))
		} else if msg.err == context.Canceled {
			m.append(dimStyle.Render("(interrupted)"))
		}
		m.persist()
		// codex-style follow-up: send queued messages one turn at a time
		if len(m.queue) > 0 && msg.err == nil {
			next := m.queue[0]
			m.queue = m.queue[1:]
			return m.submit(next)
		}
		// goal loop: keep working until the model explicitly declares GOAL_MET
		if m.goal != "" && msg.err == nil {
			if goalMet(msg.final) {
				m.append(dimStyle.Render("◎ goal met after " + fmt.Sprint(m.goalRounds) + " round(s)"))
				m.setGoal("")
				return m, nil
			}
			if m.goalRounds >= maxGoalRounds {
				m.append(errStyle.Render(fmt.Sprintf("◎ goal paused after %d rounds — /goal resume to continue, /goal clear to drop", m.goalRounds)))
				return m, nil
			}
			m.goalRounds++
			return m.submit(goalContinuePrompt(m.goal))
		}
		return m, nil

	case catalogsMsg:
		m.updateCatalogs(msg)
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.picker != nil {
		return m.pickerKey(msg)
	}
	if m.mpicker != nil {
		return m.modelPickerKey(msg)
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.busy && m.cancel != nil {
			// explicit interruption: first press arms, second cancels
			// ponytail: no reset timer; the flag clears on turn end
			if !m.interrupt1 {
				m.interrupt1 = true
				return m, nil
			}
			m.cancel()
			return m, nil
		}
		return m, tea.Quit

	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.follow = m.vp.AtBottom()
		return m, cmd

	case tea.KeyEsc:
		m.menu = nil
		return m, nil

	case tea.KeyTab, tea.KeyDown, tea.KeyCtrlN:
		if m.menu != nil {
			m.menu.idx = (m.menu.idx + 1) % len(m.menu.cands)
			return m, nil
		}
		if msg.Type == tea.KeyTab {
			m.openMenu()
			return m, nil
		}
		m.histNext()
		return m, nil

	case tea.KeyShiftTab, tea.KeyUp, tea.KeyCtrlP:
		if m.menu != nil {
			m.menu.idx = (m.menu.idx + len(m.menu.cands) - 1) % len(m.menu.cands)
			return m, nil
		}
		if msg.Type == tea.KeyUp {
			m.histPrev()
			return m, nil
		}
		if msg.Type == tea.KeyCtrlP { // command palette: the / menu, prefilled
			m.input.SetValue("/")
			m.refreshMenu()
		}
		return m, nil

	case tea.KeyEnter:
		if m.menu != nil {
			// bare commands that act without further args run immediately
			// (this is what makes the ctrl+p palette one-keystroke)
			if c := m.menu.cands[m.menu.idx]; m.menu.head == "" && execNow[c.Text] {
				m.menu = nil
				m.input.Reset()
				return m.command(c.Text)
			}
			if m.accept() {
				return m, nil // completed something; next enter submits
			}
			// selection was already fully typed — fall through to submit
		}
		text := strings.TrimSpace(m.input.Value())
		if m.busy {
			switch {
			case text != "": // codex-style: queue it (multiple allowed)
				m.queue = append(m.queue, text)
				m.hist = append(m.hist, text)
				m.histIdx = len(m.hist)
				m.input.Reset()
				m.menu = nil
			case len(m.queue) > 0: // grok-style: empty enter force-steers the queue
				sk := skills.Scan(skills.DefaultDirs()...)
				for _, q := range m.queue {
					m.agent.Steer(expandMentions(expandSkills(q, sk)))
				}
				m.queue = nil
			}
			return m, nil
		}
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.menu = nil
		m.hist = append(m.hist, text)
		m.histIdx = len(m.hist)
		m.draft = ""
		if strings.HasPrefix(text, "/") {
			return m.command(text)
		}
		return m.submit(text)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshMenu()
	return m, cmd
}

// histPrev/histNext recall submitted inputs with the arrow keys.
func (m *model) histPrev() {
	if len(m.hist) == 0 || m.histIdx == 0 {
		return
	}
	if m.histIdx == len(m.hist) {
		m.draft = m.input.Value()
	}
	m.histIdx--
	m.input.SetValue(m.hist[m.histIdx])
}

func (m *model) histNext() {
	if m.histIdx >= len(m.hist) {
		return
	}
	m.histIdx++
	if m.histIdx == len(m.hist) {
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.hist[m.histIdx])
	}
}

// switchModel rebuilds the agent on a new model/provider, carrying history.
func (m *model) switchModel(name, prov string) {
	ag, mn, pn, err := buildAgent(m.cfg, name, prov, m.sysPrompt)
	if err != nil {
		m.append(errStyle.Render(err.Error()))
		return
	}
	ag.Effort = m.agent.Effort
	ag.Messages = append(ag.Messages, m.agent.Messages[1:]...) // carry history
	m.agent, m.modelName, m.provName = ag, mn, pn
	if !contains(m.effortsFor(), ag.Effort) {
		m.setEffort("") // the new model doesn't support the current level
	}
	m.cfg.DefaultModel, m.cfg.DefaultProvider = mn, pn // store the switch as the new default
	if err := m.cfg.Save(); err != nil {
		m.append(errStyle.Render("config save failed: " + err.Error()))
	}
	m.append(dimStyle.Render("→ " + mn + " @ " + pn))
}

// pickerKey handles keys while the /resume browser is open.
func (m *model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.picker = nil
	case tea.KeyUp, tea.KeyCtrlP, tea.KeyShiftTab: // older sessions sit above
		if p.idx < len(p.metas)-1 {
			p.idx++
			p.loadPreview(m.store)
		}
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab: // newer sessions sit below
		if p.idx > 0 {
			p.idx--
			p.loadPreview(m.store)
		}
	case tea.KeyEnter:
		id := p.metas[p.idx].ID
		m.picker = nil
		if err := m.resume(id); err != nil {
			m.append(errStyle.Render(err.Error()))
		}
	}
	return m, nil
}

func (p *picker) loadPreview(store *session.Store) {
	id := p.metas[p.idx].ID
	if _, ok := p.previews[id]; !ok {
		u, a := store.LastExchange(id)
		p.previews[id] = [2]string{u, a}
	}
}

// openPicker starts the /resume browser on recent sessions.
func (m *model) openPicker() {
	if m.store == nil {
		m.append(errStyle.Render("session store unavailable"))
		return
	}
	metas, err := m.store.Recent(50)
	if err != nil {
		m.append(errStyle.Render(err.Error()))
		return
	}
	if len(metas) == 0 {
		m.append(dimStyle.Render("(no previous sessions)"))
		return
	}
	m.picker = &picker{metas: metas, previews: map[string][2]string{}}
	m.picker.loadPreview(m.store)
}

// openMenu computes candidates for the current input (tab pressed).
func (m *model) openMenu() {
	head, cands := completions(m.input.Value(), m.modelCands(), m.providerCands(), m.skillCands(), effortCandsFor(m.effortsFor()))
	switch len(cands) {
	case 0:
	case 1:
		m.menu = &menu{head: head, cands: cands}
		m.accept()
	default:
		m.menu = &menu{head: head, cands: cands}
	}
}

// refreshMenu keeps a live dropdown open while typing a slash command, an
// @file mention, or a $skill, re-filtering on every keystroke; otherwise closes it.
func (m *model) refreshMenu() {
	val := m.input.Value()
	token := val[strings.LastIndexByte(val, ' ')+1:]
	if strings.HasPrefix(val, "/") || strings.HasPrefix(token, "@") || strings.HasPrefix(token, "$") {
		head, cands := completions(val, m.modelCands(), m.providerCands(), m.skillCands(), effortCandsFor(m.effortsFor()))
		if len(cands) > 0 {
			idx := 0
			if m.menu != nil && m.menu.idx < len(cands) {
				idx = m.menu.idx
			}
			m.menu = &menu{head: head, cands: cands, idx: idx}
			return
		}
	}
	m.menu = nil
}

// accept applies the selected candidate. Returns false if the input already
// equals it (nothing to complete).
func (m *model) accept() bool {
	c := m.menu.cands[m.menu.idx]
	v := m.menu.head + c.Text
	if !strings.HasSuffix(c.Text, "/") { // directories stay open for deeper completion
		v += " "
	}
	if strings.TrimRight(m.input.Value(), " ") == strings.TrimRight(v, " ") {
		m.menu = nil
		return false
	}
	m.input.SetValue(v)
	m.menu = nil
	m.refreshMenu()
	return true
}

func (m *model) modelCands() []cand {
	out := make([]cand, 0, len(m.cfg.Models))
	for name, mdl := range m.cfg.Models {
		out = append(out, cand{name, "via " + strings.Join(mdl.Providers, ", ")})
	}
	return out
}

func (m *model) providerCands() []cand {
	out := make([]cand, 0, len(m.cfg.Providers))
	for name, p := range m.cfg.Providers {
		out = append(out, cand{name, p.BaseURL})
	}
	return out
}

// skillCands rescans skill dirs so newly added skills appear immediately.
// ponytail: full rescan per keystroke; cache with a TTL if a huge skill tree drags
func (m *model) skillCands() []cand {
	sk := skills.Scan(skills.DefaultDirs()...)
	out := make([]cand, 0, len(sk))
	for _, s := range sk {
		d := s.Description
		if len(d) > 80 {
			d = d[:80] + "…"
		}
		out = append(out, cand{"$" + s.Name, d})
	}
	return out
}

// prepareTurn refreshes the system prompt's skills block (so new skills load
// without a restart) and expands $skill / @file tokens in the input.
func (m *model) prepareTurn(text string) string {
	sk := skills.Scan(skills.DefaultDirs()...)
	m.agent.Messages[0].Content = m.sysPrompt + skills.PromptBlock(sk)
	return expandMentions(expandSkills(text, sk))
}

// appendAssistant writes assistant text into the transcript, prefixing the
// first line of each segment.
func (m *model) appendAssistant(s string) {
	if !m.inMsg {
		s = botStyle.Render("● ") + s
		m.inMsg = true
	}
	m.append(wrap(s, m.width))
}

// flushCurrent moves any in-flight partial line into the transcript and ends
// the current assistant segment.
func (m *model) flushCurrent() {
	cur := strings.TrimRight(m.current, " \n")
	m.current = ""
	if cur != "" {
		m.appendAssistant(cur)
	}
	m.inMsg = false
}

func (m *model) submit(text string) (tea.Model, tea.Cmd) {
	m.busy = true
	prepared := m.prepareTurn(text)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	p := m.prog

	// Coalesce streaming deltas (~25fps) so each SSE chunk doesn't cost a
	// full Update/View cycle.
	var mu sync.Mutex
	var pend string
	var timer *time.Timer
	flush := func() {
		mu.Lock()
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		out := pend
		pend = ""
		mu.Unlock()
		if out != "" {
			p.Send(textMsg(out))
		}
	}
	onText := func(d string) {
		mu.Lock()
		pend += d
		if timer == nil {
			timer = time.AfterFunc(40*time.Millisecond, flush)
		}
		mu.Unlock()
	}

	go func() {
		final, err := m.agent.Turn(ctx, prepared, agent.Events{
			OnText: onText,
			OnToolStart: func(n, a string) {
				flush()
				p.Send(toolStartMsg{n, a})
			},
			OnToolEnd: func(n, r string) { p.Send(toolEndMsg{n, r}) },
			OnSteer: func(s string) {
				flush()
				p.Send(steeredMsg(s))
			},
		})
		flush()
		p.Send(turnDoneMsg{final: final, err: err})
	}()
	m.append(youStyle.Render("❯ ") + text)
	return m, m.spin.Tick
}

func (m *model) command(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	switch fields[0] {
	case "/quit", "/exit", "/q":
		return m, tea.Quit
	case "/clear":
		m.agent.Messages = m.agent.Messages[:1] // keep system prompt
		m.blocks = nil
		m.setGoal("")    // clear before detaching so the old session's goal is dropped too
		m.sessionID = "" // next turn starts a fresh session
		m.saved = 1
		m.append(dimStyle.Render("(conversation cleared)"))
	case "/effort":
		levels := m.effortsFor()
		if len(fields) > 1 {
			lv, ok := parseEffort(levels, fields[1])
			if !ok {
				names := make([]string, len(levels))
				for i, e := range levels {
					names[i] = effortLabel(e)
				}
				m.append(errStyle.Render("unknown effort level; " + m.agent.Model + " supports: " + strings.Join(names, ", ")))
				break
			}
			m.setEffort(lv)
		} else {
			m.setEffort(nextEffort(levels, m.agent.Effort))
		}
		m.append(dimStyle.Render("⚡ effort: " + effortLabel(m.agent.Effort)))
	case "/goal":
		switch {
		case len(fields) == 1:
			if m.goal == "" {
				m.append(dimStyle.Render("no goal set — /goal <text> to set one"))
			} else {
				m.append(dimStyle.Render(fmt.Sprintf("◎ goal (round %d/%d): %s", m.goalRounds, maxGoalRounds, m.goal)))
			}
		case fields[1] == "clear":
			m.setGoal("")
			m.append(dimStyle.Render("(goal cleared)"))
		case fields[1] == "resume":
			if m.goal == "" {
				m.append(errStyle.Render("no goal to resume — set one with /goal <text>"))
				break
			}
			m.goalRounds = 0
			m.append(dimStyle.Render("◎ resuming goal: " + m.goal))
			return m.submit(goalContinuePrompt(m.goal))
		default:
			goal := strings.TrimSpace(strings.TrimPrefix(text, "/goal"))
			m.setGoal(goal)
			m.append(dimStyle.Render("◎ goal set: " + goal))
			return m.submit(goal)
		}
	case "/resume":
		if len(fields) > 1 {
			if err := m.resume(fields[1]); err != nil {
				m.append(errStyle.Render(err.Error()))
			}
			break
		}
		m.openPicker()
	case "/help":
		m.append(dimStyle.Render(
			"/model <name> [provider] — switch model\n/resume [id] — resume a previous session\n/goal <text> — keep working until the goal is met (resume | clear)\n/clear — reset conversation\n/quit — exit\ntab — complete · PgUp/PgDn — scroll · ctrl+c — interrupt / quit"))
	case "/model":
		if len(fields) < 2 {
			m.openModelPicker()
			break
		}
		prov := ""
		if len(fields) > 2 {
			prov = fields[2]
		}
		m.switchModel(fields[1], prov)
	default:
		m.append(errStyle.Render("unknown command " + fields[0]))
	}
	return m, nil
}

const menuRows = 8

func (m *model) currentView() string {
	s := m.current
	if !m.inMsg {
		s = botStyle.Render("● ") + s
	}
	return wrap(s, m.width)
}

func (m *model) View() string {
	var b strings.Builder
	left := fmt.Sprintf(" loopy · %s @ %s · %s", m.modelName, m.provName, cwd())
	if m.goal != "" {
		left += " · ◎ " + truncLine(m.goal, 40)
	}
	if !m.follow {
		left += fmt.Sprintf(" · ↑ %d%%", int(m.vp.ScrollPercent()*100))
	}
	// right-aligned clickable effort control
	right := "⚡ " + effortLabel(m.agent.Effort) + " "
	m.effortX = max(m.width-len(right)-1, 0) // ⚡ renders 2 cells wide
	left = truncLine(left, max(m.width-len(right)-2, 0))
	pad := max(m.width-len(left)-len(right)-1, 1)
	b.WriteString(dimStyle.Render(left+strings.Repeat(" ", pad)) + toolStyle.Render(right) + "\n")
	if m.picker != nil {
		b.WriteString(m.pickerView())
		return b.String()
	}
	if m.mpicker != nil {
		b.WriteString(m.modelPickerView())
		return b.String()
	}
	b.WriteString(dimStyle.Render(truncLine(" / commands · ctrl+p palette · tab completes · ↑/↓ history · mouse scroll · ctrl+c interrupt/quit", m.width)) + "\n\n")
	b.WriteString(m.vp.View() + "\n")
	if m.current != "" {
		b.WriteString(m.currentView() + "\n")
	}
	if m.busy {
		hint := " thinking… (enter queues · ctrl+c ctrl+c interrupts)"
		if m.interrupt1 {
			hint = " thinking… (ctrl+c again to interrupt)"
		}
		b.WriteString(m.spin.View() + dimStyle.Render(hint) + "\n")
	}
	if len(m.queue) > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf(" ⧗ queued (%d) — enter on empty input to steer into this turn", len(m.queue))) + "\n")
		for _, q := range m.queue {
			b.WriteString(truncLine(youStyle.Render(" ❯ ")+q, m.width) + "\n")
		}
	}
	b.WriteString(m.input.View())
	if m.menu != nil {
		b.WriteString("\n" + m.menuView())
	}
	b.WriteString("\n") // bottom padding
	return b.String()
}

const previewLines = 5

// pickerView renders the /resume browser: oldest at top, newest at bottom,
// the selected session expanded with previews of its last exchange.
func (m *model) pickerView() string {
	p := m.picker
	rows := []string{}
	expanded := 3 + 2*previewLines // meta + previews
	// how many collapsed rows fit alongside the expanded selection + footer
	budget := max(m.height-2-expanded-1, 2)
	lo := max(p.idx-budget/2, 0)
	hi := min(lo+budget+1, len(p.metas))

	for i := hi - 1; i >= lo; i-- { // metas is newest-first; render oldest on top
		meta := p.metas[i]
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		line := fmt.Sprintf("%s  %s · %s · %s @ %s", meta.ID, title, ago(meta.UpdatedAt), meta.Model, meta.Provider)
		if i != p.idx {
			rows = append(rows, truncLine("    "+line, m.width))
			continue
		}
		rows = append(rows, botStyle.Render(truncLine("  → "+line, m.width)))
		prev := p.previews[meta.ID]
		rows = append(rows, previewBlock(youStyle.Render("❯ "), prev[0], m.width)...)
		rows = append(rows, previewBlock(botStyle.Render("● "), prev[1], m.width)...)
	}
	rows = append(rows, dimStyle.Render(fmt.Sprintf("  (%d/%d) ↑ older · ↓ newer · enter resume · esc cancel", p.idx+1, len(p.metas))))
	// pad so the footer stays at the bottom of the screen
	for len(rows) < m.height-1 {
		rows = append(rows, "")
	}
	return strings.Join(rows, "\n")
}

// previewBlock renders up to previewLines lines of a message under a prefix.
func previewBlock(prefix, text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	out := []string{"      " + prefix + truncLine(lines[0], max(width-8, 8))}
	for _, l := range lines[1:] {
		if len(out) == previewLines {
			out = append(out, dimStyle.Render(fmt.Sprintf("        … +%d lines", len(lines)-previewLines)))
			break
		}
		out = append(out, dimStyle.Render("        "+truncLine(l, max(width-8, 8))))
	}
	return out
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m *model) menuView() string {
	// window of menuRows candidates around the selection
	start := 0
	if m.menu.idx >= menuRows {
		start = m.menu.idx - menuRows + 1
	}
	end := min(start+menuRows, len(m.menu.cands))

	nameW := 0
	for _, c := range m.menu.cands[start:end] {
		nameW = max(nameW, len(c.Text))
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		c := m.menu.cands[i]
		line := fmt.Sprintf("%-*s  ", nameW, c.Text)
		if i == m.menu.idx {
			b.WriteString(botStyle.Render("→ "+line) + dimStyle.Render(c.Desc))
		} else {
			b.WriteString("  " + line + dimStyle.Render(c.Desc))
		}
		b.WriteByte('\n')
	}
	b.WriteString(dimStyle.Render(fmt.Sprintf("  (%d/%d)", m.menu.idx+1, len(m.menu.cands))))
	return b.String()
}

func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

func truncLine(s string, width int) string {
	if width > 0 && len(s) > width {
		return s[:width-1] + "…"
	}
	return s
}
