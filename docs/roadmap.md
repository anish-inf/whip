# loopy roadmap

UX niceties worth adopting, learned from [pi](file:///home/abe/code/pi) and
[opencode](file:///home/abe/code/coding-harnesses/opencode). Check things off as they land.
Full exploration reports: [learnings/other-harnesses/opencode/](learnings/other-harnesses/opencode/).

**Reference docs:** [features.md](features.md) (what's shipped, where it lives,
its tests) and [concurrency.md](concurrency.md) (the channel patterns behind
parallel tool calls and background subagents).

## Table of contents

- [Input & editing](#input--editing)
- [Transcript & rendering](#transcript--rendering)
- [Sessions](#sessions)
- [Agent loop](#agent-loop)
- [Skills & subagents](#skills--subagents)
- [Models & providers](#models--providers)
- [Safety & permissions](#safety--permissions)
- [Theming & config](#theming--config)
- [CLI surface](#cli-surface)

## Input & editing

- [x] Queue messages while busy (enter, codex-style multiple), force-steer queue into the running turn (empty enter, grok-style), auto-send queued as follow-up turns
- [x] Explicit interruption: double ctrl+c while busy (cf. opencode's triple-escape with 5s reset — `packages/tui/src/routes/session/index.tsx:1388`)
- [ ] Queue management: edit/remove queued messages before they send (opencode `<leader>q`, `runtime.queue.ts`)
- [ ] Multiline input (grow textarea; opencode binds newline to `shift+enter,ctrl+enter,alt+enter,ctrl+j` because terminals disagree — `keybind.ts:161`)
- [ ] `!` prefix shell mode: only triggers at cursor offset 0, backspace-at-0 exits, output lands in transcript as a tool result the model can see (opencode `prompt/index.tsx:815`)
- [x] `@` file mentions, pointer-style: tag any file, any path (relative/absolute/`~`), `@file#10-40` line ranges, tab-completion — a pointer note is appended to the user message, contents never inlined; the model probes with its own tools (Abe's design; alternative documented in [learnings/other-harnesses/opencode/at-mentions.md](learnings/other-harnesses/opencode/at-mentions.md))
- [ ] `@` mention fuzzy picker + frecency ranking (opencode `prompt/frecency.tsx`, `prompt/autocomplete.tsx`)
- [ ] External editor for long prompts: `$VISUAL || $EDITOR`, suspend renderer → edit temp .md → resume (opencode `editor.ts:26-53`; pi setting `externalEditor`)
- [ ] Paste handling: collapse big pastes (≥3 lines) into a `[Pasted ~N lines]` placeholder expanded on submit (opencode `prompt/index.tsx:1149`)
- [ ] Persist prompt input history to disk, restore across sessions; up/down only navigate history when cursor is at offset 0 (opencode `prompt/history.tsx`)

## Transcript & rendering

- [x] Markdown rendering for assistant messages (glamour, hardcoded dark style — no OSC background query; finalized segments + resumed transcripts render rich, in-flight streaming stays plain text; right-padding stripped, body aligned under the "● " marker)
- [ ] Diff view for `edit` tool results (pi edit tool returns `details: {diff, patch, firstChangedLine}` — `packages/agent/src/harness/tools/edit.ts`; opencode picks split vs unified by terminal width >120)
- [ ] Tool rows: icon + present-participle verb while running ("Reading file…"), collapse to one line on completion, red + expandable on failure (opencode `routes/session/index.tsx:1836`, `util/collapse-tool-output.ts` — 19 lines)
- [ ] Render tool calls as they stream, before execution starts (pi: `message_update` spawns `ToolExecutionComponent` keyed by tool-call id)
- [ ] Spinner with elapsed time + token count (% of context window) + cost in status line (opencode `routes/session/footer.tsx`)
- [ ] Toast-style transient notifications for command success/failure (opencode `ui/toast.tsx` — 102 lines)
- [ ] Desktop notification/sound when a turn finishes and the terminal is blurred (opencode `attention.ts` — "when: blurred" is the detail that makes it not-annoying)

## Sessions

- [x] SQLite session store with `--resume` / `/resume` picker
- [ ] Session titles: auto-generate a short title from the first exchange
- [ ] `/rename` a session (opencode: ctrl+r prompt dialog)
- [ ] `/fork` a session (pi: tree-structured JSONL entries with `parentId` — `docs/session-format.md`; opencode forks from any message via a per-message action menu)
- [ ] Timeline: jump-to-message picker that live-scrolls the transcript as you browse (opencode `dialog-timeline.tsx`)
- [ ] Undo last message: abort turn, revert file changes, restore the prompt text into the input for editing (opencode `routes/session/index.tsx:615` — the input restore is what makes it feel good)
- [x] Compaction: summarize old turns when context fills (pi settings: `compaction: {reserveTokens, keepRecentTokens}`; opencode `/compact`) — `/compact` manually; auto-compacts proactively at 90% of the provider-advertised context_length (GET /models, cached in ~/.loopy/models.json) plus retries once when the provider errors with context_length_exceeded; `/compact <model> [provider]` picks the summarizer (else the current model); kept tail never orphans a tool_call from its result
- [ ] Token/cost tracking per session (pi models.json carries `cost: {input, output, cacheRead, cacheWrite}`)
- [ ] Export transcript to markdown with include-options dialog (opencode `/export`, `ui/dialog-export-options.tsx`)

## Agent loop

- [x] `/goal <text>` (codex-style): keep driving turns until the model verifies and explicitly declares `GOAL_MET` — continuing is the default, so it can't terminate early like claude's; `/goal resume` re-engages (also after `/resume` of a session — goals persist), `/goal clear` drops, 20-round cap pauses with a resume hint

- [x] Parallel tool-call execution with per-path file mutation lock (pi: `withFileMutationQueue`, `executeToolCallsParallel`) — `agent.runTools` fans a tool-call batch out to goroutines; write/edit serialize through a per-canonical-path channel semaphore, bash takes a global lock; results land in call order, OnToolStart/End fire per call
- [ ] Retry with backoff on provider errors (pi settings: `retry: {maxRetries, baseDelayMs}`)
- [ ] Streamed partial tool output (bash `onUpdate` throttled at 100ms in pi)
- [ ] Spill truncated bash output to a temp file and mention the path (pi bash tool)
- [ ] Inject `LOOPY_SESSION_ID` / `LOOPY_MODEL` env into bash children (pi injects `PI_*`)

## Skills & subagents

- [x] Skills: scan `.agents/skills/*/SKILL.md` (project) and `~/.loopy/skills/` (user), inject name+description into the system prompt as an `<available_skills>` block; the model reads a SKILL.md with its own read tool when relevant (pi's approach — no skill tool needed, `packages/coding-agent/src/core/skills.ts`)
- [x] Subagents: a `task` tool that runs a self-contained prompt in a fresh agent with the same tools (minus `task` — no recursion) and returns its final report
- [x] `$skill-name` invocation (codex-style) with live completion dropdown; skills re-indexed every turn and every `$` keystroke, so new skills load without restarting the harness
- [ ] Custom agent definitions (`.agents/*.md` with model/tools/prompt frontmatter; opencode agents config `packages/core/src/config/agent.ts`)
- [x] Parallel/background subagents (pi streams tool `onUpdate`; opencode `background-job.ts`) — `task` with `background:true` runs concurrently and reports back via a steered message; a `taskRegistry` keyed by id holds a `Done` channel whose single close broadcasts completion to every waiter; `/tasks` lists them, a `⚙ N bg` header badge shows running count, `/tasks` updates live via `OnChange`
- [ ] `@agent` mentions to target a named subagent (opencode autocomplete)

## Models & providers

- [x] Model → provider routing in config (switch providers without touching models)
- [ ] `anthropic-messages` API style alongside `openai-completions` (pi: `packages/ai/src/api/`)
- [ ] `"$VAR"` / `"!cmd"` resolution for apiKey/header values in config (pi models.json value resolution)
- [x] Reasoning effort: `/effort [off|low|medium|high]` (bare cycles), tab-completes, clickable `⚡` control in the header top-right; sent as `reasoning_effort`, inherited by subagents, survives model switches
- [ ] Per-model sampling params in config (`samplingParams: {temperature, top_p}`)

## MCP

- [x] MCP client: stdio + streamable HTTP servers; config merges claude-style `.mcp.json` and codex-style `~/.codex/config.toml [mcp_servers]` under loopy's own `"mcp"` block (opencode's status model `mcp/index.ts:83-106`, name sanitization + tool bridging `mcp/catalog.ts:47-90,117-119` — with the sanitize-collision fixed via hashed server keys; claude-code's `mcp__server__tool` naming kept). Lazy-with-kickoff connects (close-to-broadcast `ready` chan), per-server call serialization, 30s startup / 60s call timeouts, errors as tool output, `/mcp` status + reconnect/enable/disable, `loopy mcp add|list|remove|serve`
- [ ] MCP resources/prompts (opencode: synthetic `read_mcp_resource` tools + prompts-as-slash-commands)
- [ ] MCP OAuth for remote servers (opencode `oauth-provider.ts` — buffer creds in memory, commit on success; ~800 lines, a `needs_auth` status covers most of the value first)
- [ ] `ToolListChanged` notification → live re-list (opencode `mcp/index.ts:462-471`; needs the standalone SSE stream on remote transports)

## Safety & permissions

- [ ] Permission prompt: Allow once / Allow always / Reject, where "always" previews the exact rule it installs and "reject" takes a free-text redirect message back to the model (opencode `routes/session/permission.tsx`)
- [ ] Command-prefix arity for useful "allow always" rules: `git checkout branch` → rule for `git checkout`, not the whole string (opencode `permission/arity.ts`)
- [ ] Project trust prompt on first run in a directory (pi: `trust.json`, `defaultProjectTrust: "ask"`)

## Theming & config

- [x] ctrl+p command palette (opencode-style): modal dialog (own filter line, esc pops one level, ↑/↓ wraps), category headers, "Suggested" group pinned when the filter is empty, dimmed keybind/slash hints teach shortcuts, cheap subsequence fuzzy filter; fully interactive — rows show live state badges, ←/→ step reversible settings (effort, thinking, mouse) in place, and enter drills into sub-panels (model browser with live preview-switch, effort levels, compaction model, inline goal editor) that apply real changes without leaving the palette
- [ ] Single keybind+command registry: palette, slash commands, help, and footer hints all derived from one table (opencode `config/keybind.ts` — the highest value-per-line idea in that repo)
- [ ] One generic fuzzy-select widget reused by every picker: model, session, theme, timeline (opencode `ui/dialog-select.tsx`)
- [ ] KV table in sessions.db for palette-toggleable UI prefs — no config ceremony per toggle (opencode `context/kv.tsx` pattern)
- [ ] Theme support: JSON themes with named defs + `{dark, light}` variant pairs; a "system" theme built from the terminal's real palette (opencode `theme/index.ts`)
- [x] `"mouse": false` config escape hatch so native terminal selection works (opencode `app.tsx:196`) — also a runtime `/mouse` toggle; with capture on, hold shift to select text in the transcript

## CLI surface

- [ ] Non-interactive one-shot mode: `loopy run "prompt"` — reads piped stdin too, `--format json` emits the raw event stream for scripting (opencode `cli/cmd/run.ts`)
- [ ] `loopy sessions` list subcommand
- [ ] Env markers in child processes (`LOOPY=1`, `LOOPY_SESSION_ID`) so scripts can detect they run under the agent (opencode sets `AGENT=1`, `OPENCODE_PID`)
