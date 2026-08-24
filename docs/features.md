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
  token count crosses the compaction threshold — a percent of the advertised
  context window, default 50% (`compactPct` in config, clamped 10–90;
  `Agent.CompactThreshold` holds the fraction). Slide it in the palette's
  "Compaction level" row (←/→ steps ±10%).
- **Reactive**: if the provider still rejects a request with a context-limit
  error (`context_length_exceeded`, `prompt_too_long`, HTTP 413), `Turn`
  compacts once and retries. A `compacted` guard prevents retry loops.

`compact()` keeps the system prompt and a recent tail, and is **orphan-safe**:
a kept tail that begins with a `tool`-role message walks back to its owning
assistant message so no tool result references an erased call ID. The summary
runs as a non-streaming `Complete` on the compaction model — the built-in
default `deepseek-v4-flash-0731` (`config.DefaultCompactModel`, resolved from
the user's config when `compactModel` is empty), a configured
`compactModel` / `compactProvider`, or the conversation's own model when the
default isn't in the config.

Token bookkeeping: `llm.Usage` (prompt/completion/cached) is read off the
terminal stream chunk (`stream_options: include_usage`) and folded into session
totals via `AddUsage`. Compaction and subagent calls count too.

Commands: `/compact` (compact now), `/compact <model> [provider]` (pick the
summarizer), `/compact off` (restore the built-in default). The palette's
"Compaction model" panel lists every configured model behind a
"default (…)" row that restores the default; "Compaction level" steps the
threshold ←/→.

Tests: `agent_test.go` — `TestTurnAutoCompactsOnContextLimit`,
`TestCompactDoesNotLoopOnRepeatedContextLimit`, `TestCompactKeepsToolCallPair`,
`TestProactiveCompactAtFiftyPercent`, `TestCompactThresholdExplicitOverride`,
`TestUsageAccumulates`; `compact_cmd_test.go` —
`TestCompactModelEmptyResolvesDefault`, `TestCompactModelDefaultFallsBack`,
`TestCompactThresholdFor`, `TestSetCompactPct`; `palette_test.go` —
`TestPaletteCompactPanelAppliesInPlace`,
`TestPaletteCompactPanelDefaultRowRestores`, `TestPaletteCompactionLevelSteps`.

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
  and ←/→ steppers for the compaction level — `palette.go`.
- **Mouse**: `/mouse` toggles capture; with capture off the terminal's native
  selection works, with it on shift-drag selects. `"mouse": false` in config
  disables capture at startup.
- Queueing (enter while busy), steering (empty enter), history recall (↑/↓),
  `@file` mentions, `$skill` invocation, `/goal` loop, `/resume` session
  picker, `/effort` reasoning levels — see the roadmap for the full list.
- **Settings commands run mid-turn.** `/theme`, `/mouse`, `/effort`, `/tasks`,
  `/help`, `/cd`, `/pwd`, and the non-submitting `/goal` forms (bare, `clear`,
  `rounds`) execute immediately while busy instead of queueing — queued text
  is sent to the model verbatim after the turn, which is nonsense for a
  settings change. The `busyCmd` allow-list gates this; everything else
  (`/model`, `/goal <text>`, plain messages) still queues. These commands only
  touch TUI-local state or fields read at the *next* request, and their
  confirmation notes append as transcript blocks safely behind the streaming
  one. Tests: `queue_test.go` (`TestBusyCmdAllowList`, `TestEnterWhileBusy*`).
- **`!` shell escape, `/cd`, `/pwd`** — `shell.go`. An input starting with
  `!` runs locally via the same `bashrun` runner as the agent's bash tool
  (120s cap, `tools.TruncateTail`, `(exit …)` markers) — no model turn, no
  busy state, runs immediately even mid-turn, and queued `!` lines execute
  when the queue drains instead of being submitted to the model. The command
  runs on a goroutine and lands via `shellDoneMsg` (the UI never blocks), the
  output lands in the transcript as a collapsed tool block **and** in the
  conversation so the model sees it at the next request: idle via
  `Agent.AppendUser` (non-authored `$ <cmd>` user message), mid-turn via
  `Agent.Steer` (mutex-guarded, injected at the next loop boundary with the
  usual `(steered)` echo) — the turn goroutine owns `Agent.Messages` while
  busy. Esc stays bound to the turn; a running escape isn't cancellable (the
  120s cap bounds it). `/cd [dir]` changes loopy's process cwd (an in-flight
  command keeps its already-resolved cwd, POSIX; the next spawns, relative
  tool paths, and the `@` index follow); bare prints it, `~` expands. `/pwd`
  prints it. Port of opencode's `session.shell` minus the shell-mode chrome —
  see the `ponytail` note in `shell.go`.
  Tests: `shell_test.go` (output/message routing idle+busy, queue-drain,
  truncation, echo rules, cd/pwd incl. `~` and bad dirs).
- **`/goal-from-context`** distills the last two conversation messages into a
  goal statement with one non-streaming call on the current model (the
  compact-model override is deliberately ignored), then sets it exactly like
  `/goal <text>` and starts the goal loop. Prompt building is pure
  (`agent.BuildGoalFromContextPrompt` over the window from
  `agent.GoalFromContextMessages`); the TUI command mirrors `/compact`'s
  goroutine + `goalFromContextMsg` pattern, refusing while busy and running
  inline when headless. Tests: `goal_test.go` (`TestGoalFromContext*`).

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

## MCP

`internal/mcp/` — loopy is an MCP client (stdio + streamable HTTP) and, via
`loopy mcp serve`, an MCP server. Three sources of server config merge with
loopy's own on top (per-name, whole entry): a project `.mcp.json`
(claude-style: `{"mcpServers": {name: {type, command, args, env, url,
headers}}}`), `~/.codex/config.toml` `[mcp_servers.*]` (codex-style), and the
`"mcp"` block in `~/.loopy/config.json`. Claude `type: sse` imports as
disabled-with-note (legacy transport); `${VAR}` references in env/headers
expand from loopy's environment.

- **Manager** (`manager.go`) — one lifecycle goroutine per server; a
  `ready chan struct{}` closes once on first settle (the BackgroundTask
  close-to-broadcast pattern), so tool calls block only on *their* server and
  startup never waits. Statuses: connecting → ready/failed (plus disabled);
  a dropped session flips to failed via a generation-guarded watcher
  (opencode's client-identity check, `mcp/index.ts:443`). Connect/list bounded
  by `startupTimeout` (default 30s — opencode's DEFAULT_TIMEOUT).
- **Tool bridge** — listed tools become agent tools named
  `mcp__<server>__<tool>` (claude-code convention; double underscores keep
  the split unambiguous since tool names contain `_`). Unsafe server-name
  chars get an fnv hash suffix so sanitized names can't collide (an opencode
  weakness). Calls serialize per server (1-cap channel — many stdio servers
  are single-request), run under `toolTimeout` (default 60s), and respect
  ctrl+c via ctx. Results flatten to text: images/audio/binary resources →
  placeholders, `structuredContent` → JSON when there's no text, `IsError` →
  `"Error: …"` fed back to the model — a broken MCP tool never kills a turn.
  Output capped at the shared 50KB truncation. MCP tools take no file locks
  and run in parallel with everything.
- **Late arrivals** — `Manager.SetOnChange` pushes refreshed tool sets into
  `Agent.SetMCPTools` (mutex-guarded; a settle mid-turn can't race the slice
  a request reads), so a server connecting after turn 1 appears without a
  restart.
- **TUI** — `/mcp` shows the status table (`● N tools` / `✗ err` /
  `○ disabled` / `◌ connecting…`); `/mcp <name> reconnect|enable|disable`
  reconnects live or persists a toggle through the guarded `Config.Save`.
- **CLI** — `loopy mcp list` (merged view with source labels), `loopy mcp
  add <name> -- <cmd...>` / `--url`, `loopy mcp remove`. `loopy mcp serve`
  (`serve.go`) exposes loopy's read/bash/edit/write as an MCP stdio server
  for other harnesses.
- **Shutdown** — `Manager.Close()` runs before `bashrun.KillAll()`; stdio
  children spawn in their own process group, and the SDK terminates them
  (stdin close → SIGTERM → SIGKILL after 3s).

Polish (the "never stuck, always know why" pass):

- **Fail-fast calls** — a call to a failed/disabled server returns instantly
  with an actionable message (`/mcp <name> reconnect|enable`); a
  still-connecting server caps the wait at a 5s grace then returns "retry in
  a moment". No turn parks on a 30s startup timeout.
- **Did-you-mean** — `tools.Suggester` (installed by `Agent.SetMCPTools`)
  runs an early-exit Levenshtein over live tool names, so a stale/typo'd
  `mcp__` call gets `did you mean mcp__docs__greet?` instead of a dead end.
- **First-settle notes** — each server's first settle lands one transcript
  line (`⚡ mcp: docs ready (4 tools)` / `✗ mcp: x failed: …`); later
  transitions stay quiet.
- **Auto-reconnect** — a dropped session retries in the background with
  backoff (1s/2s/4s, cap 3), guarded against close/disable/dupes; manual
  `/mcp reconnect` stays unlimited.
- **Server instructions** — initialize-result instructions render into an
  `<mcp_instructions>` block appended to the system prompt every turn
  (alongside skills), tracking live sessions.
- **`loopy mcp test <name>`** — the doctor: connect + list + timing + tool
  names, stderr tail on failure, non-zero exit — CI-checkable `.mcp.json`.

Tests: `config_test.go` (claude/codex parsing incl. a real-world codex
config, merge precedence, discovery errors, tool-name round-trips),
`manager_test.go` (connect/call, error-as-output, structured+media
flattening, dead-server degradation, reconnect, parallel calls under `-race`,
ctx cancel mid-connect), `loop_test.go` (model→MCP→model round trip against
a fake provider; stale def on a dead server returns `"Error: …"` and the turn
completes), `selfhost_test.go` (`loopy mcp serve` end-to-end, gated on
`LOOPY_TEST_SELFHOST=1`), `tui/mcp_test.go` (status view, toggle persistence
round-trip).

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
into the system prompt as an `<available_skills>` catalog in the Agent Skills
spec format (`<skill><name>/<description>/<location>`, XML-escaped). The model
reads a SKILL.md with its own read tool when relevant. Skills re-index every
turn, so new ones load without restarting.

**Spec compliance** (agentskills.io, matching pi's `core/skills.ts`): name
validated (≤64 chars, lowercase a-z/0-9/hyphens, no leading/trailing/double
hyphens), description ≤1024 chars (a *validity* ceiling, not a prompt budget),
`disable-model-invocation: true` skills excluded from the catalog but still
invocable via `$name`. Violations load with a `Warning` (surfaced in the
startup report), never silently disappear. Tests: `skills/spec_test.go`.

**`/context-doctor` (alias `/context-doctor`)** — fresh-session context audit: every
automatic injection source with its estimated token cost (base system prompt,
skills block with the 5 biggest offenders, per-server MCP tool schemas, server
instructions, built-in tool schemas, conversation history, and actual session
spend once requests have run), a TOTAL line, and trim pointers. Built for
users arriving from heavier harnesses whose first call silently carries tens
of thousands of tokens of skill/MCP bloat. Tests: `tui/context_doctor_test.go`.

**Startup resource report** — first paint names what loopy loaded: `skills: N
loaded`, one `⚠` line per degraded skill (description over maxDesc → truncated
in the prompt) or unparseable SKILL.md (pi's [Skill conflicts] lesson — a
broken skill is never silent), and one `mcp:` line with per-server status
glyphs (`✓ N tools` / `✗` / `○ disabled` / `◌ connecting`). Skipped on resume.
Tests: `tui/startup_report_test.go` (warnings, MCP glyphs, silence when empty).

Installed: the `golang-*` skill set plus `i-have-adhd` (output-shaping for ADHD
readers; invoke with `/i-have-adhd`, off with "stop adhd mode").
