# Agent UX batch: waits, subagent visibility, busy-state messaging, speed

Branch: TBD (likely split into several PRs)

Working file for a cluster of issues raised in one session (2026-08-05). Each
item has its own section with findings + proposed design. Nothing implemented
yet — awaiting user sign-off per item.

## Threads in flight

- [x] Research: wait/poll mechanisms — exo, codex, pi, opencode, claude-code (done)
- [ ] Research: hermes + oh-my-pi wait/poll (subagent sub-4 still running)
- [ ] Research: opencode speed secrets (subagent sub-7 still running)
- [x] TUI investigation: subagent view has no history (root cause found)
- [~] TUI investigation: spawn visibility lag + busy-state messaging (partial)

---

## 1. `wait` tool — kill `sleep`+poll turns (the original request)

**Problem:** model runs `sleep 60 && gh pr checks` loops; each poll costs a
full LLM turn (~28M tok observed on an 8-min CI watch).

**Cross-harness research summary** (full reports in session history; distilled
in `.ai-docs/plans/agent-ux-batch/wait-tool-research.md` — TODO write):

- **codex**: `exec_command(cmd, yield_time_ms)` returns a session handle;
  `write_stdin(session_id, "", yield=up to 5min)` is a harness-owned long-poll.
  Activity watch channel wakes blocked tools on new user input.
- **opencode**: background task → `Deferred` await → `notify()` injects
  synthetic `<task state>` message into parent via `prompt()` + coalesced
  wake/re-drain. Tool prompt literally says "DO NOT sleep, poll… you will be
  notified". No generic wait tool.
- **pi**: nothing. (Has unwired `DeferredHandle` design for future.)
- **exo**: external scheduler daemon injects a wakeup User message starting a
  fresh turn. Grid-anchored, durable, crash-recoverable.
- **claude-code**: background bash + "remind to continue" — completion injected
  once as system/user message; O(1) messages, never n polls.

**whip's existing pieces:** `Agent.Steer` (drained at loop boundaries),
`taskRegistry` + `Done` channels + `OnChange`, TUI 5s tick, `submitTurn(text,
false)` for machine turns, bash timeout escalation already exists.

**Proposed design (candidate, needs sign-off):**
- New tool `wait` (internal/tools/wait.go): args `command`, `until`
  (optional regex; absent = exit 0), `interval` (default 10s), `timeout`
  (default 10m, hard cap 1h).
- Poller goroutine loops command at interval — zero LLM turns.
- Delivery: if agent busy → `Steer("[wait …] …")` drained at next boundary;
  if idle → TUI wake → `submitTurn(msg, false)` (opencode/exo wake pattern).
  Needs an idle-wake hook on Agent (nil in headless/tests).
- System-prompt nudge: "prefer `wait` over sleep loops; you will be notified".
- Anti-patterns to refuse: no polling a task whose `Done` channel we already
  have (claude-code lesson 6); steer once, on state transition only.

## 2. Subagent spawn visibility lag

**Symptom:** model-spawned background subagents take a long time to appear in
the dock below the input box.

**Findings so far:**
- `/subagent` command path (taskmodel.go:128-129) appends a confirmation line
  immediately — no lag there.
- Model-driven path: `subagent(background:true)` tool → `StartBackground`
  (agent.go) → `OnChange` → detached `prog.Send(taskUpdateMsg{})` → dock
  redraw. Design looks immediate.
- Suspects: (a) tool runs in the parallel batch — `OnChange` fires only after
  the tool result, plus the turn keeps streaming; (b) `sendTaskMsg`/
  `OnChange` use detached goroutine per send (`go p.Send`) — a backed-up UI
  queue delays arrival; (c) user perception may actually be "no feedback until
  the whole turn ends" because the ⚙ badge/dock only stands out when idle.
- TODO: verify what actually shows between tool call and settle: does
  OnToolStart render a row for the subagent tool call? (It should — but user
  says "eventually spawned", implying not.)
- Next step: reproduce with a headless test asserting dock content right after
  the subagent tool call completes.

## 3. Busy-state messaging while waiting on subagents

**Symptom:** when the main agent is blocked on foreground subagents, user
messages queue behind the whole turn (tui.go:2027: queue drains only when turn
ends); user wants typing to feel live.

**Proposal (needs sign-off):** new steer-vs-queue routing rule —
if busy AND no main-thread tool running yet in this turn (i.e. model is
"thinking" or in-flight API call; hard to observe cheaply) OR the current tool
batch is entirely subagent waits → submit as steer (inject at loop boundary)
instead of queue. Otherwise keep queue semantics. Alternative simpler take:
always steer when busy and the turn's only in-flight work is foreground
subagent tools; never cancel (keep the current empty-enter=cancel special case
intact? that one is explicit user intent).

Open question: how does Turn know "only subagents in flight"? The TUI can't see
the tool batch directly — needs an agent-side signal (e.g. atomic counter of
running non-subagent tools, or an `Agent.BusyKind()` snapshot).

## 4. Subagent detail view shows only post-attach events

**Root cause (confirmed):** `openTask` (internal/tui/tasks.go:165) seeds the
pane with prompt only; live events are broadcast to subscribers and discarded —
no journal. Subscribe gets only future events. Settled tasks show only the
final Report, no transcript.

**Fix (needs sign-off):** bounded per-task event journal in taskRegistry
(e.g. `journal []taskEvent` capped ~100KB, appended in `emitter()` before
broadcast). `openTask` replays journal into `tv.buf` before subscribing.
Journal is live-only (cleared with ClearSettled); persistence covered by #5.

## 5. Subagent session persistence (~/.whip, with attribution)

**Ask:** subagent sessions persist like regular sessions, with attribution
(parent session id, task id, spawned-by).

**Current state:** registry persists only task metadata row (ID, description,
prompt, status, report) via `OnRecord` → `SaveTask`; the subagent's message
list lives in the retained `*Agent` in memory only, dies with the process.

**Proposal (needs sign-off):**
- session.Store: subagent sessions as rows in the sessions table with
  `parent_session_id`, `task_id`, `kind=subagent` columns (or a
  `subagent_sessions` table — prefer extending sessions, one table).
- Where messages get saved: subagent Turn appends to `sub.Messages`; hook
  persistence the way main agent does (TUI persists main session in turnDone;
  subagent worker has no TUI — persist on settle in the StartBackground
  goroutine, and incrementally? incremental = Save(id, from, msgs) per turn
  boundary like main).
- `whip resume` / session list: show subagent sessions nested/attributed
  ("↳ subagent of <session> · <task id>").
- Interaction with #4: persisted subagent transcript could replace/supplement
  the live journal for restored tasks — `--resume` re-renders history.
  Restored task detail view = read-only replay of persisted messages.
- Resume correctness test required (skill rule): kill mid-subagent, resume,
  open task, see full transcript.

## 6. opencode "feels faster" — research pending

Subagent sub-7 investigating: parallel tool dispatch, streaming pipeline,
prompt caching (`cache_control`), request pipelining. Await report before
designing.

---

## Docs/plan bookkeeping

- Skill: new-feature-development — each item above gets its own plan dir (or a
  checkbox here) before implementation; `docs/features.md` section + roadmap
  checkbox per shipped item.
- Session memory entries mirror this file's index.
