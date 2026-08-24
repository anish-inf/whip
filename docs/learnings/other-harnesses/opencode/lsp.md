# opencode LSP integration — distillation

> Research for **loopy** LSP support. Distilled from the opencode codebase at
> `/home/abe/code/coding-harnesses/opencode`. All citations are `file:line`
> relative to that repo root. Primary sources: `packages/opencode/src/lsp/{lsp,client,server,language,diagnostic,launch}.ts`,
> `packages/core/src/config/lsp.ts`, `packages/opencode/src/tool/{read,write,edit,apply_patch,lsp,registry}.ts`,
> `packages/web/src/content/docs/lsp.mdx`.

## TL;DR for loopy

opencode's LSP layer is **diagnostics-first, optional, and gated**: LSP is *disabled by default* (`lsp` config omitted → off), servers are spawned lazily on first file touch by extension, and diagnostics are injected into write/edit tool outputs as a short `<diagnostics>` block containing **errors only** (max 20/file, max 5 other files). A richer `lsp` tool (definition/references/hover/symbols/call-hierarchy) exists but is hidden behind an experimental flag. Their own docs warn LSP is "not always a net positive" and recommend CLI lint/typecheck loops as the default. This validates loopy's instinct: start with post-edit error diagnostics, don't block aggressively, and treat hover/symbols as a stretch goal.

---

## 1. Architecture: discovery, spawn, lifecycle, roots

### Server registry (built-ins)

`packages/opencode/src/lsp/server.ts` (1983 lines) is a flat list of `export const X: Info` server definitions. The `Info` shape is at `server.ts:81-86`:

```ts
export interface Info {
  id: string
  extensions: string[]
  global?: boolean
  root: RootFunction            // (file, ctx) => Promise<string | undefined>
  spawn(root, ctx, flags): Promise<Handle | undefined>
}
```

~35 built-in servers (deno, typescript, vue, eslint, oxlint, biome, gopls, ruby-lsp, ty, pyright, elixir-ls, zls, csharp, razor, fsharp, sourcekit-lsp, rust-analyzer, clangd, svelte, astro, jdtls, kotlin-ls, yaml-ls, lua-ls, php intelephense, prisma, dart, ocaml-lsp, bash, terraform, texlab, dockerfile, gleam, clojure-lsp, nixd, tinymist, hls, julials). Each declares its own extension list and root detection. There's no shared "extension → server" index; matching is done by linear scan over enabled servers (see below).

**Mutual exclusion / precedence is ad hoc**: e.g. `Typescript` uses `NearestRoot([...lockfiles], ["deno.json", "deno.jsonc"])` — the exclude list makes the TS server back off when a Deno project is detected (`server.ts:117-121`). An experimental runtime flag swaps pyright for ty (`lsp.ts:98-108`, flag `OPENCODE_EXPERIMENTAL_LSP_TY`).

### Root detection

Two helpers (`server.ts:32-79`):

- `NearestRoot(include, exclude?)` — walks **up** from the file's directory via `Filesystem.up()` looking for marker files (lockfiles, `go.mod` equivalents, `pyproject.toml`, `Gemfile`, etc.), stopping at the project directory. Falls back to `ctx.directory` if no marker found.
- `StrictNearestRoot` — same but returns `undefined` if no marker (server simply doesn't activate).

Some servers just root at `ctx.directory` unconditionally (bash `server.ts:1599`, dockerfile `server.ts:1777`). Deno has a hand-rolled root fn that returns `undefined` without `deno.json(c)` (`server.ts:90-100`).

### Command lookup & auto-install

Each `spawn` does its own `which()`/module resolution. Examples:

- typescript: resolves the project's own `typescript/lib/tsserver.js`, then needs `typescript-language-server` on PATH; returns `undefined` if either missing (`server.ts:124-142`) — server silently skipped.
- deno: `which("deno")` then `deno lsp` (`server.ts:101-113`).
- tinymist/terraform/etc.: if binary not found, **downloads it from GitHub releases at spawn time** into `Global.Path.bin` (`server.ts:1867-1944`), gated by `flags.disableLspDownload` (env `OPENCODE_DISABLE_LSP_DOWNLOAD`, docs `lsp.mdx:54`).
- Spawned with piped stdio via `launch.ts:6-21` (thin wrapper over `Process.spawn` requiring stdin/stdout/stderr pipes).

### Config merge & enablement

LSP is **off unless configured**: `lsp.ts:151-153` — if `cfg.lsp` is falsy, no servers at all ("all LSPs are disabled"). Otherwise all built-ins are registered by id (`lsp.ts:154-156`), then user entries overlay them (`lsp.ts:160-181`): `disabled: true` deletes the entry; a config entry keeps the built-in's `root`/`extensions` as defaults but **replaces `spawn`** with the user's `command` + `env` + `initialization`, rooting at `ctx.directory` when the built-in doesn't exist (`lsp.ts:168-180`).

### Lifecycle (lazy spawn, keyed by root+serverID)

Servers are **spawned on demand**, never eagerly. The core loop is `getClients(file)` at `lsp.ts:208-297`:

1. Skip files outside the project (`containsPath` check, `lsp.ts:210`).
2. For each enabled server: skip if `extensions` non-empty and doesn't include the file's ext (`lsp.ts:255`); compute `root` (`lsp.ts:257`); skip if root+id is in `s.broken` (`lsp.ts:259`).
3. Reuse an existing client with same `root`+`serverID` (`lsp.ts:261-265`).
4. Deduplicate concurrent spawns via an in-flight `spawning` map keyed `root + server.id` (`lsp.ts:267-282`).
5. `schedule()` spawns the process, creates the client, and on any failure adds the key to `s.broken` (permanent for the session) and kills the process (`lsp.ts:217-251`). A race guard disposes a client if an identical one was registered meanwhile (`lsp.ts:244-248`).

**Shutdown**: only at instance teardown — an Effect finalizer calls `client.shutdown()` on all clients (`lsp.ts:198-202`), which ends the JSON-RPC connection and stops the process (`client.ts:640-644`). There is no idle timeout, no LRU, no per-server cap — one process per (root, server) lives for the whole session.

`hasClients(file)` (`lsp.ts:328-342`) is the cheap "would any server handle this file?" check used by the lsp tool.

---

## 2. Which LSP features they use

Two tiers:

**Tier 1 — diagnostics (always on when LSP enabled):**
- Push: `textDocument/publishDiagnostics` (`client.ts:160-172`).
- Pull (LSP 3.17): `textDocument/diagnostic` and `workspace/diagnostic`, incl. dynamic registration via `client/registerCapability` and `relatedDocuments` (`client.ts:180-199, 293-353`).
- These feed tool outputs automatically (§3).

**Tier 2 — an experimental `lsp` agent tool** (`tool/lsp.ts`), gated by `flags.experimentalLspTool` (`tool/registry.ts:247`, flag `OPENCODE_EXPERIMENTAL_LSP_TOOL`). Operations (`tool/lsp.ts:11-21`): `goToDefinition`, `findReferences`, `hover`, `documentSymbol`, `workspaceSymbol`, `goToImplementation`, `prepareCallHierarchy`, `incomingCalls`, `outgoingCalls`. Implementation is a thin pass-through to `client.connection.sendRequest` in `lsp.ts:377-478`; results are dumped as raw `JSON.stringify(result, null, 2)` into the tool output (`tool/lsp.ts:108`) — no summarization. `workspaceSymbol` filters to 8 symbol kinds and caps at 10 per server (`lsp.ts:87-96, 433-441`).

**Incidental use**: `session/prompt.ts:839` uses `documentSymbol` to expand a `file#Lx-Lx` range attached in a prompt to the enclosing symbol's full range.

So: diagnostics are the load-bearing feature; navigation/symbol queries exist but are experimental and unpolished.

---

## 3. How diagnostics flow to the model

### The touch() pattern

Every file-mutating tool notifies the LSP layer and then reads back the diagnostic cache:

- **write** (`tool/write.ts:75-90`): `lsp.touchFile(filepath, "document")` → `lsp.diagnostics()` → for the edited file and up to `MAX_PROJECT_DIAGNOSTICS_FILES = 5` other files (`write.ts:18`), append blocks.
- **edit** (`tool/edit.ts:195-201`): same, current file only.
- **apply_patch** (`tool/apply_patch.ts:266-293`): touches every changed (non-deleted) file, then appends a block per file with errors.
- **read** (`tool/read.ts:117-119, 353`): calls `lsp.touchFile(filepath)` **without** the diagnostics wait, forked in the background (`Effect.forkIn(scope)`) purely to warm the server and open the document, so a later edit gets fast diagnostics. Explicitly non-fatal (`Effect.ignoreCause`).

### touchFile mechanics

`LSP.touchFile(input, diagnostics?)` at `lsp.ts:344-362`:

1. Resolve clients (spawning servers if needed — this can take the full initialize path on first touch).
2. `client.notify.open({ path })` (`client.ts:554-621`): reads the file from disk and sends `didOpen` (version 0, full text) or `didChange` (bumped version), preceded by a `workspace/didChangeWatchedFiles` notification (`CREATED` on first open, `CHANGED` after). Returns the new version.
3. If a diagnostics mode was passed, `client.waitForDiagnostics({ path, version, mode, after })` — **this blocks the tool** until fresh diagnostics arrive or a timeout fires.

### Waiting for diagnostics (the interesting part)

Timeouts (`client.ts:13-18`):

```ts
DIAGNOSTICS_DEBOUNCE_MS = 150
DIAGNOSTICS_DOCUMENT_WAIT_TIMEOUT_MS = 5_000   // mode "document"
DIAGNOSTICS_FULL_WAIT_TIMEOUT_MS    = 10_000   // mode "full"
DIAGNOSTICS_REQUEST_TIMEOUT_MS      = 3_000    // per pull request
INITIALIZE_TIMEOUT_MS               = 45_000
```

`waitForDocumentDiagnostics` (`client.ts:499-519`) races two strategies:
- **Push**: `waitForFreshPush` (`client.ts:464-497`) listens for a `publishDiagnostics` notification for that path from that server, matching the version (or timestamp ≥ `after`), then waits out a 150ms debounce to coalesce follow-up pushes.
- **Pull**: loops `requestDocumentDiagnostics` — fires the base `textDocument/diagnostic` request **plus one per dynamically-registered identifier, in parallel** (`client.ts:416-427`), and — explicitly "LATENCY-CRITICAL" per the comment at `client.ts:412-415` — resolves as soon as any batch produced diagnostics *for the current file*, letting slower pulls merge in the background.
- Between pull rounds it races the push-wait against a `client/registerCapability` change (`client.ts:513-517`), since some servers only register diagnostic capability after initialization.

`waitForFullDiagnostics` (`client.ts:521-541`) is the same but also issues `workspace/diagnostic` pulls and waits up to 10s. Note: **only "document" mode is used by tools today** — write/edit/apply_patch all pass `"document"`; `"full"` exists in the API but no caller in tree passes it.

Special case: TypeScript's server pushes diagnostics aggressively on first open, so the very first push is seeded directly into the cache (`shouldSeedDiagnosticsOnFirstPush`, `client.ts:119-121, 167-170`) to avoid waiting for a second push.

### Cache, dedup, merge

Per client: `pushDiagnostics` and `pullDiagnostics` maps keyed by normalized path; `mergedDiagnostics` = dedupe of concatenation (`client.ts:139-153`). Dedup key is JSON of `{code, severity, message, source, range}` (`client.ts:91-105`). `client.diagnostics` getter merges both maps (`client.ts:623-629`); `LSP.diagnostics()` unions across all clients (`lsp.ts:364-375`). Deliberately **not cleared on didChange** — clangd only re-emits on real content changes, so clearing would lose errors on no-op touches (`client.ts:564-567`); cleared only on (re)open (`client.ts:609-610`).

### Formatting into the prompt

`lsp/diagnostic.ts` is the whole formatter:

- `pretty()` (`diagnostic.ts:5-18`): `ERROR [line:col] message` — 1-based, severity mapped 1→ERROR/2→WARN/3→INFO/4→HINT.
- `report()` (`diagnostic.ts:20-27`): **filters to severity 1 (errors) only**, caps at `MAX_PER_FILE = 20` with a `... and N more` suffix, wraps in `<diagnostics file="...">...</diagnostics>`.

Injection strings:
- write/edit: `\n\nLSP errors detected in this file, please fix:\n<diagnostics ...>` (`write.ts:85`, `edit.ts:201`).
- write additionally: `LSP errors detected in other files:` blocks for up to 5 other files (`write.ts:81-89`).
- apply_patch: `LSP errors detected in <rel>, please fix:` per file (`apply_patch.ts:289-292`).

Diagnostics are also stashed in tool `metadata.diagnostics` (raw map) for UI use (`write.ts:95`, `edit.ts:206`).

So the model sees diagnostics **only as a tail on edit-tool output**, never in the system prompt, never as a separate message.

---

## 4. Client protocol details

From `client.ts`:

- **Transport**: JSON-RPC over stdio using `vscode-jsonrpc/node`'s `createMessageConnection(StreamMessageReader(proc.stdout), StreamMessageWriter(proc.stdin))` (`client.ts:132-135`) — standard `Content-Length` framing handled by the library. stderr is drained and ignored (`client.ts:136`).
- **Initialize** (`client.ts:211-255`): `rootUri`, `processId`, single workspaceFolder `{name: "workspace", uri: root}`, `initializationOptions` from config/server. 45s timeout → `InitializeError`. Advertised capabilities are minimal: `window.workDoneProgress`, `workspace.configuration`, `didChangeWatchedFiles` dynamic registration, `workspace.diagnostics.refreshSupport: false`, `textDocument.synchronization {didOpen, didChange}`, `textDocument.diagnostic {dynamicRegistration, relatedDocumentSupport}`, `publishDiagnostics {versionSupport: false}`.
- After `initialized`, sends `workspace/didChangeConfiguration` with the initialization settings (`client.ts:260-266`).
- **Server→client requests handled**: `window/workDoneProgress/create` (null), `workspace/configuration` (serves values out of the initialization object by dotted section path, `client.ts:107-114, 176-179`), `client/registerCapability`/`unregisterCapability` (only tracks `textDocument/diagnostic` registrations, `client.ts:180-199`), `workspace/workspaceFolders` (single folder), `workspace/diagnostic/refresh` (null — they refuse refreshes, `client.ts:206`).
- **Document sync** (`client.ts:554-621`): whole documents read from disk each touch (no incremental diff computation). If server negotiated `textDocumentSync.change === 2` (incremental), they fake it with a single change spanning `(0,0)`→end-of-old-text with the full new text (`client.ts:583-596`); otherwise full-text sync. Versions tracked per file in a `files: Record<path, {version, text}>` map, starting at 0 on didOpen, +1 per didChange. **No `didClose` anywhere** — documents stay open for the life of the client.
- **languageId** comes from a static `LANGUAGE_EXTENSIONS` map (`language.ts`, ~120 extensions; falls back to `"plaintext"`, `client.ts:560`).
- Every file open/change is preceded by `workspace/didChangeWatchedFiles` (`CREATED`/`CHANGED`) (`client.ts:568-575, 600-607`) — presumably to nudge servers that gate analysis on file watching.

---

## 5. Notable weaknesses / costs

1. **Blocking waits on the edit hot path.** Every write/edit/apply_patch pays `waitForDiagnostics` up to **5s** (document mode), and the first touch additionally pays process spawn + `initialize` (up to 45s timeout). Pull-request timeout is 3s per attempt and can loop within the 5s window. Worst case an edit tool call is many seconds slower. (Mitigations: parallel identifier pulls with early resolve, `client.ts:412-427`; TS first-push seeding, `client.ts:119-121`.)
2. **Spawn-on-demand latency & flakiness.** A broken server is blacklisted for the whole session (`s.broken`, `lsp.ts:259`) with no retry. First touch of a new file type cold-starts the server synchronously inside the tool call.
3. **Reads the file from disk on every touch** (`client.ts:558`) — no reuse of the edit tool's in-memory content; full-text sync always (incremental sync is faked with a full-document range). Fine for small files, wasteful for large ones; also means the LSP view depends on the write having hit disk first.
4. **Documents never closed / servers never stopped.** Memory grows with every distinct file touched; one process per (root, server) lives until session end. Their own docs warn servers "use significant memory" (`lsp.mdx:72`).
5. **Errors-only surfacing.** Warnings/info/hints are collected but filtered out before the model sees them (`diagnostic.ts:21-22`). Cross-file visibility is limited: only write surfaces other files' errors (max 5 files, arbitrary order — `Object.entries` of the merged map).
6. **Version matching is best-effort.** `publishDiagnostics.versionSupport: false` is advertised (`client.ts:247`), and the fresh-push check tolerates timestamp-only matching (`client.ts:482-483`), so a stale push can satisfy the wait.
7. **No persistence/result-ids.** `workspace/diagnostic` is always called with `previousResultIds: []` (`client.ts:336`) — full recompute each pull.
8. **Their own documented ambivalence**: "Language servers can get out of sync… slow down agent workflows. In many projects it is better to have the agent run lint, typecheck, or other diagnostic CLI tools directly" (`lsp.mdx:70-72`). LSP is disabled by default (`lsp.mdx:51`).
9. **Extension matching is first-come across all servers** — multiple servers can attach to the same file (typescript + eslint + oxlint all claim `.ts`), each a separate process; diagnostics are merged per path across clients. That's a feature but multiplies cost.
10. **The richer `lsp` tool is experimental and raw**: JSON dump output, no ranking/truncation beyond workspaceSymbol's 10, gated behind `OPENCODE_EXPERIMENTAL_LSP_TOOL` (`registry.ts:247`) — signaling they don't trust its token cost/value yet.

---

## 6. Config schema (user-facing)

Schema: `packages/core/src/config/lsp.ts`:

```ts
Info = boolean | Record<string, Entry>
Entry = { disabled: true } | Server
Server = {
  command: string[]
  extensions?: string[]
  disabled?: boolean
  env?: Record<string, string>
  initialization?: Record<string, unknown>
}
```

Semantics (docs `lsp.mdx:76-201` + merge code `lsp.ts:151-189`):

- `lsp` omitted or `false` → everything disabled. `lsp: true` → all built-ins. `lsp: {...}` → built-ins plus per-key overrides.
- An entry **without** `command` that matches a built-in only toggles `disabled` (e.g. `{"typescript": {"disabled": true}}`).
- An entry **with** `command` replaces spawn: user's command/env/initialization, extensions falling back to the built-in's, root falling back to project dir for brand-new servers. This is both the override and the custom-server mechanism (`{"custom-lsp": {"command": ["custom-lsp-server", "--stdio"], "extensions": [".custom"]}}`).
- `initialization` is sent both as `initialize.initializationOptions` and as a `workspace/didChangeConfiguration` payload, and answers `workspace/configuration` requests (`client.ts:221-223, 262-266, 176-179`).
- Env vars: `OPENCODE_DISABLE_LSP_DOWNLOAD` (stop auto-downloads), `OPENCODE_EXPERIMENTAL_LSP_TY` (swap pyright→ty), `OPENCODE_EXPERIMENTAL_LSP_TOOL` (enable the `lsp` agent tool).

---

## Implications for loopy (suggested takeaways)

- **Copy the touch-then-report pattern**: after edit/write, notify LSP (didOpen/didChange with full text), wait briefly for fresh diagnostics, append errors-only block to tool output. Keep reads as fire-and-forget warmers.
- **Adopt their caps**: errors-only, ~20/file, `<diagnostics file="...">` wrapper, "please fix" phrasing.
- **Bound the wait harder**: 5s is their ceiling and clearly a pain point (see the LATENCY-CRITICAL comment); consider 1–2s document wait and never wait on cold spawn (spawn async, report "diagnostics pending" instead).
- **Root detection**: NearestRoot-over-marker-files with exclude lists is simple and sufficient; StrictNearestRoot avoids spurious servers.
- **Skip for v1**: pull-diagnostics identifier fan-out, workspace/diagnostic, dynamic registration races, auto-downloading binaries, the navigation tool. Start with push diagnostics + plain `didOpen/didChange`, reuse project-local binaries only.
- **Default-off or default-minimal** is the industry position — even opencode ships it disabled and documents CLI typecheck loops as the better default.
