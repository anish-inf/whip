# Features

loopy is a minimal coding-agent harness: an interactive bubbletea TUI driving an
LLM tool-use loop (bash / read / write / edit / task) with provider-routable
models. This document is the map of what's shipped and where it lives. Each
section links the behavior to the code and its tests.

## The agent loop

`internal/agent/agent.go` — `Agent.Turn` is the loop: append the user message,
stream a completion, run any tool calls, append results, repeat until the model
stops calling tools. Steered messages (`Steer`) inject at loop boundaries,
never mid-generation.

### Parallel tool calls with per-path file locks

When the model emits several tool calls in one turn, `runTools` fans them out
to goroutines and collects results on a buffered channel, laid back out in
**call order** (the API matches tool results to call IDs). `OnToolStart` /
`OnToolEnd` fire per call as they run, so the UI shows each tool live.

`internal/agent/filelocks.go` — mutations to the same file serialize through a
**per-canonical-path channel semaphore** (a 1-capacity `chan struct{}` per
path: send to acquire, receive to release). Two edits to `foo.go` can't
interleave; edits to different files run truly in parallel. `bash` takes a
global lock because a command's side effects aren't attributable to one path.
Reads don't lock.

This is the Go-native port of pi's `withFileMutationQueue` (per-path promise
chains in TypeScript). In Go the lock is a buffered channel — no explicit
unlock bookkeeping.

Tests: `parallel_test.go` — `TestToolCallsRunInParallel` (overlap measured via
a concurrency counter), `TestSamePathEditsSerialize`, `TestToolMutationPath`,
`TestCanonicalPathKey`.

### Compaction

When the conversation fills the context window, old turns fold into an
LLM-generated summary. Two triggers:

- **Proactive**: `maybeCompact` runs before each request once the estimated
  token count crosses 90% of the advertised context window.
- **Reactive**: if the provider still rejects a request with a context-limit
  error (`context_length_exceeded`, `prompt_too_long`, HTTP 413), `Turn`
  compacts once and retries. A `compacted` guard prevents retry loops.

`compact()` keeps the system prompt and a recent tail, and is **orphan-safe**:
a kept tail that begins with a `tool`-role message walks back to its owning
assistant message so no tool result references an erased call ID. The summary
runs as a non-streaming `Complete` on the conversation's model, or on a
dedicated compaction model if configured (`compactModel` / `compactProvider`).

Token bookkeeping: `llm.Usage` (prompt/completion/cached) is read off the
terminal stream chunk (`stream_options: include_usage`) and folded into session
totals via `AddUsage`. Compaction and subagent calls count too.

Commands: `/compact` (compact now), `/compact <model> [provider]` (pick the
summarizer), `/compact off` (clear the override).

Tests: `agent_test.go` — `TestTurnAutoCompactsOnContextLimit`,
`TestCompactDoesNotLoopOnRepeatedContextLimit`, `TestCompactKeepsToolCallPair`,
`TestProactiveCompactAtNinetyPercent`, `TestUsageAccumulates`.

### Background subagents

`internal/agent/background.go` — `task` with `background: true` launches a
subagent that runs **concurrently with the parent** instead of blocking the
turn. This is the channel-native port of opencode's `background-job.ts`
registry.

Each task is a `BackgroundTask` with a `Done chan struct{}`. When the subagent
settles, the registry `settle()`s and **closes `Done` once** — closing a
channel broadcasts to every waiter at once, so the tool caller, the TUI, and
`/tasks` all wake together with no per-waiter state (opencode needs a per-job
`Deferred` for the same thing). On settle the report fans back into the parent
as a **steered message**, so the model sees it on the next loop boundary.

- `Tasks().List()` / `Get(id)` / `Cancel(id)` — registry snapshot + cancel.
- `Tasks().OnChange` — the TUI installs a callback that sends a message to
  redraw live.
- `/tasks` lists running/done tasks with report previews; a `⚙ N bg` header
  badge shows the running count.

Background tasks use a context **not** tied to the current turn — they outlive
it by design. Cancelling a task cancels its subagent's turn.

Tests: `TestBackgroundTaskDeliversReport`, `TestBackgroundTaskBroadcastsToManyWaiters`
(8 waiters all woken by one channel close), `TestBackgroundTaskCancel`.

## Models & providers

`internal/config/config.go`, `internal/config/catalog.go` — models route to
providers; the provider's `GET /models` is the source of truth for
capabilities. Two distinct limits, both honored:

- **Context window (input)** — `Model.Context` (legacy `maxTokens` still
  parses via `ContextWindow()`), overridden by the provider's
  `context_length`. Drives the header's `% ctx` and proactive compaction.
- **Output cap** — `Model.MaxOut`, else the provider's `max_completion_tokens`,
  else the context window. Sent as the request's `max_tokens`.

The catalog (`~/.loopy/models.json`) caches each provider's model list with a
24h TTL and refreshes in the background.

`internal/llm/openai.go` — the streaming client. Typed `HTTPError` (keeps the
`<status>: <body>` shape), `IsContextLimit()` classifies context-overflow
errors for the compaction retry, `Stream` returns the message + usage, and
`Complete` is the non-streaming round-trip used by compaction.

## The TUI

`internal/tui/tui.go` — bubbletea fullscreen alt-screen. Highlights:

- **ctrl+c is a two-stage key.** While busy it interrupts the turn (first press
  arms, second cancels). While idle it quits — but only on a **second press
  within a 2-second window**, so a stray ctrl+c can't nuke the session. The
  hint `press ctrl+c again to quit` shows while armed.
  Tests: `quit_confirm_test.go`.
- **Collapsible tool results.** Tool results store raw output in a `blockTool`
  transcript block and render collapsed to 5 lines with a `… +N lines` hint.
  `ctrl+e` toggles the most recent; clicking a block expands/collapses it
  (each block tracks its rendered line range `y0`/`y1` so the click row maps
  through the viewport offset). Blocks re-render at the current width on
  terminal resize. Tests: `tool_expand_test.go`, `resize_test.go`.
- **Markdown.** Assistant messages render through glamour; streamed in-flight
  text stays plain and renders on flush. `markdown.go`.
- **Command palette** (ctrl+p) with sub-panels for model/effort/goal/compaction
  — `palette.go`.
- **Mouse**: `/mouse` toggles capture; with capture off the terminal's native
  selection works, with it on shift-drag selects. `"mouse": false` in config
  disables capture at startup.
- Queueing (enter while busy), steering (empty enter), history recall (↑/↓),
  `@file` mentions, `$skill` invocation, `/goal` loop, `/resume` session
  picker, `/effort` reasoning levels — see the roadmap for the full list.

## Conversation time travel

`internal/tui/rewind.go` — **double-esc while idle** opens the rewind picker:
the conversation's authored user messages, newest first, with the transcript
**live-scrolling** to the selected message as you browse (opencode's
`dialog-timeline.tsx` `onMove`, and `msgBlock` maps conversation index →
transcript block so the jump is direct). enter rewinds to just before the
selected message: `Agent.Messages` is truncated, the clipped tail becomes an
in-memory **redo stack** (`m.future`, oldest first), the DB rows are deleted
(`Store.DeleteFrom`), the transcript is rebuilt via `seedTranscript`, and the
rewound message's text lands back in the input for editing (opencode's undo:
"the input restore is what makes it feel good"). Cuts sit at user-message
indices, so a tool_call is never orphaned from its results.

**Forward travel:** reopening the picker while rewound lists the clipped
messages dimmed, marked `(rewound)`; enter on one pulls the tail back in and
re-saves it. Submitting new input while rewound discards the redo stack.
Compaction also drops it (a stale redo would resurrect summarized history).
esc cancels and restores the scroll position. The redo stack is in-memory
only by design: quitting while rewound leaves the DB at the rewound point.

`internal/tui/fork.go` — **`/fork [name]`** copies the conversation into a
**new** session (one `INSERT…SELECT` in `Store.Fork`; the original is
untouched and stays under `/resume`) and switches to it. Bare `/fork` opens an
inline name prompt prefilled with `<title> (fork #N)` (`Store.ForkTitle`
increments past existing forks and unwraps nested suffixes, opencode's
`getForkedTitle`). **`f` in the rewind picker** forks from the selected
message instead — one picker, two destinations. Forking while rewound pulls
the redo stack up to the picked point into the copy. **`/rename [title]`**
retitles the current session (`Store.SetTitle`); bare opens the same inline
prompt prefilled with the current title. Both prompts stash and restore any
in-progress draft. All three refuse to run mid-turn. Palette entries:
"Rewind conversation", "Fork session", "Rename session" under Session.

Tests: `rewind_test.go` — double-esc opens/cancels, busy esc still
interrupts, truncation + input restore + DB rows deleted, forward travel,
partial-rewind DB prefix, tool-call-pair safety, stale esc-arm across modal
dismiss, draft preservation, resume-after-rewind. `fork_test.go` (session) —
prefix/full copy, fork-title numbering, rename, DeleteFrom. `fork_test.go`
(tui) — fork with arg, bare prompt suggestion + cancel, fork from the picker,
fork while rewound into the redo stack, rename both paths.

## Process safety

`internal/tools/bashrun/bashrun.go` — every command the agent runs is tracked
in a process registry (`track`/`untrack`). On exit (`tui.Run` returning — quit,
`/quit`, or a signal), `KillAll()` SIGKILLs every tracked **process group** and
waits briefly for reaping, so an agent-started server or watcher never outlives
loopy.

The non-interactive path captures via explicit `StdoutPipe`/`StderrPipe` and
closes the read ends the moment the process exits, so a detached grandchild
(`nohup`, `sleep 30 &`, a daemonized server) holding the write end can't hang
the agent on pipe EOF. The interactive path runs in a PTY for sudo/ssh-style
prompts, killed after 15s of no input.

Tests: `killall_test.go` — `TestKillAllReapsChildren` (kills a live `sleep 60`),
`TestBackgroundGrandchildDoesNotHang`.

## Skills

`internal/skills/skills.go` — scans `.agents/skills/*/SKILL.md` (project) and
`~/.loopy/skills/` (user) for a name+description frontmatter block, injected
into the system prompt as an `<available_skills>` block. The model reads a
SKILL.md with its own read tool when relevant. Skills re-index every turn, so
new ones load without restarting.

Installed: the `golang-*` skill set plus `i-have-adhd` (output-shaping for ADHD
readers; invoke with `/i-have-adhd`, off with "stop adhd mode").
