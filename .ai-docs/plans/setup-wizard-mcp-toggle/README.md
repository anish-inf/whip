# First-run setup wizard + live MCP import toggles in the ctrl-p palette

Branch: `feat/setup-wizard-mcp-toggle`
Status: ✅ shipped — `task check` + `-race` green after the adversarial pass.

## Fix log (adversarial review)

The pre-ship review caught one shipping blocker and four real bugs; all fixed:

1. **CRITICAL — OFF toggle was a silent no-op in production.** `isSource`
   matched only the short labels (`"codex"`, `".mcp.json"`) that
   `Filtered.Sources` uses, but the live manager's `Statuses()` carry the
   ABSOLUTE discovery path `setSource` stamps (`/home/u/.codex/config.toml`).
   Toggling imports off removed nothing and showed the server as both live
   and blocked. Fixed by matching both shapes (`isSource` in `tui/mcp.go`),
   and the panel test now injects absolute-path Sources (the short-label
   version was what masked the bug) plus asserts the real disconnect count.
2. **HIGH — stdin read-ahead.** The trust gate and the wizard each made their
   own `bufio.Reader(os.Stdin)`; the first reader's buffered read-ahead would
   swallow the second's answers when a paste supplies several lines. Fixed:
   `tui.Run` owns one shared reader threaded into both (`checkTrust(r)`,
   `setupWizard(cfg, r)`).
3. **MEDIUM — subcommands consumed the first run.** `whip auth`/`run`/`mcp`
   call `config.Load`, which writes the default config on a fresh install —
   the next interactive launch then saw `Exists()==true` and skipped the
   wizard forever. Fixed with a `~/.whip/setup.done` marker the wizard leaves
   only on success (`config.SetupDone`/`MarkSetupDone`); the trigger is now
   `!config.Exists() && !config.SetupDone()`. Aborting the wizard mid-way
   re-offers next launch.
4. **Tests didn't prove the claims.** `TestRemoveWhileConnecting` never
   deterministically hit removal-mid-connect (in-process transports settle
   instantly) — added `TestRemoveDuringInFlightConnect` with a transport
   parked on a release channel so the removal lands mid-connect and the
   `startGen`/`stillOurs` guard is actually exercised. The panel test no
   longer reads the developer's real `~/.codex/config.toml` (path fixtures).
5. **LOW.** An open MCPs panel rebuilds its rows on `mcpStatusMsg` so a
   background settle shows without re-opening; `context.Background()` in
   `mcpSetImport` is now commented as deliberate (connects must outlive the
   keypress — the manager's existing Start/reconnect contract).

No deadlock/race found in the new locking (reviewer verified the
`onChangeMu`↔`server.mu` ordering is clean everywhere).

## What this does

1. **First-run wizard** — when whip starts with no `~/.whip/config.json`, a
   short plain-terminal wizard (before the TUI starts, same shape as the trust
   dialog) asks four things:

   - **Provider**: `[1] Inference.net (browser sign-in) · [2] OpenRouter
     (paste API key) · [3] skip` — Enter = skip. `1` runs the existing device
     login (`inferencenet.Login`) in the plain terminal, then upserts the
     provider. `2` masks nothing (plain terminal, no echo suppression — print
     the caveat) — validates the pasted key against OpenRouter's `/models`
     then `UpsertOpenRouter`. Skip leaves the shipped inference-net provider
     entry (resolves its machine key if the user logs in later via `/auth`).
   - **Show thinking tokens?** `[y/N]` — Enter = no. Writes
     `"thinking": false` only on a "n"/Enter answer; "y" leaves the block
     absent (nil = on, today's default) so default configs stay minimal.
   - **Import MCP servers from Claude?** (`~/.claude.json`, `.mcp.json`)
     `[y/N]` — Enter = **no** (opt-in; user decision).
   - **Import MCP servers from Codex?** (`~/.codex/config.toml`) `[y/N]` —
     Enter = **no**.

   MCP answers are written into the config's `"mcpImport"` block
   (`{"claude": {"enabled": …}, "codex": {"enabled": …}}`) — always written
   by the wizard so the install has an explicit record (Enter = no means
   `"enabled": false`, not absent).

   Only ever asked once: the wizard runs only when the config file did not
   exist, and the saved config is the record. Non-interactive stdin (piped
   run, tests, ACP, `whip run`) skips the wizard entirely and keeps today's
   defaults (import both, thinking on, shipped inference-net provider).

2. **Palette "MCPs" sub-menu** — ctrl+p gets an "MCPs" row that drills into a
   sub-panel (existing `ppanel` machinery) listing:
   - `Import Claude MCPs  [on/off]` — ←/→/enter toggles
   - `Import Codex MCPs   [on/off]` — ←/→/enter toggles
   - a row per configured server with live status — toggles enable/disable

3. **Live, no restart** — toggles persist to config and apply immediately:
   source-off disconnects that source's imported servers and drops their tools
   from the next turn; source-on discovers and connects them (lazy-with-kickoff
   connects). Per-server toggles reuse the existing `/mcp enable|disable` live
   path.

## Goal

- New users make the provider, thinking-display, and claude/codex import
  decisions explicitly at install time instead of discovering them later.
- Users can flip the MCP import decision (or individual servers) mid-session
  from ctrl+p.
- No restart for any MCP change.

## Non-goals

- Wizard editing of `only`/`exclude` lists — config-file-only; the palette
  shows a note when a source has them ("some servers filtered — edit config").
- 3-level panel nesting (per-server reconnect lives in `/mcp`, not the panel).
- ACP / `whip run` wizard — headless implies trusted automation, no prompts.
- Re-running the wizard on demand (a `/setup` command) — possible follow-up.
- Wizard theme/mouse/effort questions — the palette already covers those well.

## Design

### First-run detection (`internal/config/config.go`)

`Load()` currently writes the default config on `os.IsNotExist` before any
caller can tell it's a first run. Smallest honest seam:

```go
// Exists reports whether the config file is already on disk. Checked before
// Load (which creates it) to detect a first run.
func Exists() bool
```

`cmd/whip/main.go` reads `firstRun := !config.Exists()` immediately before
`config.Load()` (main.go:144) and threads it into `tui.Run(cfg, …, firstRun)`.
Only the interactive TUI entry gets the wizard; `run`/`acp`/bench never pass
firstRun (they don't call Load with the intent of onboarding anyway — but
subcommands don't reach tui.Run at all, so no threading needed there).

### Wizard (`internal/tui/setup.go`, new)

Modeled on `checkTrust` (trust.go:17-48): plain stdin/stdout, before bubbletea
starts, in `tui.Run` right after the trust gate. Order: trust → wizard → TUI
(trust is about *this folder*; the wizard is about *the install*).

```go
// setupWizard runs once, on first run, before the TUI starts. It walks the
// provider, thinking-display, and MCP import questions and persists the
// answers. Non-terminal stdin skips silently (headless keeps defaults).
func setupWizard(cfg *config.Config) error
```

- One `bufio.Reader` for the whole wizard (checkTrust makes its own; the
  wizard must not re-read buffered bytes — it runs after checkTrust returns,
  and checkTrust's reader is discarded after one line, so a fresh reader here
  is safe as long as the wizard never runs before trust).
- Yes/no helper: `askYN(r, w, question string, def bool) bool` — prints
  `[Y/n]` or `[y/N]`, Enter takes the default, `y/yes`/`n/no` parse, anything
  else re-asks once then takes the default (ponytail: no validation loop).
- Provider step: `1/2/3` (or provider names). `1` → `inferencenet.Login` with
  the onCode callback printing the URL+code and attempting `openBrowserURL`;
  on success, team/project selection happens **in-TUI on first `/auth`-style
  need** — ponytail cut: the wizard's inference-net path only does the device
  login + `UpsertInferenceNet("", false)` + Save (machine key provisioning
  needs a project; the device login already returns teams, so pick: single
  team → first project auto, else print "finish with /auth inference-net").
  Simpler still: wizard runs login, stores the session token via
  `inferencenet.SaveAuth` after `EnsureMachineKey` when the team/project is
  unambiguous (1 team → pick its first project? no — creating scope in the
  wizard is presumptuous). **Decision: wizard inference-net = device login +
  save auth with the team chosen by a numbered list prompt; project =
  `CreateProjectOption`-style numbered list with "create new" — reusing
  `inferencenet.ListProjects/CreateProject` directly (all plain-terminal).**
  This is the one place the wizard is not a yes/no — it's the existing
  in-TUI flow transliterated to numbered stdin prompts.
- `2` → read key, `llm.New(config.OpenRouterBaseURL, key).Models(ctx)` with a
  15s timeout (same as applyAuthResult), `UpsertOpenRouter(key, false)`, Save.
  Validation failure → print the error, continue the wizard (don't wedge
  install on a bad paste; `/auth openrouter` retries later).
- `3`/Enter → nothing.
- All writes via the guarded `cfg.Save()` (atomic tmp+rename, `.bak`, clobber
  refusal).

### Palette sub-menu (`internal/tui/palette.go`)

New `panelMCP` `panelKind`; the existing "MCP servers" row (palette.go:242-247)
becomes "MCPs" with a `panel:` instead of `run:`.

- `ppanel` gains `mcps []mcpRow` where

```go
type mcpRow struct {
    name     string // server name, or "claude"/"codex" for source rows
    source   bool   // source-toggle row
    on       bool   // current toggle state
    status   string // rendered status detail ("ready · 4 tools", "blocked")
    filtered bool   // source has only/exclude — note instead of bare toggle
}
```

- Panel builder: two source rows (state from `cfg.MCPImport`) then one row per
  `m.mcpMgr.Statuses()` + `m.mcpMgr.Blocked()` entry.
- `panelKey` case: ↑/↓ move; ←/→/enter toggles the highlighted row; esc pops.
  Source rows call `m.mcpSetImport(name, on)`; server rows call the existing
  `m.mcpSetEnabled(name, on)`; blocked rows show the note and don't toggle
  (matching /mcp enable's refusal).
- Toggling rebuilds `pp.mcps` in place (like panelCompact's inline err) so the
  checkbox flips visibly without leaving the panel.
- `panelView` case: `[x] Import Claude MCPs — ~/.claude.json, .mcp.json` /
  `[ ] server — disabled` rows.
- Root-row badge: extend the existing `[n/n ready]` paletteState badge with
  source state, e.g. `2/3 ready · imports: claude`.

### Live source toggling (`internal/mcp/manager.go`)

Today only per-server enable/disable is live; the source gate is startup-only
(`LoadMergedFiltered` + `SetBlocked`). Add:

```go
// AddServers folds new configs into the manager and starts connecting the
// enabled ones (a source toggled on, post-startup).
func (m *Manager) AddServers(cfgs map[string]ServerConfig)

// RemoveServers tears down and forgets servers by name (a source toggled
// off): sessions closed, auto-reconnect stopped, tools gone from Tools().
func (m *Manager) RemoveServers(names ...string)
```

Concurrency contract (per docs/concurrency.md):
- `servers` map mutation takes `onChangeMu` (already guards blocked/closed/
  onChange); `Tools()`/`Statuses()`/`Config()` reads take it too (they walk
  the map). Per-server state stays under the server's own `mu`.
- A removed server gets `cfg.Enabled=false` + session close + gen++ so a
  queued reconnect or in-flight connect no-ops (the gen-guard at manager.go:
  the stale-session watcher already checks gen). Its `run` goroutine parks on
  `reconnect` forever — documented at run(): "parks thereafter — no cost,
  whip exits rather than idles" (same model as Close).
- `fireOnChange` after each mutation → the TUI callback calls
  `agent.SetMCPTools(mgr.Tools())` → the next turn (even the next tool-call
  round of a running turn, agent.go:370) sees the new set. **No restart.**

### TUI glue (`internal/tui/mcp.go`)

```go
// mcpSetImport persists the source gate and applies it live: off removes the
// source's imported servers; on re-runs discovery (LoadMergedFiltered) and
// AddServers the newly admitted ones. Whip-owned servers never move.
func (m *model) mcpSetImport(source string, enabled bool)
```

- Persists `cfg.MCPImport.<source>.Enabled` (allocating the block as needed)
  via `cfg.Save()`.
- On enable: `mcp.LoadMergedFiltered(cwd, whipBlock, newPolicy)` →
  `AddServers(newly admitted not already live)`, `SetBlocked(newly blocked)`.
- On disable: `RemoveServers(names of that source's imported servers — every
  live server whose Source attributes to that source's files)` + `SetBlocked`.
  Whip-owned shadow entries stay — whip always wins per name.
- Transcript note like mcpSetEnabled's: "claude imports: off (persisted) —
  2 servers disconnected".

## Prior art

- mcp-import-toggle plan (`.ai-docs/plans/mcp-import-toggle/`) shipped the
  `mcpImport` block this writes — shape + `Admits` semantics pinned by
  `config_test.go:TestMCPImportRoundTrip`.
- `/auth openrouter` + `/auth inference-net` (auth_cmd.go,
  auth_inferencenet_cmd.go) — the validate-and-upsert paths the wizard reuses
  in plain-terminal form (`llm.New(...).Models`, `inferencenet.Login`,
  `UpsertOpenRouter`/`UpsertInferenceNet`).
- Trust dialog (trust.go) — the plain-terminal pre-TUI prompt pattern.
- Skills per-turn refresh (main.go:61-62, prepareTurn) — the "rebuilt fresh
  each turn" precedent the live tool swap follows.

## Test plan

- `internal/config`: `Exists()` true/false against a WHIP_HOME fixture.
- `internal/mcp`: AddServers connects a late server (in-process transport via
  the existing `connectTransport` test seam); RemoveServers disconnects +
  drops from Tools()/Statuses(); remove-while-connecting and
  reconnect-on-removed no-op — all under `go test -race`.
- `internal/tui`: wizard with non-terminal stdin = no-op; askYN parsing
  (Enter/y/n/garbage); headless palette test drives panelMCP: toggle codex
  off → manager loses the server + config file gains
  `"codex": {"enabled": false}`; toggle on → server back. Wizard provider
  step: OpenRouter path against an httptest `/models`.
- Resume: no new persisted message shapes — nothing to test (recorded).

## Docs plan

- `docs/features.md`: extend the MCP section (wizard + palette panel), and the
  auth section for the wizard provider step (behavior → code → tests).
- `docs/roadmap.md`: no listed box for this; nothing to tick.

## Task breakdown

1. config: `Exists()` + test.
2. mcp: `AddServers`/`RemoveServers` + `-race` tests.
3. tui: `setupWizard` (askYN, provider step, thinking, MCP imports) + wire
   into `Run` behind the `firstRun` flag from main.
4. tui: `panelMCP` (item, builder, panelKey, panelView, badge).
5. tui: `mcpSetImport` live-toggle glue + headless panel test.
6. `task check` + `go test -race` on touched packages.
7. features.md section; adversarial review pass.
