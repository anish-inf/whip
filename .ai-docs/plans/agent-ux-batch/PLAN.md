# Agent UX batch — execution plan

Signed off by user 2026-08-05 ("i need all these to be done"). Research and
root-cause work already live in this directory:

- `wait-tool-research.md` — 6-harness wait/poll research, distilled
- `opencode-speed.md` — why opencode feels fast (prompt caching + O(1) render)
- `tui-findings.md` — TUI root causes (journal, spawn lag, busy steering, persistence)

This file is the build plan. **Status: written, not started.** Work top-down;
each item is its own branch + PR. Update checkboxes as things land. `task
check` (gofmt -s + go vet + go test ./...) must pass per item; add `go test
-race ./...` for anything touching goroutines (items 1, 3, 4, 5, 6).

Standing rules (from the new-feature-development skill):
- Minimal is the product. `// ponytail:`-mark deliberate shortcuts.
- Errors are tool output (`"Error: <msg>"`), never loop-abort. Bound all output.
- Channels over locks; every goroutine has an owner and an exit.
- Docs are part of the diff: `docs/features.md` section + roadmap checkbox per
  shipped item; `docs/concurrency.md` if a new channel pattern is introduced.
- Session persistence correctness (`--resume`) is part of any feature that
  adds message-adjacent state.

---

## Item 1 — `wait` tool: kill sleep-poll turns

Branch: `feat/wait-tool`

**What it does:** the model calls `wait` with a shell command, a success
condition, an interval, and a timeout. A harness-owned poller goroutine loops
the command at the interval — zero LLM turns while waiting. On state change
(condition met / timeout / command error), exactly one message re-enters the
agent loop: steered at the next loop boundary if the agent is busy, or a
machine-authored wake turn if idle. Research: `wait-tool-research.md`.

**Goal:** replace `sleep 60 && gh pr checks` loops (observed: 28M tokens on an
8-minute CI watch) with one tool call + one completion message.

**Non-goals:** cron/scheduling (that's `/schedule`), auto-backgrounding of
arbitrary bash calls (later, Item 7), adaptive backoff ladder (ponytail later
if fixed intervals prove noisy), file watching.

### Design

New file `internal/tools/wait.go` (tool def + Run), new file
`internal/agent/wait.go` (poller registry + goroutine). Surfaces: tool,
agent-loop (idle-wake hook), system prompt (one line), TUI (installs the wake
hook). No config, no persistence (waits are live-only; a dead process's waits
die with it — same as background subagents pre-Item-5).

Tool def (appended in `tools.All()` — subagents get it too, which is fine):

```
wait(command, until?, interval?, timeout?)
  command:  shell command to run repeatedly (same bashrun path as bash tool)
  until:    optional regex; success = exit 0 AND (until absent OR stdout matches)
  interval: seconds between runs, default 10, min 2
  timeout:  seconds before giving up, default 600, max 3600
Returns immediately: "Waiting on <desc> (wait-3): checking every 10s, giving
up after 10m. You will be notified once — do NOT poll or sleep-check."
```

Registry (`internal/agent/wait.go`):

```go
type waitRegistry struct {
    mu    sync.Mutex
    waits map[string]*waitTask   // id "wait-<n>" (reuse taskSlug-style counter)
    OnWake func(text string)     // installed by TUI; nil headless
}
type waitTask struct {
    id string; command, until string; interval, timeout time.Duration
    cancel context.CancelFunc; done atomic.Bool
}
```

Poller goroutine per wait (owner = registry; exits on condition, timeout,
cancel, or agent teardown — plumbed via a registry-level context canceled in
a `Close()` the TUI/agent calls on session switch):

```
ticker at interval → run command via bashrun (bounded, same timeout per run
as interval) → evaluate condition:
  met      → deliver("[wait-3 done] <command> succeeded: <last ~2KB stdout>")
  timeout  → deliver("[wait-3 timeout] gave up after 10m; last output: …")
  run err  → count; 3 consecutive errors also delivers as failure (hermes
             3-strike lesson) so a broken command doesn't poll for an hour
deliver() = once (atomic.Bool CAS): if a turn is running → a.Steer(msg)
            (drained at next loop boundary, agent.go:408-421);
            else → OnWake(msg) → TUI submitTurn(msg, false) machine turn.
```

Busy-vs-idle: the registry needs to know. Cheapest correct source: the agent
already knows when a Turn is in flight — add `a.TurnRunning() bool` guarded by
the existing turn mutex (or an atomic set at Turn entry/exit). If that's
awkward, invert it: always `Steer`, and have the TUI's wake hook only used
when `!m.busy` — but Steer on an idle agent parks the message until the next
turn, so idle must go through OnWake. Verify TurnRunning() against
double-submit races (a wait firing during submitTurn's busy-set window):
mitigate by having OnWake re-check `m.busy` and fall back to queueing.

**Interruptible waits** (codex/oh-my-pi lesson): a new user message while a
wait is pending does NOT cancel the wait (waits are cheap, no tokens) — the
user can `/waits` + cancel explicitly. Cancel surface: extend `/subagents`
list or add `/waits` listing + `/waits cancel <id>` calling registry cancel.
Ponytail option: fold waits INTO the existing task dock as rows (they're
conceptually background tasks without an LLM) — check effort; if the dock
code generalizes cleanly, do that instead of a new /waits command.

System prompt: one line in the main prompt — "To wait for an external
condition (CI, deploys, servers), use the `wait` tool; never poll with
`sleep` loops — you will be notified once when the condition changes."

### Tests (internal/agent/wait_test.go, internal/tools test for def parsing)

- Poller fires deliver exactly once on condition-met (fake command via a
  temp script; short intervals).
- Timeout path delivers timeout message.
- 3-strike error path.
- Busy → Steer (message appears in agent Messages after loop boundary in the
  fake-provider loop test); idle → OnWake invoked.
- Cancel stops the ticker, no delivery.
- Race: `-race` over concurrent waits + cancel + settle.
- TUI headless: OnWake wiring → machine turn submitted when idle; steered
  path when busy.

### Docs

`docs/features.md` section (behavior → code → tests); roadmap checkbox if a
matching entry exists (grep "wait" docs/roadmap.md); one line in
`docs/tools.md` tool table.

---

## Item 2 — prompt caching in internal/llm

Branch: `feat/prompt-caching`

**What it does:** stamps provider cache hints so intra-turn tool round-trips
hit the provider's prompt-prefix cache instead of reprocessing the full
conversation. This is the measured root of opencode's speed
(`opencode-speed.md`): their `cache-policy.ts` stamps ephemeral breakpoints at
[last tool def, last system part, latest user message] and pins
`promptCacheKey = sessionID` for OpenAI-shaped APIs.

**Goal:** `Usage.Cached()` (already parsed! openai.go:372-379) becomes nonzero
on typical turns; TTFT on intra-turn calls drops.

**Non-goals:** response caching, provider-specificAnthropic-beta headers
beyond the standard fields, caching for one-shot `whip run` invocations
(harmless if it happens anyway).

### Design

whip speaks OpenAI chat-completions JSON (`internal/llm/openai.go`) to
multiple providers (OpenAI, OpenRouter, Moonshot/Kimi, Anthropic-compatible
gateways, inference.net). Two mechanisms, both additive JSON fields:

1. **`prompt_cache_key`** (OpenAI + compatible: openrouter, xai, azure):
   add `PromptCacheKey string \`json:"prompt_cache_key,omitempty"\`` to
   `Request` (openai.go:348). Value = session ID. Agent needs to know it:
   `Agent.SetSessionID` already exists (called from wireTasks) — thread it to
   a new `Client.SessionID` field or onto Request at call time. Client-level
   field is simplest: set when the TUI establishes/resumes a session;
   subagents get their own task-scoped key (`parent-session/task-id`) so
   their caches don't collide.

2. **Anthropic-style `cache_control` breakpoints**: gateways that accept
   Anthropic-shaped payloads want `cache_control: {type:"ephemeral"}` on
   message content parts. whip's `Message.Content` is a plain string for the
   OpenAI wire format; breakpoints mean emitting content as a parts array with
   the extra field for providers that understand it. This needs a
   provider-capability signal — read `docs/models-providers.md` and the
   config provider block before designing; likely a per-provider config flag
   or base-URL heuristic (ponytail: ship `prompt_cache_key` first, gate
   cache_control behind a provider config knob `"cache_control": true`).

Also required for hits (verify, fix if violated):
- **Byte-stable prefix**: system prompt must be identical across turns of a
  session (check: does anything append turn-varying content to the system
  message — timestamps, cwd churn, memory file mtime? Memory DOES change
  between turns when the model remembers things — that's mid-conversation
  prefix mutation; acceptable, cache resumes after the mutation point, but
  keep memory block at the END of the system prompt so the stable part stays
  prefix-maximal. Audit where the memory block is concatenated.)
- **Deterministic tool ordering**: `AllTools()` order must be stable
  (grep for map iteration feeding the tool list — MCP tools especially;
  sort if needed).

### Tests

- Unit: request JSON includes `prompt_cache_key` when session set; omitted
  when empty. cache_control emitted only when the provider flag is on.
- Loop test with fake provider: capture two consecutive requests, assert
  byte-identical prefix up to the latest user message (guards regressions
  that silently break caching — e.g. someone adding a timestamp to the
  system prompt).
- Ordering test: AllTools() stable across calls with MCP tools present.
- Manual verification: run a session against a real provider, watch
  `Usage.Cached()` climb; add it to the status line if it isn't visible
  (check current usage display first).

### Docs

`docs/features.md` section; `docs/models-providers.md` note about which
providers get which mechanism.

---

## Item 3 — subagent event journal (open a task, see full history)

Branch: `feat/task-journal`

**Root cause (confirmed):** `openTask` (internal/tui/tasks.go:165-201) seeds
the pane with header+prompt, then `Subscribe` receives only FUTURE events.
`emitter()` (internal/agent/background.go:305) broadcasts to current
subscribers and drops everything. Settled tasks show only the final Report.

**What it does:** the registry keeps a bounded per-task journal of every
emitted event; `openTask` replays it (formatted like the live handlers) and
subscribes atomically so nothing is missed or duplicated.

### Design

- New type in `internal/agent/background.go`:

```go
type journaledEvent struct {
    kind  int    // mirrors taskEventMsg kinds: 0 text, 1 tool start,
                  // 2 tool end, 3 steer, 4 compact
    s, s2 string // text / tool name+args / result
}
```

- Registry field `journal map[string][]journaledEvent` + byte budget
  (~128KB per task; on overflow drop oldest and mark `truncated bool` once —
  replay renders "[earlier output dropped]" at the top). Text deltas are
  fine-grained; coalesce consecutive kind-0 appends into one entry's string
  to keep the slice small.
- `emitter()` appends to the journal under `mu` before broadcasting (same
  lock hold — broadcast already snapshots subscribers under mu).
- New method `SubscribeWithJournal(id string, ev Events) ([]journaledEvent,
  bool)`: one lock hold — returns journal snapshot AND registers subscriber.
  Replaces `Subscribe` in `openTask`; keep `Subscribe` for other callers if
  any (grep; if only openTask uses it, fold and delete the old one —
  ponytail: one method).
- `openTask` renders the journal through the SAME formatting code as live
  `taskEventMsg` handling — factor the render into a helper
  (`renderTaskEvent(buf, kind, s, s2)`) so replay and live can never drift.
- Journal freed on `ClearSettled` (delete with the task) and on settle IF
  nobody opens views... no — settled tasks' journal IS their history until
  cleared; keep until ClearSettled. Memory bound: tasks × 128KB, fine.
- Steers into the task (taskSend) and follow-up turns: follow-up turn events
  (taskSend's ev closure) should also journal if the task is settled+open...
  ponytail: journal only the registry-emitter stream (the original run);
  follow-up turns already render into the open view and are covered
  properly by Item 5 persistence.

### Tests

- Loop test (fake provider): start background task, let it emit text + tool
  events, settle; THEN openTask in a headless model; assert buf contains the
  full pre-open transcript in order.
- Overflow: force >budget events, assert marker + retained tail.
- Atomicity: concurrent SubscribeWithJournal vs emitter — no missed/dup
  events (count-based assertion, `-race`).
- ClearSettled frees journals.

### Docs

`docs/features.md` subagent-view section update.

---

## Item 4 — busy-state steering while waiting on subagents

Branch: `feat/busy-steer`

**What it does:** when the user types while a turn is running AND the turn's
only in-flight work is foreground subagent tool calls, the message is
delivered as a steer into the live turn (`Agent.Steer`, drained at the next
loop boundary) instead of queueing behind the whole turn. Queue behavior is
unchanged when real tools (bash/edit/…) are mid-flight.

Research notes: `tui-findings.md` §C + §E (opencode re-checks steer after
every turn; codex interrupts waits on input).

### Design

1. Agent-side observability (`internal/agent`):
   - runTools (agent.go:441+) tracks in-flight tool names:
     `a.inflight sync.Map` (or mutex-guarded map[string]int) — increment at
     tool goroutine start (after lock acquisition, where OnToolStart fires),
     decrement at end.
   - `func (a *Agent) InFlightTools() []string` — snapshot, sorted.
   - `func (a *Agent) WaitingOnSubagents() bool` — true iff a turn is
     running AND inflight non-empty AND every entry == "subagent".
     (Empty inflight = mid-generation; treat as NOT waiting — typing during
     generation keeps queue semantics for now; widening that is a separate
     decision, see open questions in tui-findings.md §C.)
2. TUI submit path (tui.go ~2591, the busy+enter branch):
   - `if m.agent.WaitingOnSubagents() → m.agent.Steer(text)` + local echo as
     a steer row (reuse the steeredMsg rendering style, prefixed
     "you (steer):") — do NOT also queue it.
   - else → current queue behavior unchanged.
   - Input affordance: when WaitingOnSubagents, input placeholder switches to
     "steer this turn… (enter)"; when busy otherwise, "queued — sent when the
     turn ends". A cheap `placeholderFor()` consulted on refresh.
3. Empty-enter cancel path stays exactly as-is (explicit user intent,
   tui.go:2027 comment).
4. Steered message content: the model sees the user's text as a normal
   steered user message — same shape as background-task reports. No special
   wrapper needed; the loop-boundary injection already renders OnSteer.

Race notes: WaitingOnSubagents is a snapshot — by the time Steer lands the
subagent may have finished and real tools started. Harmless: Steer is
loop-boundary-delivered regardless; worst case the user message arrives
mid-turn (which is what steering IS). The queue path remains the
conservative default.

### Tests

- agent: inflight tracking counts across parallel batches (table test with
  scripted tool durations); WaitingOnSubagents true only for pure-subagent
  in-flight sets.
- Loop test: steer injected while subagent tool blocked → message appears at
  next boundary, turn continues (existing steer tests are the pattern —
  agent_test.go).
- TUI headless: busy + fake waiting-state → submit routes to Steer (assert
  agent.Messages / pending), queue stays empty; busy + bash in flight →
  routes to queue. Placeholder text switches.
- `-race`: inflight map under parallel tool execution.

### Docs

`docs/features.md` (busy/queue section update); key-hint text if the
placeholder changes are user-visible enough to document (they are).

---

## Item 5 — subagent session persistence with attribution

Branch: `feat/subagent-session-persistence`

**What it does:** a subagent's full message transcript persists to the
session store (SQLite, ~/.whip) attributed to its parent session and task id;
`--resume` shows restored tasks with their transcripts; the session list
shows subagent sessions nested under their parent.

Current state: only task METADATA persists (`SaveTask`, tui.go:2807-2820 —
id, description, prompt, status, report, times). The transcript lives in the
retained `BackgroundTask.sub *Agent` in memory and dies with the process.
Restored tasks (`Restored: true`) have no transcript at all.

### Design

1. Schema (`internal/session/session.go`, follow the existing ALTER TABLE
   additive-migration pattern at :144-148):
   - `sessions ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT ''`
   - `sessions ADD COLUMN task_id TEXT NOT NULL DEFAULT ''`
   - (kind derivable: parent_session_id != '' ⇒ subagent)
   - Subagent session id: `<parentID>/task/<taskID>` — deterministic, so
     re-persist is idempotent and resume can find it.
2. Save path: subagent messages are `t.sub.Messages`. Persist incrementally
   the same way the main session does — the StartBackground goroutine
   (background.go:264-284) wraps the turn; simplest correct version:
   **persist at settle** (after `Turn` returns, before/after `settle` — one
   `Save(subSessionID, 0, sub.Messages, model, provider)`), PLUS a
   **mid-run checkpoint every N appended messages** only if settle-only
   proves lossy in practice. Ponytail decision: settle-only first, note the
   checkpoint option. Follow-up turns (taskSend path) must re-persist after
   each follow-up — hook the same save after the follow-up Turn completes.
   OnRecord already gives a persistence seam; this is a second writer to a
   DIFFERENT table row (messages), keep it in background.go next to settle,
   guarded by the same session-id publication (SetSessionID pattern — the
   parent session id must be known; it is, via recordSession()).
3. Resume:
   - Loading a session (`--resume`) already restores task rows as
     `Restored: true` (tui.go:701 area). Extend: when restoring a task, look
     up `<sessionID>/task/<taskID>`; if messages exist, keep them on the
     restored BackgroundTask (new field `Transcript []llm.Message` or lazy
     load on openTask — lazy is cleaner: `openTask` on a Restored task reads
     the store when store != nil).
   - Restored task detail view renders the persisted transcript read-only
     (reuse Item 3's `renderTaskEvent` formatting by converting messages →
     journaledEvent-ish render, or a simpler message-level renderer —
     decide during implementation; simplest: role-labeled blocks, tool calls
     shown as rows).
   - ClearSettled must NOT delete persisted transcripts (live journal only).
4. Session list UI (`/resume` picker, ctrl+j resume test files show the
   surface): subagent sessions appear nested under the parent, dimmed,
   "↳ <task-id>: <description>", selectable read-only. If the picker code
   makes nesting awkward, ponytail: filter them out of the top-level list
   and surface them ONLY via the parent's restored task views. Decide with a
   quick look at the picker implementation.
5. Retention: subagent sessions accumulate fast (research fan-outs of 6+).
   Policy: subagent sessions die with their parent (delete cascade when a
   session is deleted — check if session deletion exists; if not, note it
   and skip). No independent cap for now; revisit if DB size matters.

### Tests

- session package: migration adds columns on an old DB file; Save/load of a
  subagent session row with attribution.
- agent loop test: background task settles → store has the transcript row
  with parent attribution.
- Resume test (REQUIRED per skill): run session with background task → kill
  (new process/store reopen) → resume → openTask shows the persisted
  transcript, read-only, no live input.
- Follow-up turn after settle re-persists (append, not clobber — Save with
  correct `from` index).
- `whip run` headless path: no store → no persistence, no panic.

### Docs

`docs/features.md` (sessions + subagents sections); README only if a
user-facing flag/command changes (probably none).

---

## Item 6 — spawn-lag polish

Branch: `feat/subagent-spawn-feedback`

Verified state (`tui-findings.md` §B-update): the transcript queued row
("⋯ subagent <desc>") renders mid-stream and swaps to a running row at tool
start — that path is fine. The DOCK row appears only when the tool's Run
completes (StartBackground fires OnChange), which can lag behind a slow
parallel sibling (global bash lock) or synchronous `git worktree add`
(subagent.go:110).

**What it does:**
1. Fire the dock-visible start signal EARLIER: move registry insertion +
   OnChange to the moment the subagent tool starts executing (before worktree
   provisioning and before the goroutine is fully armed). Concretely: split
   `StartBackground` into `RegisterBackground` (id + row + OnChange, status
   "starting") and the goroutine launch; or cheaper — keep one function but
   call it before `provisionSubagentWorktree` and pass the worktree into the
   prompt after (prompt is already built before StartBackground — reorder:
   provision → register+OnChange immediately → launch). Watch ordering: the
   ⚙ badge count and dock use registry state, so registering before the
   goroutine starts is safe as long as cancel/Done are initialized first
   (they are, in the constructor).
2. Provision worktrees concurrently with registration so a slow `git
   worktree add` doesn't delay the visible row: provision in the task's
   goroutine and steer the path into the subagent's initial prompt… that
   changes prompt timing. Ponytail: measure first — if `git worktree add` is
   <100ms on this repo, skip async entirely and just do (1).
3. Status line: while a background task is "starting" (registered, goroutine
   not yet emitting), the dock row already shows ⟳ — verify no extra state
   needed.

**Tests:** headless model — subagent tool starts, assert dockTasks()
non-empty BEFORE the tool result returns (block the tool via a scripted
provider/latch). `-race` over register/launch ordering.

**Docs:** one line in `docs/features.md` subagent section.

---

## Item 7 — (deferred) opencode-style auto-background for bash

Not in this batch. Research captured (`wait-tool-research.md` oh-my-pi
section): bash calls race completion vs a 60s threshold; slow commands
auto-background with a handle + notify-on-complete delivered at the yield
boundary. Revisit after Items 1–6 land; `wait` may cover the practical need.

---

## Execution order & dependencies

```
1 (wait tool)        ─┐ independent
2 (prompt caching)   ─┤ independent
3 (task journal)     ─┤ independent; 5 builds on 3's renderTaskEvent helper
4 (busy steer)       ─┤ independent
5 (persistence)      ─┘ benefits from 3 (restored-task rendering)
6 (spawn feedback)   ── last; smallest
```

Suggested sequence: **3 → 1 → 2 → 4 → 5 → 6** (3 is smallest and fixes the
daily annoyance; 1 is the original ask; 2 is the biggest felt-perf win).
Parallelizable: 1, 2, 3, 4 touch mostly disjoint files (tools+agent vs llm
vs tui/tasks vs tui submit path) — background subagents could run 2 and 3
concurrently while driving 1/4 directly, if desired.

## Definition of done (per item)

- [ ] `task check` green; `go test -race ./...` green for concurrent items
- [ ] tests named in `docs/features.md` section, roadmap checkbox if listed
- [ ] plan checkbox ticked here; deviation notes appended if the design
      changed mid-build
- [ ] least-code re-read of the diff (ponytail pass) before PR

## Global checklist

All six items shipped, each on its own branch/PR, CI green (11/11) at PR-open
time; PLAN.md's per-item checkboxes and Definition-of-done apply. #59 merged.

- [x] 1. wait tool — PR #58 (feat/wait-tool)
- [x] 2. prompt caching — PR #59 MERGED (feat/prompt-caching)
- [x] 3. task event journal — PR #57 (feat/task-journal; registry
      journal + atomic SubscribeWithJournal + shared renderTaskEvent; journal
      tests, replay tests, task check + -race green)
- [x] 4. busy-state steering — PR #60 (feat/busy-steer)
- [x] 5. subagent session persistence — PR #61
      (feat/subagent-session-persistence; transcripts as attributed sessions
      `task-<parent>-<taskID>`, forked_from=parent, restored-task replay)
- [x] 6. spawn-lag polish — PR #62 (feat/subagent-spawn-feedback; register
      before worktree provision, worktree path queued as first steer)
- [x] memory cap change (DONE — 300→2000 in internal/memory/memory.go,
      folded into PR #57)
- [ ] cleanup: delete `internal/tui/whip-transcript-.md` (stray untracked
      export file noticed at session start — confirm with user before
      deleting)
