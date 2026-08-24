# LSP support: diagnostics in tool output via stdlib Go client

Branch: `feat/lsp-diagnostics`

Linear: INF-4989 (parent of INF-4991, @-mention symbol expansion — NOT this plan)

## What this does

loopy gains an LSP client. When the model edits or writes a source file that
an LSP server covers, the tool output now includes compile/lint diagnostics
(errors from the edited file **and sibling files the edit broke**), so the
model sees and fixes breakage in the same turn instead of spending a `go
build` round-trip.

- `internal/lsp`: stdlib-only JSON-RPC/LSP client over stdio (~350 lines).
  No new dependency: LSP framing is `Content-Length` headers + JSON, covered
  by `encoding/json` + `bufio` + `os/exec`.
- Server registry is **data, not code**: `gopls` built-in (root = nearest
  `go.work`/`go.mod`/`go.sum`); a `lsp` block in `config.json` (same shape as
  `MCPServer`) makes any other server user-configurable. Adding a built-in
  later = one table row.
- Diagnostics are **wait-free**: `didOpen`/`didChange` carry a version; the
  server pushes `publishDiagnostics`; a per-file-version channel `close`
  wakes waiters (opencode polls with timeouts — ours is one `close`).
- Bounded: capped wait (~1.5s) inside the `write`/`edit` tool Run; timeout or
  ctrl+c returns the tool result without diagnostics, never a dead turn.

## Goal

The best LSP-in-harness experience of the reference harnesses, the Go way:

1. **Diagnostics in-band, zero turns burned** — write/edit output gains a
   `<diagnostics>` block the model can act on immediately (opencode does
   this; we match it).
2. **Cross-file errors** (the 10x over opencode): opencode renders only the
   touched file's diagnostics (`tool/edit.ts:199-201`). gopls reports errors
   the edit caused in *other* files of the package; we append those too.
3. **Never blocks the loop**: lazy spawn on first covered file touch, broken
   servers remembered, wait capped, everything ctx-cancellable.
4. **Zero new dependencies** and ~1/10th opencode's line count (their
   `src/lsp/` is ~3,300 lines of TS; their two npm protocol packages become
   stdlib here).

## Non-goals

- Semantic navigation tools (`lsp_definition`/`lsp_references`/`hover`).
  Ponytail verdict: cross-file diagnostics deliver the value; nav tools are a
  follow-up ticket if users ask. The client's `request()` primitive makes
  them ~30 lines when wanted.
- `@file.go#120` symbol-range expansion — separate sub-issue **INF-4991**
  (needs `documentSymbol`, small add-on after this lands).
- Pull diagnostics (`textDocument/diagnostic`, dynamic registration).
  Push diagnostics cover gopls/rust-analyzer/ts-server; pull is the
  `// ponytail:` mark.
- Formatting, code actions, rename, completion, hover.
- Auto-installing language servers (opencode's ~2k-line `server.ts` does
  install orchestration; we exec what's on PATH and remember failures).
- Formatters-as-diagnostics (eslint/biome/deno-style double-reporting).

## Prior art

Research distilled in `docs/learnings/other-harnesses/opencode/lsp.md`
(written alongside this plan). Key citations into
`~/code/coding-harnesses/opencode/packages/opencode/src/`:

- **Model surface**: `tool/edit.ts:197-202`, `tool/write.ts:75-76` —
  `touchFile(path, "document")` then append `Diagnostic.report`; errors-only,
  max 20/file, `<diagnostics file="…">ERROR [l:c] msg</diagnostics>`
  (`lsp/diagnostic.ts`). We port this block format verbatim (models may
  already know it).
- **Spawn/registry**: `lsp/lsp.ts:141-200` `getClients` — ext match → root →
  spawn, `broken` set for failed spawns, inflight dedup. Ours: same flow,
  `map[root+id]clientState` + per-key spawn channel instead of Promise map.
- **Diagnostics wait**: `lsp/client.ts:160-170` + `waitForDiagnostics` — TS
  polling/timeout machinery. Ours: close-to-broadcast on
  (uri, version) from `docs/concurrency.md`.
- **Read warm-up**: `tool/read.ts:119` — forked `touchFile` to warm the
  server. Deferred: our `read` doesn't mutate files; warming costs a goroutine
  and an LSP spawn for files the model may never edit. Add if diagnostics
  latency on first edit proves annoying (mark in plan as considered-and-cut).

## Design

### Surfaces

| Surface | Files |
|---|---|
| New `internal/lsp` package | `client.go` (protocol: framing, dispatch, request/notify, wait), `manager.go` (registry, spawn-on-demand, didOpen/didChange, diagnostics cache), `diagnostic.go` (types + `<diagnostics>` rendering), `fake_test.go` (~120-line in-process LSP server) + unit tests |
| Tools | `internal/tools/tools.go` — `tools.LSP *lsp.Manager` package hook (mirrors `tools.InteractiveBash` pattern); `write`/`edit` append diagnostics block after success |
| Config | `internal/config/config.go` — `LSPServers map[string]LSPServer` field (`json:"lsp,omitempty"`), config-file form mirroring `MCPServer` |
| TUI | `internal/tui/tui.go` — build manager next to MCP block (same cwd gate), `/lsp` case (status view, sibling of `/mcp` at tui.go:2656), `Close` on exit; `internal/tui/lsp.go` — status view |
| Docs | `docs/features.md` section, `docs/roadmap.md` checkbox, `docs/learnings/other-harnesses/opencode/lsp.md` (research, landing alongside) |

No `internal/agent` changes: diagnostics ride inside tool output strings, the
loop never sees them. No session changes: nothing persisted.

### Client protocol core (`internal/lsp/client.go`)

```go
// client is one LSP server process speaking JSON-RPC over stdio.
type client struct {
    cmd    *exec.Cmd
    out    chan []byte            // writer pump owns stdin (docs/concurrency.md)
    nextID atomic.Int64
    pend   sync.Map               // id -> chan rpcResponse (capacity 1)
    onDiag func(uri string, version int, diags []Diagnostic)
    // root, serverID, capabilities from initialize
}

func (c *client) request(ctx context.Context, method string, params any, out any) error
func (c *client) notify(method string, params any) error
func (c *client) close() error // shutdown/exit, then kill; own process group
```

- One read goroutine: `bufio.Reader` parses `Content-Length` frames, decodes
  JSON; messages with `id` → look up `pend`, deliver on the cap-1 channel;
  `method == "textDocument/publishDiagnostics"` → `onDiag`; server→client
  requests (`window/workDoneProgress/create`, `workspace/configuration`,
  `client/registerCapability`) → reply `null`/`[]` minimal acks (opencode
  answers these the same way, `client.ts:173-208`).
- Writes: all serialized through `out` (buffered chan); a pump goroutine owns
  stdin. No write locks.
- `request` registers the pending channel before enqueueing the write; a
  `close(ch)` on client death resolves every waiter with an error (no
  leaks — proven by goleak-style counting in tests via `-race` + goroutine
  counts).
- `initialize` params: `rootUri`, `processId`, capabilities =
  publishDiagnostics + sync open/change full (opencode's set,
  `client.ts:220-251`, minus pull-diagnostic registration). Initialize budget
  10s.

### Manager (`internal/lsp/manager.go`)

```go
type serverSpec struct {
    command    []string
    extensions []string          // e.g. [".go"]
    rootMarkers []string         // e.g. ["go.work", "go.mod", "go.sum"]
    env        map[string]string
}

type Manager struct {
    specs   map[string]serverSpec // built-ins merged under config overrides
    clients map[string]*client    // key: serverID + "\x00" + root
    broken  map[string]bool       // same key shape
    spawning map[string]chan struct{} // close-to-broadcast inflight dedup
    mu      sync.Mutex            // maps only; never held across I/O
    // per-file document state, keyed by absolute path:
    docs  map[string]*doc        // {client, version}
    // diagnostics cache: map[absPath][]Diagnostic + (path,version) wait chans
}

func (m *Manager) WaitDiagnostics(ctx context.Context, path string, wait time.Duration) []FileDiagnostics
func (m *Manager) Statuses() []Status
func (m *Manager) Close()
```

- `WaitDiagnostics` flow (the only entry point v1 uses): resolve ext → spec →
  root (walk up from file dir for markers); spawn-or-reuse client (inflight
  dedup via `spawning[key]` channel close); `didOpen` (full text, version 1)
  or `didChange` (full content, version++); then `select` on the
  (path,version) diag channel, ctx.Done(), and a `wait` timer. Cache ALL
  pushed diagnostics keyed by path — that's what makes cross-file reporting
  free.
- Cross-file: after wait, collect cached diagnostics for files in the same
  directory tree as the edited file (ponytail scope: same directory only;
  `// ponytail: widen to module root if needed`), excluding the edited file
  itself, errors only.
- Spawn failures → `broken[key]`, all future touches on that key are no-ops.
- Context: `ctx` threads in from tool Run; ctrl+c cancels the wait, never
  leaves a waiter goroutine (waiters are `select`-only, no goroutine per
  wait).

### Diagnostics block (`internal/lsp/diagnostic.go`)

Port opencode's format exactly (`lsp/diagnostic.ts`):

```
<diagnostics file="abs/path.go">
ERROR [12:5] undefined: foo
WARN [30:1] x declared and not used
</diagnostics>
```

Plus our addition when sibling files have errors:

```
<diagnostics file="other.go">
ERROR [42:9] cannot use x (int) as string
</diagnostics>
This edit introduced errors in other files; fix them too.
```

Errors + warnings for the edited file (opencode shows errors only; warnings
are nearly free tokens and catch vet-style noise — flag: keep errors+warnings,
max 20/file, `... and N more`), errors-only for sibling files, max 5 sibling
files, whole block capped well under `maxOutput`.

### Tools integration (`internal/tools/tools.go`)

```go
// LSP, when non-nil, feeds write/edit diagnostics back to the model.
// Installed by the TUI at startup; nil in tests and headless runs.
var LSP interface {
    WaitDiagnostics(ctx context.Context, path string) string // "" when none/unavailable
}
```

(Interface-in-tools, implementation in `internal/lsp` — tools stays import-
light like it does for `InteractiveRunner`.) `write`/`edit` Run, after the
existing success message: `if LSP != nil { out += LSP.WaitDiagnostics(ctx, a.Path) }`.
The manager owns the 1.5s cap internally; the tool just passes ctx. Errors
from LSP never fail the tool (diagnostics absence == no block appended).

### Config (`internal/config/config.go`)

```go
// LSPServers mirrors the mcp block: loopy-native server definitions that
// override/extend the built-in gopls entry. Config is a leaf; lsp converts.
LSPServers map[string]LSPServer `json:"lsp,omitempty"`

type LSPServer struct {
    Command    []string          `json:"command,omitempty"`
    Extensions []string          `json:"extensions,omitempty"`
    RootMarkers []string         `json:"rootMarkers,omitempty"`
    Env        map[string]string `json:"env,omitempty"`
    Enabled    *bool             `json:"enabled,omitempty"`
}
```

`"lsp": false`-style kill switch: an entry `{ "gopls": { "enabled": false } }`
disables the built-in; absence of config = gopls active when on PATH.

### TUI

- Manager built in the same block that builds `mcpMgr` (tui.go:253-275):
  cwd-gated, `Close` next to `mcpMgr.Close()` (tui.go:339-340).
- `/lsp` command: per-server rows `gopls  connected (root: …)` /
  `not started` / `failed: <err>`, modeled on `mcpCommand` — v1 is a status
  view only, no toggle subcommands (`// ponytail: add /lsp <name> restart
  when someone needs it`).

## Concurrency notes (per docs/concurrency.md)

- Writer pump: one buffered `out` channel owns stdin — no write mutex.
- Pending requests: `sync.Map` id→cap-1 chan; client death closes a broadcast
  channel that a single goroutine fans out into pending-channel closes.
- Spawn dedup: `spawning[key] chan struct{}`; losers `<-ch`, winner closes.
- Diagnostics waiters: `map[waitKey][]chan struct{}` — `publishDiagnostics`
  closes them all; no per-waiter goroutines.
- Every goroutine (reader, writer pump, process reaper) has an owner
  (`client`) and an exit (stdin EOF / `close()`). Whole package passes
  `go test -race`.

## Test plan

- `internal/lsp/fake_test.go`: in-process fake LSP server on pipes —
  parses frames, answers `initialize`, records didOpen/didChange, pushes
  scripted `publishDiagnostics` (including delayed + multi-file). Shared by
  all package tests; no real gopls anywhere.
- Unit: frame parsing (split frames, garbage), request/response routing,
  notification dispatch, version-chan wake (publish before wait AND wait
  before publish), spawn-dedup (N concurrent touches → 1 process), broken
  caching, root walk, diagnostics render (cap, severity mapping, sibling
  block).
- Tools-level: install a stub `tools.LSP`, run `write`/`edit`, assert block
  appended and truncation respected; nil LSP = unchanged output.
- Loop test: extend the existing fake-provider pattern in `agent_test.go` —
  model edits a file, fake LSP pushes an error, assert the tool result fed
  back contains `<diagnostics`.
- Concurrency proof: parallel writes to the same file through the agent's
  per-path semaphore + parallel writes to different files → single didChange
  stream, race-clean, waiter wakeups counted.
- Cancel test: ctx cancelled mid-wait returns promptly, no leaked goroutines.

## Docs plan

- `docs/features.md`: new "LSP diagnostics" section (behavior → code →
  tests), naming the tests above.
- `docs/roadmap.md`: check/annotate LSP entry (add if absent).
- `docs/learnings/other-harnesses/opencode/lsp.md`: research report (in
  flight via background task).
- `docs/concurrency.md`: add the (uri,version) close-to-broadcast waiter
  pattern only if it differs from what's already documented — likely one
  sentence under the existing close-to-broadcast entry.
- README: one line under features if we list tool behaviors there.

## Tasks (ordered)

- [x] 1. `internal/lsp/client.go` + frame/dispatch unit tests (fake server)
- [x] 2. `internal/lsp/manager.go` — registry, root walk, spawn dedup,
      didOpen/didChange, waiter wake; unit tests
- [x] 3. `internal/lsp/diagnostic.go` — types + render; unit tests
- [x] 4. `internal/config` `lsp` block + manager merge; config tests
- [x] 5. `tools.LSP` hook + write/edit integration; tools tests
- [x] 6. TUI: manager lifecycle + `/lsp` status view; headless TUI test
- [x] 7. Loop test with fake provider + fake LSP
- [x] 8. `task check` + `go test -race ./...`; least-code pass; adversarial
      review subagent
- [x] 9. Docs: features.md, roadmap.md, learnings (already landing),
      concurrency.md note if warranted

## Deviations / breadcrumbs

(keep current as code lands)

- 2026-08-24: plan drafted; research report `docs/learnings/other-harnesses/
  opencode/lsp.md` written by a background task alongside the plan.
- Scope locked with user: gopls-only built-in; diagnostics-only model
  surface; blocking-capped wait; symbol expansion → INF-4991.
- 2026-08-24: implemented (tasks 1–7). Deviation: the wait loop breaks on
  ANY push for the edited file (plus a 50ms trailing wake for sibling
  frames), not on a diagnostics-list diff — a clean file re-pushes an
  identical empty list, which a diff can't see. Versionless servers
  (rust-analyzer) are covered by the same wake since publish() closes all
  waiters on the file.
- 2026-08-24: `go build`, `go vet`, `gofmt -s`, `go test ./...` all green.
  The two `-race` failures in `TestEmptyEnter{SteerDrainsQueue,
  IdleDrainsStuckQueue}` (internal/tui/queue_test.go) turned out to be a
  pre-existing main-branch bug — the helper `hasUserMsg` polled the bare
  `m.agent.Messages` field while the turn goroutine appended — and were
  fixed in this branch at the root: `Agent` now publishes an atomic snapshot
  on every `Messages` mutation (`publishMsgs`/`MessagesSnapshot`), and the
  helper reads the snapshot. `go test -race ./...` is fully green.
- 2026-08-24: adversarial review landed and was applied: (1) BLOCKER —
  `client.send` could block forever on a wedged server (full out-buffer,
  dead never closes); fixed with a 5s writeTimeout that tears the client
  down. (2) ctrl+c mid-spawn poisoned `broken` permanently; canceled spawns
  no longer recorded. (3) `uriPath` double-unescaped %-paths; url.Parse
  output is already decoded. (4) `Close()` wakes rendered stale diagnostics;
  close-wakes now return "". (5) grace wait now bounded by the 1.5s
  deadline. (6) added real-process test (TestHelperProcess re-exec fake
  server) covering spawn/Setpgid/initialize/kill. (7–10) nits applied:
  sync-kind assumption comment, dead Status.Builtin removed, sort.Strings
  replaces hand-rolled sorts, 300-char message cap. One self-inflicted bug
  found via full-suite run: lsp_test.go's `LSP = nil` clobbered a sibling
  test's stub — all hook-mutating tests now save/restore.
- 2026-08-24: deviation — waiters keyed per-file (not per-(path,version))
  with publish waking all of a file's waiters; the waiter loop re-checks the
  cache and re-registers. Version-keyed matching could skip stale wakes but
  added a bug surface for zero benefit.
