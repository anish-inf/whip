# Subagents 100x: model routing, user spawning, chat-with-subagent

Branch: abe/subagents-100x

## Deviations recorded while landing

- Abe (mid-review): rename `task` → `subagent` everywhere user/model-facing.
  Tool names are now `subagent` / `subagent_steer`, ids `sub-N`, commands
  `/subagent` (spawn) and `/subagents` (list, `/tasks` kept as alias).
  Internal Go identifiers (BackgroundTask, taskRegistry, …) unchanged.
- Two pre-existing bugs surfaced by the new tests, fixed at the root:
  `settle()` now notifies/persists before closing `Done` (waiters could read
  a stale "running" session row), and `newSub` copies the llm.Client
  (`Turn` writes `Client.OnRetry`, so concurrent subagents sharing the
  parent's or TaskDefault's client pointer raced).
- Also fixed: `schedule_cmd_test.go` polled `agent.Messages` cross-goroutine
  (raced under -race); now uses `MessagesSnapshot`.

## What this does / Goal

Three upgrades to whip's `task` subagents (goal set via /goal by Abe):

1. **Subagent model routing.** Subagents default to a cheap fast model
   (`deepseek-v4-flash-0731`, same default as compaction) instead of the
   session model; the user pins it via config `taskModel`/`taskProvider`;
   the MAIN MODEL can pick a model per task via new `model`/`provider`
   params on the `task` tool. Openrouter-only configs resolve the default
   via a catalog suffix scan (`…/deepseek-v4-flash-0731`).
2. **User-spawned subagents.** `/task [-m model[@provider]] <prompt>` starts
   a background subagent directly — works while a turn is running (the LLM
   is no longer the only driver).
3. **Chat with subagents.** The retained subagent (`*Agent`) lives on its
   `BackgroundTask`. The task detail view gets an input line: typing while
   the task RUNS steers it (channel-native parent→child pipe, same Steer
   primitive the parent uses); typing after it SETTLES runs follow-up turns
   on its preserved context — a task becomes a full chat session. The main
   model gets the same power via a new `task_steer` tool.

## Non-goals

- Named agent definitions (`.agents/*.md` frontmatter) and `@agent`
  mentions — separate roadmap items, unchecked.
- Persisting subagent conversations (follow-up chat is live-only; restored
  tasks are read-only — their process died).
- A palette panel / slash command for pinning the task model (config field
  + docs cover the ask; palette row later if wanted).

## Design

Surfaces: agent loop (`internal/agent/task.go`, `background.go`, `agent.go`),
config (`internal/config/config.go`), TUI (`internal/tui/tui.go`, `tasks.go`,
`palette.go`, `auth_cmd.go`), CLI (`cmd/whip/run.go`), docs.

### agent (config-free; front-ends inject resolution)

```go
type SubModel struct { Client *llm.Client; Model string; ContextLimit, MaxTokens int }
// Agent fields:
TaskDefault  SubModel                                    // zero = use session model
ResolveModel func(model, provider string) (SubModel, error) // nil = per-task overrides rejected
```

- `newSub(o SubModel) *Agent` shared by blocking task, StartBackground.
  Precedence: explicit `o` → `TaskDefault` → parent client/model.
- `task` tool gains optional `model`/`provider`; unknown model → `"Error: …"`
  tool output (never aborts the turn).
- `StartBackground(desc, prompt string, o SubModel)` — the old ctx param was
  unused (background tasks own their context); dropped.
- `BackgroundTask.sub *Agent` retained (set before publish; snapshots copy
  the pointer). New: `Agent.SteerTask(id, text) error` (running only) and
  `Agent.FollowupTask(ctx, id, text, ev) (string, error)` (settled only;
  status/report unchanged — the registry lifecycle stays single-settle,
  Done closes exactly once).
- `task_steer` tool: `{id, message}` → SteerTask; result string tells the
  model the guidance lands at the child's next loop boundary.
- `ClearSettled(keep ...string)` so the TUI can protect a task whose chat
  view is open from the new-turn sweep.

### config

- `TaskModel`/`TaskProvider` fields; `DefaultTaskModel = DefaultCompactModel`
  ("deepseek-v4-flash-0731").
- `Snapshot()` — shallow map copy so the TUI can hand worker goroutines a
  race-free config view (ResolveModel runs on tool goroutines; /auth mutates
  cfg maps on the UI goroutine).

### tui

- `applyTaskModel()` next to every `applyCompactModel()` call: resolves
  taskModel → default → catalog suffix scan → silent fallback (session
  model); installs `TaskDefault` + `ResolveModel` over a `cfg.Snapshot()`.
- Agent-swap sites (palette preview-switch, /auth rebuild) copy
  `TaskDefault`/`ResolveModel` like they copy CompactClient/CompactModel.
- `/task` command (in the works-while-busy list): parses optional
  `-m model[@provider]`, auto-description = first 8 words, appends a
  "⚙ started task-N (ctrl+t to watch)" note.
- Task detail view: textinput at the bottom; enter → running: SteerTask +
  local echo; settled: FollowupTask streamed into the pane via sendTaskMsg
  (busy flag serializes; cancel via ctrl+x which replaces 'x' so typing is
  free; follow-up ctx cancel stored on the view). Restored tasks: read-only
  hint. Usage from follow-ups rolls into parent via OnUsage: AddUsage.

### cmd/whip/run.go

Wire `ResolveModel` + `TaskDefault` from the loaded cfg (headless runs get
the same routing; no snapshot needed — nothing mutates cfg there).

## Prior art

- opencode `background-job.ts` registry → already ported as Done-channel
  broadcast (docs/concurrency.md). This change extends the same shape: the
  parent→child steer is the existing `pendingSteer` queue reused on the
  child, no new synchronization.
- claude-code's Agent tool model override + "chat with subagent as a full
  session" UX is the target experience (goal text).
- Compact-model plumbing (`applyCompactModel`, `DefaultCompactModel`
  fallback chain) is the in-repo pattern task-model routing mirrors.

## Test plan (stdlib, -race)

- agent: task tool `model` override hits resolver and the sub request
  carries the resolved model id (textServer records `req.Model`).
- agent: TaskDefault routes background subagent; zero TaskDefault falls
  back to parent model.
- agent: task tool with unknown model returns an Error string, turn
  continues.
- agent: SteerTask lands in a running child (gated server); errors on
  settled/unknown. FollowupTask works on settled task, leaves status/report
  untouched, errors on running/restored.
- agent: ClearSettled(keep) preserves the kept id.
- tui (headless): `/task` spawns a background task with description;
  applyTaskModel resolves default / falls back when unconfigured / catalog
  suffix scan finds openrouter-style ids.

## Docs

- features.md: extend the subagents section (model routing, /task, chat,
  task_steer; live-only follow-ups recorded as deliberate).
- roadmap.md: check "user-spawned subagents"/model-choice items (add lines
  under Skills & subagents), leave `.agents/*.md` + `@agent` unchecked.

## Tasks

- [x] agent: SubModel, TaskDefault, ResolveModel, newSub, task tool params
- [x] agent: sub retained, SteerTask, FollowupTask, task_steer, ClearSettled(keep)
- [x] config: TaskModel/TaskProvider, DefaultTaskModel, Snapshot
- [x] tui: applyTaskModel + call sites + swap-site copies
- [x] tui: /task command
- [x] tui: task view chat input (steer + follow-up), ctrl+x cancel
- [x] run.go wiring
- [x] tests (agent + tui headless), -race
- [x] docs (features.md, roadmap.md)
- [x] task check green
