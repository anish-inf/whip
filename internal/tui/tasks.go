// tasks.go: the persistent background-subagent area and the per-task detail
// view.
//
// The dock is a strip rendered below the input box (above the status line)
// whenever background tasks exist — running or recently settled — so the user
// always knows how many subagents are in flight without running /subagents.
// ctrl+t focuses it, and so does ↓ on an empty input (the strip sits right
// under the cursor); ↑/↓ (or the mouse wheel over the strip) moves the
// selection, ↑ off the top row hands focus back to the input, enter opens the
// selected task's detail view, and esc backs out: detail → dock → main
// thread. The detail view is a scrollback pane filled from the
// task's live event stream (registry.Subscribe) while it runs, and from the
// stored Report once it settles.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/context-labs/whip/internal/agent"
)

// taskEventMsg is one live event from an opened background task (OnText /
// OnToolStart / OnToolEnd fire from the subagent's worker goroutine; prog.Send
// funnels them onto the UI thread like every other stream).
type taskEventMsg struct {
	id   string
	kind int // 0 = text, 1 = tool start, 2 = tool end, 3 = steer injected, 4 = follow-up turn settled
	s    string
	s2   string // tool args (start) or result (end)
}

// sendTaskMsg hands a task event to the UI without ever blocking the subagent
// worker goroutine: prog.Send parks while the UI event queue is backed up, so
// the send is detached into its own goroutine. Program.Send is safe for
// concurrent use (it just selects on the program's msg channel), and if the
// program exits first, bubbletea unblocks every pending Send — no leak. The
// pane resyncs from the task's Report on the next paint, so a reordered or
// lost interim frame is cosmetic; the worker must never stall on the UI.
func sendTaskMsg(p *tea.Program, msg taskEventMsg) {
	if p == nil {
		return // headless tests
	}
	go p.Send(msg)
}

// taskView is the open per-task pane: the live transcript of one background
// subagent (or its stored report once settled), plus a chat input — a task IS
// a session: while it runs, typing steers it; once it settles, typing runs
// follow-up turns on its retained context. Restored tasks are read-only.
type taskView struct {
	id    string
	vp    viewport.Model
	buf   strings.Builder // full transcript text; vp shows a window into it
	live  bool            // subscribed to the task's event stream
	input textinput.Model
	busy  bool // a follow-up turn is in flight on the settled subagent
	// followCancel cancels the in-flight follow-up turn (ctrl+x). Set with
	// busy on the UI goroutine; the worker only reads its copy.
	followCancel context.CancelFunc
}

// tasksDockHeight is the maximum number of screen rows the dock strip
// occupies (hint row + task rows); the strip scrolls if there are more tasks.
const tasksDockHeight = 6

// dockSettledGrace is how long a settled task stays in the dock after
// finishing — long enough to notice the ✓ and open the report, then the
// strip cleans itself. Restored tasks (--resume history) never show: their
// subagents died with the previous process. /tasks lists everything.
const dockSettledGrace = time.Minute

// dockTasks returns the dock's tasks — running ones plus those settled
// within dockSettledGrace, never restored ones — newest first. Bare test
// models have no agent; the dock is simply empty.
func (m *model) dockTasks() []agent.BackgroundTask {
	if m.agent == nil {
		return nil
	}
	var out []agent.BackgroundTask
	for _, t := range m.agent.Tasks().List() {
		if t.Restored {
			continue // resume history belongs in /tasks, not the dock
		}
		// running always shows; zero EndedAt (never settled) sorts with them
		if t.Status == agent.TaskRunning || time.Since(t.EndedAt) < dockSettledGrace {
			out = append(out, t)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// clampTaskSel keeps the dock selection inside the current task list.
func (m *model) clampTaskSel() {
	if n := len(m.dockTasks()); m.taskSel >= n {
		m.taskSel = max(n-1, 0)
	}
}

// tasksDock renders the persistent strip: one row per task with a live
// status icon, plus a hint row when the dock is focused.
func (m *model) tasksDock() string {
	tasks := m.dockTasks()
	if len(tasks) == 0 {
		return ""
	}
	m.clampTaskSel()

	rows := make([]string, 0, len(tasks)+2)
	if m.tasksFocus {
		rows = append(rows, dimStyle.Render(" ⚙ subagents — ↑/↓ select (↑ past top: back to input) · enter open"))
	}

	budget := tasksDockHeight - len(rows)
	if len(tasks) > budget { // reserve a row for the "+N more" counter
		budget--
	}
	lo := 0
	if m.tasksFocus && m.taskSel >= budget {
		lo = m.taskSel - budget + 1 // keep the selection visible
	}
	hi := min(lo+budget, len(tasks))

	for i := lo; i < hi; i++ {
		t := tasks[i]
		icon := toolStyle.Render("⏳")
		switch t.Status {
		case agent.TaskDone:
			icon = "✓"
		case agent.TaskError, agent.TaskCancelled:
			icon = errStyle.Render("✗")
		}
		line := fmt.Sprintf("%s %s  %s", icon, t.ID, truncLine(t.Description, max(m.width-24, 8)))
		var meta string
		if t.Status == agent.TaskRunning {
			meta = fmt.Sprintf("  %ds", int(time.Since(t.StartedAt).Seconds()))
		} else {
			meta = "  " + string(t.Status)
		}
		switch {
		case m.tasksFocus && i == m.taskSel:
			line = botStyle.Render(" → "+line) + toolStyle.Render(meta)
		case t.Status == agent.TaskRunning:
			line = "   " + toolStyle.Render(line) + dimStyle.Render(meta)
		default:
			line = "   " + line + dimStyle.Render(meta)
		}
		rows = append(rows, line)
	}
	if more := len(tasks) - hi; more > 0 {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("   … +%d more (ctrl+t to browse)", more)))
	}
	return strings.Join(rows, "\n")
}

// openTask opens the detail view for one task: a scrollback pane seeded with
// the prompt, then the live event stream while the task runs, or the stored
// report once it has settled.
func (m *model) openTask(id string) {
	t, ok := m.agent.Tasks().Get(id)
	if !ok {
		return
	}
	tv := &taskView{id: id, live: t.Status == agent.TaskRunning}
	tv.input = textinput.New()
	tv.input.Prompt = youStyle.Render("› ")
	tv.input.Placeholder = "message this subagent (enter to send)"
	tv.input.Focus()
	fmt.Fprintf(&tv.buf, "%s %s  %s\n\n%s %s\n",
		toolStyle.Render("⚙"), t.ID, t.Description,
		youStyle.Render("prompt:"), t.Prompt)
	if t.Status == agent.TaskRunning {
		fmt.Fprintf(&tv.buf, "\n%s\n", dimStyle.Render("  running…"))
		p := m.prog
		m.agent.Tasks().Subscribe(id, agent.Events{
			OnText: func(s string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 0, s: s})
			},
			OnToolStart: func(_, n, a string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 1, s: n, s2: a})
			},
			OnToolEnd: func(_, n, r string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 2, s: n, s2: r})
			},
			OnSteer: func(s string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 3, s: s})
			},
		})
	} else {
		fmt.Fprintf(&tv.buf, "\n%s %s\n", toolStyle.Render(string(t.Status)+":"), t.Report)
	}
	m.taskVP = tv
	m.refreshTaskVP()
}

// taskChatable reports whether the open task can receive messages: restored
// tasks have no live subagent (it died with the previous process).
func (m *model) taskChatable(tv *taskView) bool {
	t, ok := m.agent.Tasks().Get(tv.id)
	return ok && !t.Restored
}

// taskSend routes one typed message to the open task's subagent: steering
// while it runs, a follow-up turn on its retained context once it settled.
func (m *model) taskSend(tv *taskView, text string) {
	t, ok := m.agent.Tasks().Get(tv.id)
	if !ok {
		return
	}
	tv.input.SetValue("")
	switch {
	case t.Status == agent.TaskRunning:
		if err := m.agent.SteerTask(tv.id, text); err != nil {
			fmt.Fprintf(&tv.buf, "\n%s\n", errStyle.Render(err.Error()))
			break
		}
		fmt.Fprintf(&tv.buf, "\n%s %s\n", youStyle.Render("you (steer):"), text)
	case tv.busy:
		fmt.Fprintf(&tv.buf, "\n%s\n", dimStyle.Render("(still replying — wait for the current reply)"))
	default:
		fmt.Fprintf(&tv.buf, "\n%s %s\n\n", youStyle.Render("you:"), text)
		ctx, cancel := context.WithCancel(context.Background())
		tv.busy, tv.followCancel = true, cancel
		p, id := m.prog, tv.id
		ev := agent.Events{
			OnText: func(s string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 0, s: s})
			},
			OnToolStart: func(_, n, a string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 1, s: n, s2: a})
			},
			OnToolEnd: func(_, n, r string) {
				sendTaskMsg(p, taskEventMsg{id: id, kind: 2, s: n, s2: r})
			},
		}
		ag := m.agent
		go func() {
			defer cancel()
			_, err := ag.FollowupTask(ctx, id, text, ev)
			e := ""
			if err != nil {
				e = err.Error()
			}
			sendTaskMsg(p, taskEventMsg{id: id, kind: 4, s: e})
		}()
	}
	m.refreshTaskVP()
}

// refreshTaskVP resizes the open task pane to the free screen area and
// reloads its content, following the tail while the task streams.
func (m *model) refreshTaskVP() {
	tv := m.taskVP
	if tv == nil {
		return
	}
	// 3 rows of chrome: the header, the chat input, and the footer hint
	tv.vp.Width, tv.vp.Height = m.width, max(m.height-3, 1)
	tv.input.Width = max(m.width-4, 8)
	atBottom := tv.vp.AtBottom()
	tv.vp.SetContent(tv.buf.String())
	if atBottom {
		tv.vp.GotoBottom()
	}
}

// taskViewKey handles input while a task detail view is open: typing goes to
// the chat input (enter sends — steer while running, follow-up turn once
// settled), ↑/↓/PgUp/PgDn scroll the pane, ctrl+x cancels the running task or
// the in-flight follow-up, esc backs out to the dock.
func (m *model) taskViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	tv := m.taskVP
	switch msg.Type {
	case tea.KeyEsc:
		m.taskVP = nil
		m.tasksFocus = true // land on the dock so ↑/↓ keep working; ↑ past the top returns to the input
		return m, nil
	case tea.KeyCtrlT:
		m.taskVP = nil
		m.tasksFocus = true
		return m, nil
	case tea.KeyCtrlX:
		if tv.busy && tv.followCancel != nil {
			tv.followCancel()
		} else {
			m.agent.Tasks().Cancel(tv.id)
		}
		return m, nil
	case tea.KeyEnter:
		if text := strings.TrimSpace(tv.input.Value()); text != "" && m.taskChatable(tv) {
			m.taskSend(tv, text)
		}
		return m, nil
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		tv.vp, cmd = tv.vp.Update(msg)
		return m, cmd
	}
	if !m.taskChatable(tv) { // restored: read-only, keys keep scrolling
		var cmd tea.Cmd
		tv.vp, cmd = tv.vp.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	tv.input, cmd = tv.input.Update(msg)
	return m, cmd
}

// taskViewView renders the open task pane full-screen: header, transcript,
// the chat input, and a footer hint (View's layout mirrors View's structure).
func (m *model) taskViewView() string {
	tv := m.taskVP
	t, ok := m.agent.Tasks().Get(tv.id)
	status := "running"
	if ok {
		status = string(t.Status)
	}
	if ok && t.Restored {
		status += ", restored"
	}
	if tv.busy {
		status += ", replying"
	}
	head := toolStyle.Render(fmt.Sprintf(" ⚙ %s — %s", tv.id, truncLine(t.Description, max(m.width-30, 8)))) +
		dimStyle.Render("  ("+status+")")
	in := " " + tv.input.View()
	hint := " esc back · ↑/↓ scroll · enter send (steers while running, chats after) · ctrl+x cancel"
	if ok && t.Restored {
		in = dimStyle.Render(" (restored from a previous session — read-only)")
		hint = " esc back · ↑/↓ scroll"
	}
	foot := dimStyle.Render(hint)
	return head + "\n" + sanitizeView(tv.vp.View()) + "\n" + in + "\n" + foot
}
