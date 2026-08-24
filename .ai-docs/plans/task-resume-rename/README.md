# Restore background subagents on resume + user-facing "subagent" naming

Branch: TBD

## What this does

1. **Persist background tasks** in the session store so `--resume`/`/resume`
   restores the dock list. Tasks found in `running` state on disk are restored
   as `error` ("interrupted — loopy exited"): a process exit kills in-flight
   subagents, so a persisted running row always means that.
2. **Rename the user-facing term** task → subagent: dock strip, palette row,
   header badge, hints, `/tasks` help text. The command stays `/tasks`;
   internal Go names (`BackgroundTask`, `taskRegistry`, `task-N` IDs, the
   `task` tool name the model calls) are unchanged.

## Goal

- `loopy --resume <id>` shows the session's background subagents in the dock
  and `/tasks`, with their final reports viewable (enter opens the detail
  view for settled tasks).
- Users read "subagent" everywhere they'd previously read "task".

## Non-goals

- Resuming/restarting interrupted subagents' work (restored as settled-error).
- Renaming the `task` tool, Go types, or the `/tasks` command.
- Persisting live event streams (only final state/report survives).

## Design

Surfaces: `internal/session/session.go` (new table + CRUD), `internal/agent`
(hook for settle events), `internal/tui` (wire hook, seed on resume, strings).

- **Schema**: `tasks(session_id, task_id, description, prompt, status,
  report, started_at, ended_at)`; PK `(session_id, task_id)`. Created in the
  existing `schema` const — `CREATE TABLE IF NOT EXISTS` is the migration.
- **Store methods**: `SaveTask(sessionID string, t Task)` (INSERT OR REPLACE),
  `LoadTasks(sessionID) []Task`. `session.Task` is a persistence DTO (plain
  fields; the session package must not import agent — agent imports nothing
  from session today either; the TUI converts).
- **Agent**: the registry already has `OnChange func(*BackgroundTask)` fired
  on start and settle — but the TUI owns it for redraws. Add a second hook
  `OnSettle`? No — ponytail: the TUI's existing `OnChange` closure gets one
  extra line calling `store.SaveTask` (it already runs on start AND settle).
  No agent-package change needed for persistence. For seeding, add
  `agent.RestoreTask(t BackgroundTask)` — inserts a settled task into the
  registry (no goroutine, no Steer), giving it a fresh Done channel already
  closed so waiters don't block.
- **TUI resume()**: after rebuilding the agent, `store.LoadTasks(id)` →
  convert to `agent.BackgroundTask` (running→error "interrupted — loopy
  exited", fresh closed `Done`) → `RestoreTask` each. `--resume` and `/resume`
  both flow through `m.resume(id)` — one site.
- **Naming pass** (tui only): dock hint "⚙ subagents — …", dock header badge
  "⚙ N sub", `/tasks` view heading "background subagents", palette row "MCP
  servers" stays but the "Resume session"-adjacent row… the palette has no
  tasks row; update `/help` text, `complete.go` description, empty-state
  "(no background subagents)", task-view footer. Tool result strings in
  `agent/task.go` ("Started background task %s") stay — they speak to the
  model, and the tool is named `task`; consistent.

## Prior art

Roadmap line 74 (background subagents) — extend; features.md "Background
subagents" section gets the persistence + naming notes.

## Test plan

- `session_test.go`: SaveTask/LoadTasks round-trip; upsert (start then
  settle keeps one row, final status wins).
- `agent`: `RestoreTask` — restored task visible in List/Get, Done already
  closed (non-blocking `<-Done`).
- `tasks_test.go` (tui): resume restores dock rows — build a store with
  tasks (done + a stale running one), drive `m.resume(id)`, assert the dock
  lists both and the stale-running one renders as error/interrupted; enter
  on a restored settled task shows its stored report (no live subscribe).
- `task check` + `go test -race ./...` (registry touched).

## Docs plan

- `docs/features.md`: Background subagents section — persistence-on-resume +
  naming; Session persistence section if it lists what's stored.
- roadmap line 74 updated.

## Tasks

1. session: tasks table, Task DTO, SaveTask/LoadTasks + tests. ✅
2. agent: RestoreTask + test. ✅ (plus OnRecord hook — OnChange alone is
   prog-gated in wireTasks, so persistence needed its own headless-safe hook)
3. tui: SaveTask via OnRecord in wireTasks; resume() seeds the registry
   (meta.ID, not the user's prefix); tests. ✅
4. Naming: dock hint, /tasks view, ⚙ N sub badge, complete.go, /help. ✅
   Kept: `/tasks` command, `task` tool name + Go types, model-facing strings.
5. `task check`, `-race`, features.md + roadmap. ✅
6. Mutation-tested: TestResumeRestoresTasks fails when RestoreTask is skipped.
