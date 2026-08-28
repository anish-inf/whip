# TUI issues — root causes and candidate fixes (2026-08-05)

All three found by reading internal/tui + internal/agent/background.go.

## A. Subagent detail view shows only post-attach events (CONFIRMED root cause)

`openTask` (internal/tui/tasks.go:165-201):
- Seeds `tv.buf` with header + prompt only.
- Running task → `Tasks().Subscribe(id, Events{...})` — receives only events
  from that point on. Registry `emitter()` (background.go:305) broadcasts to
  current subscribers and DROPS everything else. No journal anywhere.
- Settled task → prints only the final `Report` string. The full transcript
  (text, tool calls, steers) is gone unless a view was open the whole run.

User impact: "I can't see the completed result of the subagent when I open it"
— worse than annoying: the dock opens feel broken.

Candidate fix: bounded per-task event journal.
- `taskRegistry`: `journal map[string][]journaledEvent` (or field on
  BackgroundTask — but tasks are snapshot-copied by List/Get, so a registry
  map is safer; or store `*[]event` pointer on the task).
- `emitter()` appends each event (kind, text/args) before broadcast; cap
  total bytes (~64-100KB per task, drop-oldest with a "[earlier output
  dropped]" marker line).
- `openTask` replays journal into `tv.buf` (formatted exactly like the live
  handlers do), then subscribes — atomically with the replay under the
  registry lock so no event is missed or duplicated between replay and
  subscribe. Cleanest: a `SubscribeWithJournal(id, ev) ([]event, bool)` that
  returns the journal snapshot and registers the subscriber in one lock hold.
- Journal lives only while the task is registered; ClearSettled drops it.
  Persistence across process restarts is thread E below, not the journal.
- Test: spawn task in headless loop test, let it emit, THEN openTask, assert
  buf contains pre-open events. Race-clean.

## B. Spawn visibility lag (PARTIAL — needs reproduction)

Facts:
- `/subagent` hand-spawn (taskmodel.go:128-129) prints a confirmation line
  synchronously — no lag on that path.
- Model-spawned: subagent tool with `background:true` → `StartBackground`
  → registry `OnChange` → wireTasks (tui.go:2826-2830) does `go
  m.prog.Send(taskUpdateMsg{})` → taskUpdateMsg case → dock redraw.
- The design looks immediate. Suspects, in order:
  1. The dock only renders below the INPUT box — during a busy turn the user's
     eyes are on the streaming transcript; if the subagent tool-call row in
     the transcript doesn't render until tool END, there's genuinely no
     visible signal mid-turn. Need to check: does OnToolStart fire/render for
     the subagent tool call itself? (grep toolrow.go / toolcall_stream_test.go
     handling of OnToolStart vs OnToolEnd.)
  2. `go p.Send(msg)` detached goroutine per event — under a backed-up UI
     queue, ordering and latency are whatever the scheduler gives. Fine for
     coalescing, but combined with (1) it can look like "eventually spawned".
  3. `dockTasks()` filters `Restored` and ages out settled tasks after
     `dockSettledGrace` (1 min) — not the spawn path, but worth knowing.

Next step: headless test driving a turn with a fake provider that calls
subagent(background:true); assert dock shows the task row before settle.

## C. Busy-state messaging while waiting on subagents (design needed)

Current behavior (tui.go):
- Busy + enter with text → `m.queue = append(m.queue, text)` (tui.go:2591),
  dim "queued" rows render (chrome at :1548).
- Queue drains ONLY at turnDone (tui.go:2027-2033), one message per new turn.
- Empty enter while busy = intentional cancel-then-drain.
- `Agent.Steer` exists and drains at loop boundaries inside the running turn
  (agent.go:408-421) — but the TUI never uses it for typed user input.

User want: when the main agent is just WAITING on (foreground) subagents,
typing should steer into the live turn, not queue behind it. "It's not
technically an interruption."

Design constraint: the TUI can't see what the in-flight tool batch is. Needs
an agent-side observable, e.g.:
- `Agent.InFlightTools() []string` snapshot (names of currently-running tool
  calls, maintained in runTools), or
- `Agent.WaitingOnSubagents() bool` (in-flight set ⊆ {subagent}).

Then submit() while busy: if `WaitingOnSubagents()` → `agent.Steer(text)` +
echo locally as a steer row; else → queue (current behavior). Keep
empty-enter cancel as-is (explicit user intent).

Open questions for sign-off:
- Should steered-while-waiting also apply when the model is mid-generation
  with no tools in flight? (Codex does this: activity channel interrupts
  sleeps. More aggressive; changes turn semantics more.)
- UI affordance: how does the user know typing will steer vs queue? Maybe
  input placeholder changes ("steer this turn…" vs "queued").

## D. Subagent session persistence to ~/.whip (feature request)

Ask: subagent sessions persist like regular sessions, with attribution.

Current state:
- Only task metadata persists: OnRecord → `st.SaveTask(sessionID,
  session.Task{ID, Description, Prompt, Status, Report, StartedAt,
  EndedAt})` (wireTasks, tui.go:2807-2820). The subagent's MESSAGE LIST lives
  only in the retained `*Agent` (BackgroundTask.sub) — dies with the process.
- `--resume` restores task rows as settled-with-history (`Restored: true`),
  no transcript.

Design sketch (needs sign-off):
- Extend session store: subagent sessions as first-class session rows with
  attribution columns: `parent_session_id`, `task_id`, `kind='subagent'`
  (prefer extending the sessions table over a parallel table — one store,
  attribution is just columns).
- Save path: subagent Turn appends to `sub.Messages`; persist incrementally
  at loop boundaries like the main session does (`Save(id, from, msgs)`), or
  minimally once at settle. Incremental is better for crash-mid-run but
  settle-only is the ponytail version. Decide.
- `--resume`/session list UI: subagent sessions listed nested under parent
  ("↳ subagent <task-id> of <session>"), openable read-only.
- Synergy with (A): restored tasks' detail view replays the persisted
  transcript instead of showing only the report — makes Restored tasks
  useful. The live journal stays live-only.
- Retention: subagent sessions accumulate fast (research fan-outs!) — need a
  policy (delete with parent? cap? age out?) — ask user.
- Resume test required per skill: kill mid-subagent, resume, open task, see
  full transcript.

## E. Cross-cutting: opencode's busy-drain lesson for C

opencode re-checks `hasPending("steer")` after EVERY LLM turn inside a drain
(runner/llm.ts:408) and coalesces wakes (pendingWake). whip's Steer already
drains per loop boundary — equivalent. The missing piece is purely TUI-side
routing of typed input + the agent-side in-flight observability.

## B-update: spawn-lag verification result (done)

Traced the full chain:
- llm.Stream fires OnToolCall per tool-call delta (openai.go:744) → TUI
  renders "⋯ subagent <desc>" queued row MID-STREAM (tui.go:1752) →
  toolStartMsg swaps it to a live running row (tui.go:1779) →
  StartBackground fires OnChange → dock row + ⚙ badge (tui.go:2826).
- Conclusion: transcript feedback is immediate by design. The dock row,
  however, appears only at TOOL COMPLETION (OnChange is inside
  StartBackground), not when the call streams in — under a parallel batch that
  includes a slow tool (bash holding the global lock, or another subagent's
  worktree `git worktree add`), the subagent tool's Run can sit blocked
  post-stream, and the dock stays empty the whole time.
- Secondary suspect inside the tool: worktree provisioning runs
  `git worktree add` synchronously before StartBackground (subagent.go:110) —
  slow on big repos.
- Candidate fixes: (1) fire a cheap "spawning" signal at tool START for
  subagent calls (or render queued toolCall rows into the dock too — probably
  wrong surface); (2) provision worktrees asynchronously so OnChange isn't
  delayed; (3) cheapest: accept transcript-row-only feedback and make the ⚙
  badge count "queued" subagent calls too. Decide with user; the felt lag may
  also just have been the previous session's load.
