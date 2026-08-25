# MCP import toggles: gate claude/codex imports + `whip mcp import`

Branch: `main` (small enough; single commit)
Status: ✅ shipped — all tasks below landed; `task check` green except the
pre-existing `internal/browser` live-Chrome tests (fail on a clean tree too);
touched packages pass `-race`.

Deviation from the plan as written: `Manager.SetBlocked` takes the blocked
`map[string]ServerConfig` (not pre-built statuses), and the CLI test lives in
`cmd/whip/mcp_import_test.go` driving `mcpImportCLI` directly with a
`WHIP_HOME` fixture + captured stdout.

Verified live against the real `~/.codex/config.toml`:
`exclude: ["node_repl"]` turns the startup failure into a visible
`blocked` row in `whip mcp list`; `mcp test node_repl` refuses with a
pointer at the config; `mcp import` materialized `paper` + `computer-use`
(skipping the blocked `node_repl`) and is idempotent on second run.

## What this does

The user hit a surprise: the ChatGPT desktop app writes an `[mcp_servers.node_repl]`
entry into `~/.codex/config.toml`, and whip silently picked it up (and failed on it
every startup — Codex-native protocol). whip had no way to say "don't import from
codex at all" or "import only these servers"; the only fix was a per-name shadow
entry in whip's own config.

This adds:

1. **Source gating** — `"mcpImport": {"claude": true|false, "codex": true|false}` in
   `~/.whip/config.json` (absent = import both, today's behavior).
2. **Per-name filtering** — `"only": [...]` / `"exclude": [...]` per source, so e.g.
   `"codex": {"enabled": true, "exclude": ["node_repl"]}` keeps codex imports but
   never sees the ChatGPT-app entry.
3. **`whip mcp import [--dry-run]`** — materialize imported servers into whip's own
   config (mcp-polish item 6, roadmap unchecked box). `--dry-run` prints the JSONC
   block without writing.

## Goal

- One config knob turns a whole import source off; one array filters names.
- `node_repl`-style surprises are stoppable without hand-crafting shadow entries.
- The TUI status line shows source-gated servers as `○ disabled — blocked by
  mcpImport config` instead of hiding them (visible, not silent).

## Non-goals

- Overlay entries (mcp-polish item 8 — `enabled`-only patches instead of definition
  copies). Still planned separately; this change keeps the full-copy toggle.
- Per-source CLI switches on `whip` startup flags.
- Codex bearer_token_env_var (mcp-polish item 10).

## Design

### Config (`internal/config/config.go`)

```go
type Config struct {
    ...
    // MCPImport gates claude/codex source imports (nil = import both).
    MCPImport *MCPImport `json:"mcpImport,omitempty"`
}

type MCPImport struct {
    Claude *MCPImportSource `json:"claude,omitempty"` // nil = on; &{Enabled:nil} = on
    Codex  *MCPImportSource `json:"codex,omitempty"`
}

type MCPImportSource struct {
    Enabled *bool    `json:"enabled,omitempty"` // nil = on
    Only    []string `json:"only,omitempty"`    // non-empty = allowlist
    Exclude []string `json:"exclude,omitempty"` // denylist (wins over only)
}
```

Clobber-recovery in `Load` preserves `MCPImport` the same way it preserves
`MCPServers` (carry into restored/default configs).

### Merge (`internal/mcp/config.go`)

Pure core:

```go
// sourceFilter is a compiled allow/deny gate; the zero value admits everything.
type sourceFilter struct{ only, exclude map[string]bool; enabled bool }

func filterSource(in map[string]ServerConfig, f sourceFilter) (kept, blocked map[string]ServerConfig)

// Filtered is the load result: Merged for the manager, Blocked for display.
type Filtered struct {
    Merged  map[string]ServerConfig
    Blocked map[string]ServerConfig // filtered-out imports, forced disabled
    Errs    map[string]error
}

func LoadMergedFiltered(cwd string, whipCfg map[string]ServerConfig, imp ImportPolicy) Filtered
```

- `LoadMerged(cwd, whipCfg)` stays, becomes `LoadMergedFiltered(cwd, whipCfg, ImportPolicy{})` — policy is in
  `internal/mcp` (`ImportPolicy{Claude, Codex ImportSourcePolicy}`) so config stays a leaf; `internal/config`
  provides `(*MCPImport).Policy() mcp.ImportPolicy`? **No** — config is a leaf and can't import mcp.
  Resolution: mcp defines the policy structs; `cmd/whip` + `internal/tui` convert
  `config.MCPImport` → `mcp.ImportPolicy` in one small helper (`mcp.ImportPolicyFromConfig` can't
  live in config...). Put the converter in `internal/mcp` as a func taking plain
  field values? Simplest: converter takes the two source structs by value —
  but those types live in config, and mcp *can* import config (config.go already imports
  internal/config... wait, internal/mcp/config.go DOES import internal/config). So
  `mcp.ImportPolicyFrom(c *config.MCPImport) ImportPolicy` lives in internal/mcp. OK.
- Blocked servers get `Enabled=&false`, `Note="blocked by mcpImport config"` and are
  merged UNDER everything (they can never shadow a whip entry — whip wins last).
- `only`+`exclude` both set on one source: config error? ponytail: exclude wins,
  note in docs. Validation warns via Errs? Keep silent, document.

### Manager (`internal/mcp/manager.go`)

`Manager.Blocked() []Status` — statuses for the blocked set (status disabled,
with Note). Manager gains an optional blocked map at construction:
`NewManager(servers)` unchanged signature; add `NewManagerWithBlocked(servers, blocked)`?
ponytail check — tui/mcp.go renders the union. Simpler: Manager gets `blocked []Status`
field + `SetBlocked`/`Blocked` accessors (mutex-guarded, same as Statuses).

### TUI (`internal/tui/mcp.go`, `internal/tui/tui.go`)

- `tui.go:257` — `LoadMerged` → `LoadMergedFiltered(wd, whip, mcp.ImportPolicyFrom(cfg.MCPImport))`;
  construct manager from `.Merged`, `m.mcpMgr.SetBlocked(res.Blocked-statuses)`.
- `mcpStatusView` appends blocked rows after the live ones.
- `/mcp <name> enable` on a blocked server → error: "blocked by mcpImport config —
  edit ~/.whip/config.json". Check blocked set before the live toggle.

### CLI (`cmd/whip/mcp.go`)

- `list` — source labels become exact: `whip config` / `.mcp.json` / `codex
  config`; blocked rows print as `blocked (mcpImport)`. Track per-name source in
  LoadMergedFiltered? ponytail: list re-derives source the way it does today
  (whip names set) plus the blocked map for labels — good enough.
- `import` — `whip mcp import [--dry-run] [--source claude|codex]`:
  - loads merged-with-policy view, skips names already in `cfg.MCPServers`,
    skips blocked ones;
  - dry-run: prints the `"mcp": {...}` JSONC fragment via a local
    `json.MarshalIndent` (NOT config.Save);
  - real: `cfg.MCPServers[name] = entry` (convert ServerConfig→config.MCPServer),
    `cfg.Save()`; idempotent second run prints "already imported".
  - `enable|disable <name>` subcommands? ponytail: `/mcp` already does this live and
    persisted. Skip — `import` only.

### Config plumbing touch-points

- `internal/tui/tui.go:257` (manager build)
- `cmd/whip/mcp.go` list/import/test (test should refuse blocked names with a clear error)
- `internal/config/config.go` clobber-recovery preserves MCPImport

## Test plan

- `internal/mcp/config_test.go`:
  - filterSource: source off / only / exclude / exclude-beats-only / nil policy admits all.
  - LoadMergedFiltered: blocked servers appear in Blocked with disabled+note; whip
    same-name entry is untouched; policy from config conversion.
- `internal/config` — clobber recovery keeps MCPImport (extend existing recovery test).
- `internal/tui/mcp_test.go` — status view renders blocked row with note; enable on
  blocked name errors.
- `cmd/whip` import: dry-run prints fragment, real run materializes + idempotent,
  skips existing whip entries (WHIP_HOME fixture pattern from config tests).

## Docs plan

- `docs/features.md` MCP section: source-gating bullet + `mcp import` bullet; name tests.
- `docs/roadmap.md`: check `whip mcp import [--dry-run]`; add checked line for import
  source gating.
- README CLI section: `mcp import` line.

## Task breakdown

1. config.MCPImport types + recovery preservation + tests.
2. mcp sourceFilter / LoadMergedFiltered / ImportPolicyFrom + tests.
3. Manager blocked plumbing + TUI status view + enable guard + tests.
4. `whip mcp list` labels + `whip mcp import [--dry-run]` + tests.
5. features.md / roadmap.md / README touch-ups; `task check` + `-race`.
