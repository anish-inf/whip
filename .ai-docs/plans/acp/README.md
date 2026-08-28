# ACP agent mode: `whip acp`

Branch: `feat/acp-agent`

Serve whip as an **Agent Client Protocol** agent over stdio, so ACP clients
(Zed, and any other editor speaking ACP v1) can drive whip's agent loop.

## What this does / Goal

`whip acp` runs a headless, long-lived stdio process: JSON-RPC 2.0 in on
stdin, responses + `session/update` notifications out on stdout. The client's
`session/prompt` becomes an `agent.Agent.Turn`; streamed text, tool calls,
and permission prompts become ACP updates. Sessions persist in whip's SQLite
store, so an ACP session is resumable from the TUI and vice versa.

Success test: point Zed at `whip acp` as an external agent; chat, watch
streaming + tool cards, approve a permission prompt, esc-cancel a turn,
restart and resume the session.

## Non-goals

- **No terminal capability.** whip's bash tool already executes commands; the
  ACP terminal suite exists for agents that *can't*. Running agent commands
  in the editor's shell is actively worse (escapes the process registry).
  Punt until a real client need appears.
- **No fs/read_text_file|write_text_file usage** (client capability): whip's
  read/write tools touch disk directly, like claude-code's ACP adapter.
- **No `authenticate`** (whip auth is provider config, not per-connection) —
  advertise `authMethods: []`; `Authenticate` returns method-not-found.
- **No elicitation**; **no config options** (v2 can map effort/model).
- **ACP v2** (draft) — v1 only (`protocolVersion: 1`).
- No TUI changes at all.

## Key design decisions

### 1. Use `github.com/coder/acp-go-sdk` (v0.13.5)

New dependency — justified: the protocol surface is ~9.5k lines of generated
union types with exact wire shapes (discriminated `SessionUpdate`,
`ToolCallContent`, permission outcomes), and hand-rolling them is precisely
the error-prone part. The SDK is **dependency-free** (its go.mod is one
line), tracks schema v1 (`ProtocolVersionNumber = 1`), and provides:

- `Agent` / `AgentLoader` interfaces — we implement:
  `Initialize`, `NewSession`, `Prompt`, `Cancel`, `LoadSession`,
  `Authenticate` (→ method-not-found), `SetSessionMode` (→ method-not-found).
  (`AgentExperimental` etc. left unimplemented.)
- `acp.NewAgentSideConnection(agent, os.Stdout, os.Stdin)` — owns framing,
  dispatch (requests on goroutines), cancel plumbing.
- Outbound client calls: `conn.SessionUpdate`, `conn.RequestPermission`
  (plus `ReadTextFile`/`CreateTerminal`/… we won't call).
- Helpers used: `acp.UpdateAgentMessageText`, `acp.UpdateThoughtText`,
  `acp.StartToolCall(...)`/`acp.UpdateToolCall(...)` option-builders,
  `acp.ToolContent(acp.TextBlock(...))`, `acp.UpdatePlan`,
  `acp.AvailableCommands(...)`, `acp.NewRequestPermissionOutcomeSelected`.

Prior art: SDK repo `example/agent/main.go` (session registry, per-session
cancel func, prompt→turn→stopReason mapping) — ported, not transliterated.

Alternative considered and rejected: hand-rolled JSON-RPC à la
`internal/lsp/client.go`. LSP needs ~5 methods; ACP's union-heavy schema
(content blocks, 10+ session-update variants, tool-call content kinds) makes
the SDK the ponytail-correct choice. go.mod gains exactly one require.

### 2. Surfaces and files

| File | Role |
|---|---|
| `cmd/whip/acp.go` | `whip acp` subcommand: config load, model resolve, `agent.New`, `acp.NewAgentSideConnection(bridge, os.Stdout, os.Stdin)`, block on `conn.Done()`. Flag: `-m/-p` like `run`. |
| `internal/acp/bridge.go` | `Bridge` — the `acp.Agent` impl. Session registry (`map[acp.SessionId]*acpSession`), method handlers, stop-reason mapping, `session/set_mode`. |
| `internal/acp/translate.go` | Pure functions: ACP content blocks → (prompt string, []llm.ContentPart); whip `agent.Events` → `acp.SessionNotification`s; tool name/args → `ToolKind` + title + locations; tool result → `[]acp.ToolCallContent` (incl. `diff` blocks for write/edit, from the tool's unified-diff output). |
| `internal/acp/permission.go` | `tools.Gate` adapter: whip `GateRequest` → `conn.RequestPermission` (allow_once/allow_always/reject options) → `GateDecision`. |
| `internal/agent/agent.go` | +1 exported variant: `TurnParts(ctx, input, parts, authored, ev)` (the unexported `turn` already takes parts — one-line export, mirrors `TurnWithImages`). |

Reuse, deliberately not reinvented: `agent.Agent` loop + `Events` hooks,
`session.Store` (Create/Load/Save), `tools.Gate` (the *same* seam the TUI
uses for consent), `config.Load`/`Resolve`, `systemPrompt()`,
`bashrun.KillAll` on exit, MCP/LSP wiring lifted from the TUI startup block
into a shared helper if it doesn't already exist (check `cmd/whip/main.go`
startup; extract `newAgent(cfg, model, provider)` rather than duplicating).

### 3. Lifecycle mapping (against the v1 schema — see protocol-notes.md)

- `initialize` → `InitializeResponse{ProtocolVersion: 1` (echo the client's
  version if supported, else our latest — the SDK type is `ProtocolVersion
  int`), `AgentCapabilities{LoadSession: true, PromptCapabilities: {Image:
  true, Audio: false, EmbeddedContext: true}, McpCapabilities: {HTTP: true,
  SSE: false}, SessionCapabilities: {List: {}}`, `AgentInfo{Name: "whip",
  Version: version}`, `AuthMethods: []}`.
  - `image` gated on the resolved model's vision capability (config catalog);
    `embeddedContext: true` because text embedded resources are free to
    support; `audio: false` (whip's llm has no audio parts).
  - `mcpCapabilities.http: true` — whip's MCP client speaks streamable HTTP
    already; stdio servers are a baseline MUST (accept + merge, below).
- `session/new {cwd, mcpServers}` → `store.Create(cwd, model, provider)`;
  build `agent.Agent` with the system prompt rooted at **params.Cwd**, not
  process cwd (the editor spawns whip wherever it likes; `systemPrompt()` in
  cmd/whip takes wd — refactor to accept it). MCP servers from params are
  converted to `mcp.ServerConfig` (`Command []string`, `Env map`) and merged
  into the session's manager on top of whip's own config (whip config wins on
  name clash — log a note). Return `sessionId` = whip's store id, and
  **`modes`** (cheap and high-value, see below).
- **Session modes = permission postures** (ported from claude-code's ACP
  adapter, which maps its permission modes onto ACP modes):
  - `auto` (default) — `tools.Gate = nil`: whip's current headless posture,
    tools run ungated (claude-code's "bypassPermissions"/`whip run`
    equivalent).
  - `ask` — `tools.Gate` → `session/request_permission` with options
    `{allow_once "Allow once", allow_always "Always allow <rule>", reject_once
    "Reject"}`; outcome→`GateDecision` (cancelled outcome = reject with
    "the user cancelled the prompt").
  - `session/set_mode` flips the posture live (idle or mid-turn; takes effect
    on the next gated call) and answers `{}`. The agent echoes changes with
    `current_mode_update` — field is **`currentModeId`** per schema (docs
    page says `modeId`; schema wins).
- `session/load {sessionId, cwd}` → `store.Load`; rebuild agent with
  messages; **replay the entire history as `session/update` notifications
  BEFORE responding** (spec ordering): each stored user message →
  `user_message_chunk`, assistant → `agent_message_chunk`, and each stored
  tool call → a `tool_call` update in its terminal state
  (`completed`/`failed` from the result's `"Error: …"` prefix; title/kind/
  locations/rawInput re-derived from the stored call via the same
  translate.go helpers the live path uses — one code path, no drift).
  `llm.Message` already carries tool calls + results, so no store changes.
  Respond `{}` (schema-conformant; docs' `null` is older style).
- `session/prompt {sessionId, prompt[]}` →
  - Convert blocks (baseline MUSTs: `text` + `resource_link`; plus
    capability-gated `image`, `resource`): `text` → joined with `\n\n`;
    `resource_link` → `\n@<uri>`-style mention (mirrors whip's `@file`
    convention: note the path, agent reads with its own tools);
    `resource` text → fenced block with the uri as caption; blob →
    placeholder note; `image` → `llm.ImagePart` (mimeType + raw base64) when
    the model has vision, else a `[image: <mimeType>]` text placeholder.
  - **Prompt-while-busy errors** (built as FIFO queue per sign-off, then cut
    on follow-up request): a prompt arriving mid-turn gets a JSON-RPC
    "session busy" error. ACP clients serialize turns; queued prompts invite
    zombie work nobody reads. `session/cancel` interrupts the running turn;
    an idle cancel is a no-op and doesn't poison the next prompt.
  - Register `ctx, cancel` on the session; run `ag.TurnParts` (the new
    export); map outcome: nil → `end_turn`; `ctx.Canceled` → `cancelled`
    (**never** an error response — spec MUST); other error → JSON-RPC error.
  - Events wiring (all → `conn.SessionUpdate` with the session id):
    `OnText` → `agent_message_chunk`; `OnThink` → `agent_thought_chunk`;
    `OnToolStart` → `tool_call` (`status: in_progress` once running, kind
    from the tool-name map — read/bash→execute, write/edit→edit,
    task→think, browser/computer→other; `locations` from the path arg;
    `rawInput` = the args JSON); `OnToolEnd` → `tool_call_update`
    (`completed`, or `failed` when the result is an `"Error: …"` string;
    content = text block + a `diff` content entry for write/edit built from
    oldText/newText, which the tools already produce as a unified diff —
    translate.go reconstructs old/new from the diff or we thread it through).
  - **Plan updates**: whip's `Todos` already rewrite in full each todowrite —
    emit `plan` updates (complete entry list, spec requires wholesale
    replacement) with `priority: medium` (whip todos have no priority) and
    status mapped 1:1. Hook: `agent.Events` has no OnTodo today — add one
    (small) or poll between rounds; prefer a hook.
  - **`usage_update`** from `OnUsage`: `used` = last prompt tokens, `size` =
    `ContextLimit` (only when advertised); cost from `llm.SessionCost` when
    pricing is known. Cheap, editors show it; include.
  - End of turn: `store.Save(id, from, msgs)` exactly like `run.go`; send
    `session_info_update` with the auto-title once it exists.
- `session/cancel` (notification) → cancel the session's turn ctx. The SDK
  dispatches notifications while `Prompt` is blocked; whip's ctx flows into
  HTTP + tools, so cancellation is real. Also: the SDK answers
  `$/cancel_request` (protocol-level) itself — nothing for us to do.
- Permission detail: with mode `ask`, the gate runs **on the tool goroutine**
  inside `Turn` and blocks on the client's answer; concurrent gated tools
  issue concurrent `request_permission` calls (the SDK multiplexes ids).
  The gate is installed per-session (not the package-global `tools.Gate`,
  which the TUI may own in another process — ACP mode is its own process, but
  keep the install/restore scoped to the prompt turn anyway). Background
  subagents gated mid-turn inherit the gate: acceptable (the prompt hits the
  editor) — record as known behavior.
- Teardown: stdin EOF → SDK closes `conn.Done()` → run `mcpMgr.Close()` +
  `bashrun.KillAll()` (same order as the TUI exit path) → exit 0.

### 4. Concurrency (per docs/concurrency.md)

- One `acpSession` struct per session: `{ag *agent.Agent, storeFrom int,
  cancel context.CancelFunc, turnCh chan struct{}}`. `turnCh` is a
  1-capacity channel token (the filelocks idiom): `Prompt` acquires
  non-blocking (busy = "session busy" error), runs its turn, releases.
- The SDK dispatches inbound requests on goroutines; all `SessionUpdate`
  calls go through the SDK's single writer (no locks on our side). Our own
  shared state is the session map (mutex) + per-session turn token.
- `session/cancel` must work *while* `Prompt` blocks on `Turn` — the SDK
  guarantees dispatch-during-prompt; the test suite proves it. Cancel hits
  the running turn; an idle cancel is a no-op (pinned at the wire level —
  the SDK's client wrapper masks it).

## Test plan

- `internal/acp/translate_test.go` — content-block conversion (text join,
  resource_link, embedded resource text/blob, image w/ and w/o vision),
  tool-call kind/title/location derivation, diff content for edit/write.
- `internal/acp/bridge_test.go` — in-memory `acp.NewAgentSideConnection`
  over pipes (mirror the SDK's `example_client_test.go` harness) + the
  `agent_test.go` fake-provider httptest pattern:
  - initialize → capabilities; new → prompt(streaming text + tool call
    updates arrive in order) → stopReason end_turn.
  - cancel mid-turn → stopReason `cancelled`, turn actually stops, and the
    prompt response is a **result**, never an error.
  - permission in `ask` mode: fake client answers `request_permission` with
    allow_always/reject; assert the tool ran / the model got
    "Permission denied: …"; set_mode → `auto` mid-session ungates.
  - load: create+prompt, new bridge, `session/load` → replay updates arrive
    **before** the load response + conversation continues.
  - prompt-while-busy queues FIFO and runs after the current turn ends.
- `go test -race ./...` — the cancel-during-prompt and parallel-session
  paths are the interesting ones.
- Manual: Zed `settings.json` external agent smoke test (documented in the
  features.md section).

## Docs plan

- `docs/features.md` — new "ACP agent mode" section (behavior → code →
  tests, incl. the Zed config snippet).
- `docs/roadmap.md` — add + check the ACP entry.
- README — one line: editors can use whip via `whip acp`.

## Task breakdown

1. [x] `task tidy` + add `github.com/coder/acp-go-sdk@v0.13.5` (zero
   transitive deps).
2. [x] `internal/acp/translate.go` + tests (content blocks, tool cards,
   replay).
3. [x] `internal/acp/bridge.go`: initialize/new/prompt(queue)/cancel +
   stopReason mapping + session registry + per-session MCP merge.
4. [x] Modes (`auto`/`ask`) + permission bridge (`tools.Gate` ↔
   `RequestPermission`, bridge-wide serialized, per-session always-rules,
   turn-ctx-scoped prompts) + `session/set_mode` + tests.
5. [x] `session/load` + replay-before-response (messages AND tool cards) +
   incremental persistence (storeFrom starts at 1 — system prompt never
   persisted) + exact-id/cwd checks + tests.
6. [x] `cmd/whip/acp.go` wiring (config, model resolve, `systemPrompt(wd)`
   refactor, per-session MCP manager, LSP, CloseAll + KillAll exit path).
7. [x] `plan` updates (`Agent.SetOnTodos` hook), `usage_update`
   (per-request tokens, not cumulative), `session_info_update` title.
   `available_commands_update` cut — slash commands don't translate.
8. [x] Adversarial review pass (10 findings fixed: storeFrom off-by-one,
   close capability + CloseAll teardown, load-failure leak, prefix-id load,
   allow-always rules, gate serialization, permission ctx, per-request
   usage, max_tokens stop reason, zombie queued turns) + regression tests
   for each. `task check` green except pre-existing environmental failures
   (`internal/browser` sandbox-Chrome, both reproduce on clean tree);
   `-race` green on acp + cmd/whip.

## Deviations from the signed-off plan

- **Turn ctx is decoupled from the request ctx** (`context.Background()` +
  explicit `s.cancel`): the SDK's `AgentSideConnection` cancels a session's
  in-flight prompt ctx when a second prompt arrives, and its client-side
  `Prompt` auto-sends `session/cancel` + substitutes the stop reason when
  the request ctx dies. Cancellation flows through `session/cancel` →
  `Bridge.Cancel`; `CloseAll` covers client disconnect. The idle-cancel
  no-op behavior is pinned at the wire level (`TestWirePromptAfterIdleCancel`)
  because the SDK client wrapper can't observe it faithfully.
- **Queueing cut after sign-off**: prompt-while-busy was FIFO-queued at your
  request, then reverted to busy-error on your follow-up. The turn-token
  channel stayed; the queue went.
- **Gate serialization**: `tools.Gate` is package-global, so ask-mode turns
  serialize bridge-wide on `gateMu` rather than risk cross-session
  mislabeled prompts.
- Modes ended up IN scope (sign-off conversation), and `session/list` +
  `session/close` too (cheap with the store).

## Open questions for sign-off

1. **SDK dependency OK?** `github.com/coder/acp-go-sdk@v0.13.5` — official
   schema-generated types, zero transitive deps. The hand-rolled alternative
   (à la `internal/lsp`) is ~10× the code for the union-heavy schema.
2. **Scope**: initialize / new / load / prompt / cancel + streaming text,
   thought chunks, tool-call cards, plan, usage, session modes (auto/ask →
   permission bridge), session/list. Deferred: terminals, fs/*, elicitation,
   auth, config options, ACP v2. Pull anything in or push anything out?
3. **Load replay**: messages only in v1 (tool calls not replayed as cards) —
   acceptable? Full tool-card replay needs mapping stored tool metadata to
   ToolCallIds; doable but adds a chunk of translate code.
4. **Prompt-while-busy = error** (vs. steer/queue)? Recommend error to start
   — the spec lets the client serialize turns and Zed does.
5. **MCP servers from the client merged per-session** (whip config wins name
   clashes) — or ignore client MCP servers entirely for v1?
