# MCP support: clean client + server for loopy

Branch: `mcp-support`

## What this does

loopy becomes an MCP client (stdio + streamable-HTTP servers) whose tools join
the agent loop as first-class tools, and an MCP server (`loopy mcp serve`)
exposing loopy's own read/bash/edit/write to other harnesses. Configuration is
backwards compatible with both **claude-style** (`.mcp.json` project file,
`type: stdio|http|sse`, `command/args/env/headers`) and **codex-style**
(`~/.codex/config.toml` `[mcp_servers.*]`, `command/args/headers/startup_timeout_sec`)
formats, normalized into loopy's own `mcp` block in `~/.loopy/config.json`,
which remains the source of truth on conflict.

## Goal

The best MCP experience of the reference harnesses, the Go way:

1. **Zero-config import**: drop a `.mcp.json` in a repo (claude-style) or have a
   codex config — the tools just appear.
2. **Never blocks the loop**: servers connect concurrently at startup and lazily
   on first tool call; a failed/hung server degrades to an error string in tool
   output, never a dead turn.
3. **Observable**: `/mcp` shows live per-server status (connecting/ready(N
   tools)/failed(err)/disabled), `/mcp <name>` reconnects or toggles.
4. **Safe**: bounded output (existing 50KB cap + truncation marker), per-call
   timeout (default 60s), errors-as-tool-output, context-cancelled on ctrl+c,
   stdio children killed with the process registry on exit.

## Non-goals

- OAuth flows (remote servers with static `headers` auth work; browser OAuth
  dance is out — opencode's `oauth-provider.ts` is 8KB of its own)
- Legacy SSE transport (claude `type: sse` entries: surface as "unsupported
  transport, use streamable http" in `/mcp` status; the SDK and industry moved
  to streamable HTTP)
- MCP resources/prompts/elicitation/sampling (tools only, v1)
- Permission prompts per MCP server (roadmap's permission system is separate)
- Live `ToolListChanged` re-listing (opencode does it; mark `// ponytail:`)

## Design

### New dependency (justified)

`github.com/modelcontextprotocol/go-sdk/mcp` (official SDK, v1.7.0, supports
specs through 2026-07-28). Stdlib covers none of JSON-RPC 2.0 + stdio
framing + streamable HTTP session management; writing it by hand is exactly
the "transliterate, don't port" trap in reverse. `mark3labs/mcp-go` is the
alternative; the official SDK is better maintained and has the cleaner
context-first API (`session.CallTool(ctx, params)`).

For codex TOML: a ~120-line internal TOML subset reader
(`internal/mcp/codextoml.go`) covering `[mcp_servers.NAME]` tables with
string/array/bool/int values — enough for codex configs, no dep. If it grows
past that, revisit.

### Surfaces

| Surface | Files |
|---|---|
| New `internal/mcp` package | `config.go` (normalized ServerConfig + merge), `claude.go` (`.mcp.json` import), `codextoml.go` (codex TOML import), `manager.go` (lifecycle), `tool.go` (tools.Tool bridge), `serve.go` (`mcp serve`), tests |
| Config | `internal/config/config.go` — `MCPServers map[string]config.MCPServer` field on `Config` (JSONC) |
| Agent | `internal/agent/agent.go` — `Agent.Tools` already a slice; TUI appends `mgr.Tools()` after `agent.New`; no agent changes needed |
| TUI | `internal/tui/tui.go` — `/mcp` case in `command()`, status rendering; `internal/tui/mcp.go` — status view + toggle/reconnect; startup kickoff in `Run` |
| CLI | `cmd/loopy/main.go` — `loopy mcp add|list|remove|serve` subcommand |
| Process safety | `internal/mcp/manager.go` `Close()` called from `tui.Run` defer before `bashrun.KillAll`; stdio children get their own process group so close kills the tree |

### Normalized config (loopy-native, absorbs both styles)

```go
// internal/mcp/config.go
type ServerConfig struct {
    // stdio
    Command []string          // command + args (claude: command+args, codex: command+args)
    Env     map[string]string // claude: env, codex: env
    Cwd     string
    // remote
    URL     string
    Headers map[string]string
    // common
    Enabled        *bool  // nil = enabled
    StartupTimeout time.Duration // connect/list-tools budget; codex startup_timeout_sec
    ToolTimeout    time.Duration // per-call budget; codex tool_timeout_sec
}
func (c ServerConfig) Remote() bool { return c.URL != "" }
```

Merge order (later = lower precedence, loopy config always wins):
`claude .mcp.json (cwd)` → `codex ~/.codex/config.toml` → loopy `config.json mcp`.
Same-name entries: loopy's wins whole-entry (no field-level merge — predictable,
and what codex/claude do between their own scopes).

Name sanitization for tool names (opencode `catalog.ts:117`):
`sanitize(s) = replace [^a-zA-Z0-9_-] with _`, tool name =
`mcp_<san(server)>_<san(tool)>` — claude-code uses `mcp__server__tool`; loopy
uses single underscores (opencode-style) since our tool-name charset goes
through providers that dislike long names; document the difference. Keep a
`strings.HasPrefix(name, "mcp_")` check in `tools.Execute` path for mutation
classification (MCP tools never take file locks — side effects unknown, treated
like bash: **no lock**, they run parallel; per-server serialization is the
server's business; but note the global bash lock does NOT cover them).

`// ponytail: per-server call serialization semaphore if a server proves not
concurrency-safe`

### Manager (channel-idiomatic, per docs/concurrency.md)

```go
// internal/mcp/manager.go
type Status int // StatusDisabled, StatusConnecting, StatusReady, StatusFailed
type Server struct {
    Name string
    Status Status
    Err string
    Tools []mcp.Tool // listed defs
}
type Manager struct { /* mu guards map; each server has a ready chan struct{} */ }

func NewManager(cfgs map[string]ServerConfig) *Manager
func (m *Manager) Start(ctx)          // kick concurrent connects (errgroup-style, WaitGroup+semaphore)
func (m *Manager) Tools(ctx) []tools.Tool   // snapshot: ready servers' defs bridged
func (m *Manager) Call(ctx, server, tool string, args json.RawMessage) (string, error)
func (m *Manager) Reconnect(ctx, name string) error
func (m *Manager) Close()             // close sessions, kill stdio children (process group)
func (m *Manager) Statuses() []Server // for /mcp
```

- **Lazy-with-kickoff connect** (opencode connects eagerly in parallel and
  caches defs — `index.ts` state init with `concurrency: unbounded`): loopy
  kicks connects in background at `tui.Run` startup; each server has a
  `ready chan struct{}` closed once on connect settle (success or failure) —
  the close-to-broadcast pattern from `BackgroundTask.Done`. A tool call
  blocks on `<-ready` + ctx, then calls. First turn rarely waits; a hung
  server only stalls calls *to its tools*, capped by StartupTimeout (default
  30s, opencode's DEFAULT_TIMEOUT `index.ts:38`).
- **Session per server** via SDK `client.Connect(ctx, transport, nil)` with
  `CommandTransport{Command: exec.Command(...)}` (stdio) or
  `StreamableHTTPTransport` (remote, `HTTPClient` with header-injecting
  RoundTripper).
- **Tool bridge**: listed `mcp.Tool` defs → `tools.Tool{Def: llm.NewTool(mcpName,
  desc, schemaJSON)}`; `Run` = `session.CallTool` with `context.WithTimeout(ctx,
  toolTimeout)`; result content flattened: text concatenated, image/audio/
  resource → `[image content omitted]`-style placeholders, `structuredContent`
  JSON-appended when no text (`catalog.ts:80` does the same); `IsError` →
  `"Error: <flattened>"`. Output through existing `truncate()`.
- **Disconnect watch** (opencode `watch()`, `index.ts:440`): SDK
  `session.Wait()`/on-close → status failed, defs dropped. `// ponytail:
  auto-reconnect with backoff`
- Stdio children: `exec.Cmd.SysProcAttr.Setpgid = true`, tracked so `Close()`
  kills the group — opencode walks descendants with pgrep (`index.ts:420`);
  loopy's bashrun pattern (process-group kill) is the Go-native equivalent.

### Config import (pure functions, fully unit-tested)

```go
// internal/mcp/claude.go
func ParseClaude(data []byte) (map[string]ServerConfig, error) // {"mcpServers": {...}}

// internal/mcp/codextoml.go
func ParseCodex(data []byte) (map[string]ServerConfig, error)  // [mcp_servers.NAME]

// internal/mcp/config.go
func Merge(loopy, claude, codex map[string]ServerConfig) map[string]ServerConfig
```

Claude field mapping: `type: stdio` (default when `command` present) +
`command/args/env`; `type: http` + `url/headers`; `type: sse` → import as
disabled-with-note. `${VAR}` env expansion in env values (claude does this).
Codex mapping: `command` (string or []string), `args`, `env`/`environment`,
`headers`, `startup_timeout_sec`, `tool_timeout_sec`.

### TUI

- `/mcp` — status table: `name  status  tools  detail` (connecting…/✓ N tools/
  ✗ err/disabled)
- `/mcp <name> reconnect|enable|disable` — reconnect re-runs connect; disable
  persists `Enabled: false` into loopy config via guarded `Config.Save`
- Header badge `mcp:N` when N servers ready? — skip, palette/dock already busy.
  `/mcp` is enough. `// ponytail: palette panel`

### CLI

`loopy mcp list` (merged view with source labels), `loopy mcp add <name> --
<cmd...>` / `--url`, `loopy mcp remove <name>` — all write through
`config.Save` (atomic + clobber guard). `loopy mcp serve` runs the stdio
server (`serve.go`, ~60 lines with the SDK: wrap read/bash/edit/write,
no `task`).

## Prior art citations

- opencode config schema: `packages/core/src/v1/config/mcp.ts:4-52`
- opencode lifecycle/connect/status/watch: `packages/opencode/src/mcp/index.ts:38,226,286,340-414,440-470,503-545`
- opencode tool bridge/naming/timeout/flatten: `packages/opencode/src/mcp/catalog.ts:11,47-90,117-119`
- claude `.mcp.json` shape: `{"mcpServers": {name: {type, command, args, env, url, headers}}}`
- codex `[mcp_servers]` shape: `command, args, env, headers, startup_timeout_sec, tool_timeout_sec`
- pi: no MCP (confirmed — grep finds nothing); nothing to port.

## Test plan

- **Unit (pure)**: `ParseClaude` (all transport variants, env expansion, sse
  note), `ParseCodex` (string vs array command, timeouts), `Merge` precedence,
  `sanitize`/tool-name derivation, content flattening (text/image/structured/
  isError), schema pass-through.
- **Manager integration**: real stdio MCP server in-process (the SDK makes a
  test server ~20 lines — serve `greeter` over a pipe transport): connect →
  tools appear → call → result; kill server → status failed; reconnect.
- **Loop test** (fake provider, `agent_test.go` style): model calls
  `mcp_test_greet`, gets result, continues; server-down variant returns
  `"Error: …"` as tool output and the loop survives.
- **Concurrency**: two parallel calls to the same MCP tool under `-race`;
  ctrl+c mid-`CallTool` cancels (ctx deadline).
- **Resume**: MCP tool calls are plain `llm.ToolCall`s — verify a session with
  MCP results resumes (no special handling, but pin it).
- **Headless TUI**: `/mcp` render, `/mcp x disable` persists config.

## Docs plan

- `docs/features.md`: new "MCP" section (behavior → code → tests)
- `docs/roadmap.md`: add + check MCP entry under "Skills & subagents" or new
  "MCP" section
- `docs/concurrency.md`: the per-server `ready chan struct{}` pattern if it
  teaches something new (it's `Done`-broadcast again — likely one line)
- README: `loopy mcp` CLI surface + config example

## Task breakdown (status)

1. [x] `internal/mcp/config.go` + `claude.go` + `codextoml.go` + unit tests
2. [x] Config plumbing: `Config.MCPServers`, load/merge in `tui.Run`
3. [x] `manager.go` + integration tests (in-process SDK server)
4. [x] TUI `/mcp` + startup kickoff + `Close` wiring
5. [x] CLI `loopy mcp` subcommands + `serve.go` (self-host test passes)
6. [x] Docs (features.md section, roadmap entry, README snippet, concurrency.md §3)
7. [x] `task check` + `go test -race ./...` green; adversarial review done + fixed

## Adversarial review findings → fixes

1. **Resume/model-switch dropped MCP tools** (dead captured agent in OnChange
   closure) → closure dereferences `m.agent` at call time; `wireTasks` re-pushes
   the tool set into the new agent. Also introduced `Agent.SetMCPTools` —
   MCP tools live in their own mutex-guarded slice so a settle mid-turn can
   never race the slice a request is reading.
2. **`/mcp disable` on imported servers persisted a bare `{enabled:false}`**
   that shadowed the import and lost the command/url → the manager exposes
   `Config(name)`; the toggle copies the full live definition into loopy's
   config. Disable now also tears down the live session; `run` refuses
   reconnects on disabled servers.
3. **TOML reader bugs** (escape-blind `#` comment stripping, `\`-before-quote,
   `[mcp_servers."foo.env"]` dropped, env inline/sub-table silent conflicts,
   quoted dotted table names mangled) → all fixed + regression tests in
   `adversarial_test.go`.
4. **`Close()` could hang exit up to 60s/server** (SDK session Close waits for
   in-flight requests) → sessions close concurrently under a 5s total bound;
   `closed` flag stops post-Close session stores and silences watcher noise.
5. Nits: deduped `truncate` (`tools.Truncate`), dropped dead `validateAll` +
   `byKey` + `args0` field, `__` in server names now hash-suffixed, queued
   reconnect after a successful in-flight connect is skipped.

## Deviations from the original sketch

- **Naming: claude-code's `mcp__server__tool` (double underscore), not
  single.** Single-underscore split is unrecoverable when tool names contain
  `_` (they do). Server keys with unsafe chars get an fnv hash suffix so
  opencode's sanitize-collision class ("a.b" vs "a b") can't collide, and keys
  never contain `__`, keeping the split unambiguous. This is *more*
  claude-compatible than planned.
- **Per-server call serialization semaphore** (1-cap channel) — promoted from
  ponytail: many stdio servers are single-request-at-a-time.
- **SDK StreamableClientTransport with `DisableStandaloneSSE: true`** — v1 is
  request/response only; no server-initiated notifications (ponytail).
- **Config clobber-recovery fix** (internal/config): Load used to treat any
  providers-empty config as clobbered and regenerate defaults, silently wiping
  an mcp-only config. Now preserves MCP entries. Found by
  TestMCPTogglePersists.
- **SDK quirk pinned**: an SDK server `Run()`ning an in-memory transport exits
  at client disconnect and won't accept a second connection; tests use
  `Server.Connect` per attempt. Real stdio servers respawn per connect, so
  production reconnect is unaffected.
- `OnChange` rebuilds `ag.Tools` from a captured base length so late-connecting
  servers appear without a restart (was: only initial snapshot).

## Open questions (defaults assumed unless corrected)

1. Import scope: `.mcp.json` + codex TOML auto-imported; `~/.claude.json` NOT
   (giant state file). ✓?
2. `loopy mcp serve` in v1. ✓?
3. Tool name prefix `mcp_server_tool` (single underscore) vs claude's
   `mcp__server__tool`. Chose single — ✓?
4. MCP tool results: images flattened to placeholders, not vision-fed. ✓?
