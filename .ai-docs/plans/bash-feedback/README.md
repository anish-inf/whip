# Agent-loop feedback: bash output spill + streamed partial output

Branch: `feat/bash-feedback`

## What this does

Two bash-tool feedback gaps from `docs/roadmap.md` ("Agent loop" section), plus
one stale-checkbox fix:

1. **Spill truncated bash output to a temp file** (roadmap line 69). When
   combined stdout/stderr exceeds `maxOutput` (50_000 bytes) and gets
   tail-truncated, write the **full** output to a temp file and name the path
   in the tool result, so the model can `read`/`grep` the rest instead of
   guessing (pi bash tool does this; pi itself not on this machine — behavior
   per the roadmap citation).
2. **Streamed partial bash output** (roadmap line 68). Add an `OnUpdate`
   callback to `bashrun.Options`, invoked with the accumulated output at most
   every ~100ms while a command runs (pi: bash `onUpdate` throttled at 100ms),
   wired `bashrun → tools → agent.Events → TUI` so in-flight output is visible
   on the running tool row before the command exits.
3. **Tick roadmap line 70** (`WHIP_SESSION_ID`/`WHIP_MODEL` env injection) —
   already implemented in `internal/tools/bashrun/markers.go` (`SetMarkers`,
   wired at `internal/tui/tui.go:690,786`). Checkbox only; **no code change**.

## Goal

A long/verbose bash command is no longer a black box: the human watches output
live (throttled), and the model gets a path to the full log when the result is
truncated.

## Non-goals

- Custom agents / `@agent` mentions (roadmap 77/79) — the big follow-up.
- Streaming for the **interactive** PTY path — it already streams raw chunks
  via `Options.OnOutput` (`internal/tui/interactive.go`). Non-interactive
  `runPiped` is the gap.
- Rendering tool-call args as they stream from the model (roadmap line 45) —
  separate item.
- Env-marker implementation (line 70) — done already.
- Changing `maxOutput` or the truncation format beyond the spill notice.

## Design

### Surfaces & files

- `internal/tools/bashrun/bashrun.go` — `Options.OnUpdate func(outputSoFar string)`;
  `runPiped` takes `opts` and fires a throttled snapshot of the accumulated
  buffer from the drain path.
- `internal/tools/bashrun/spill.go` (new, small) — `spill(full string) string`:
  write full output to `os.MkdirTemp`/`os.CreateTemp` under
  `$TMPDIR/whip-bash-*`, return the path ("" on failure — spill must never
  break a tool result).
- `internal/tools/tools.go` — bash tool: after `TruncateTail` triggers, append
  `\n[full output: <path>]`; thread a per-call `OnUpdate` into `bashrun.Run`
  via a package hook (same pattern as `InteractiveBash`):
  `var BashOnUpdate func(chunk-so-far string)` set by the agent layer per run.
  **Concurrency:** tool calls run in parallel and bash takes the global bash
  lock (`toolMutationPath` → one global channel), so only one bash run is
  in flight at a time — but the hook must still be set/cleared around each
  run, not left dangling. Use a context-carried or mutex-guarded setter;
  check how `runTools` invokes `Tool.Run` and pick the shape that doesn't
  leak state across parallel batches.
- `internal/agent/agent.go` — `Events.OnToolOutput func(id, outputSoFar string)`
  (optional); `runTools` passes a per-call callback down for the bash tool.
  Tools are shared `Tool{Def, Run}` values, so the per-call identity has to
  travel via ctx or a tools-level setter keyed for the current call — decide
  during implementation; ctx value is the cleaner Go shape.
- `internal/tui/tui.go` — new `toolOutputMsg{id, text}`; on receipt, update the
  `blockToolRun` row for that id in place (throttled already upstream, so a
  plain `send()` is fine); keep the completed-row behavior on `toolEndMsg`.

### Channel/lifecycle notes (per docs/concurrency.md)

- Throttling happens **inside bashrun's drain goroutine**: last-fire timestamp
  guarded by the existing `mu`; fire at most every 100ms, always fire once at
  the end is NOT needed (toolEndMsg delivers the final text). Callback is
  invoked from the run goroutine — documented as such, same as `OnOutput`.
- No new goroutines; the drain loop already exists. No unbounded channels.
- TUI callback path: agent worker goroutine → `prog.Send` — same discipline as
  OnToolStart/End (never touch UI state directly).

### Spill details

- Trigger: `len(res.Output) > maxOutput` in the bash tool wrapper (the same
  condition `TruncateTail` uses).
- File: `filepath.Join(os.TempDir(), fmt.Sprintf("whip-bash-%d-*.txt", ...))`
  via `os.CreateTemp`, `0o600`. Notice appended after the truncation marker:
  `\n[full output (%d bytes): <path>]`.
- Cleanup: none — temp dir, OS reaps. Document it.

## Prior art

- pi bash tool: `onUpdate` throttled at 100ms; spill-truncated-output-to-file
  (roadmap lines 68-69 citations). pi source not on this machine; behavior
  taken from the roadmap + features docs.
- whip interactive PTY streaming: `internal/tools/bashrun/bashrun.go` `OnOutput`
  (raw chunks) — the non-interactive analog here is throttled snapshots, since
  the TUI row re-renders the whole buffer, not deltas.
- `InteractiveBash` hook (`internal/tools/tools.go:34-37`) — the precedent for
  TUI-installed behavior in the tools package.

## Test plan

- `internal/tools/bashrun/bashrun_test.go` (extend or new file):
  - Spill: command emitting > maxOutput bytes → result contains the spill
    notice, temp file exists, file content == full untruncated output.
    (Spill lives in tools.go where maxOutput lives — test at that level.)
  - OnUpdate: command with staged output (`echo a; sleep 0.35; echo b`) →
    callback fired ≥2 times, monotonically growing snapshots, inter-fire gap
    ≥ ~95ms (throttle proof), final snapshot ⊆ final output.
  - `-race` clean.
- `internal/agent/agent_test.go`: fake-provider loop test — bash tool call
  with OnToolOutput wired fires while the command runs (use a slow command),
  event carries the tool-call id.
- `internal/tui` headless test: `toolOutputMsg` updates the matching running
  block's text; unknown id is a no-op.

## Docs plan

- `docs/features.md`: extend the agent-loop/bash section — spill behavior +
  streaming, naming code + tests.
- `docs/roadmap.md`: check lines 68, 69 (implemented here) and line 70
  (already implemented in markers.go — tick with a pointer).
- `docs/concurrency.md`: only if the throttle introduces a pattern worth
  teaching (likely a one-liner addition at most).

## Tasks (ordered)

1. bashrun: `Options.OnUpdate` + throttle in `runPiped` + unit test.
2. bashrun: `spill.go` + unit test.
3. tools.go: wire spill into bash result; wire OnUpdate hook; tests.
4. agent: `Events.OnToolOutput` + runTools plumbing + fake-provider test.
5. TUI: `toolOutputMsg` + block update + headless test.
6. Roadmap checkboxes (68, 69, 70) + features.md section.
7. `task check` + `go test -race ./internal/...`; adversarial pass (parallel
   tool calls, ctrl+c mid-run, spill failure path, narrow terminal re-render).
8. Open PR.

## Deviations / breadcrumbs

- OnUpdate plumbing uses a **ctx value** (`tools.WithOnUpdate`), not a package
  var hook as first sketched — no shared mutable state, parallel-safe by
  construction.
- `runPiped` signature gained an `onUpdate func(string)` param (was `Options`
  pass-through); interactive fallback passes nil (PTY already streams raw).
- The ticker goroutine shares the drains' `mu` for buffer snapshots — no new
  synchronization introduced.
- TUI renders only the **last 3 non-empty lines** under the running row
  (`block.live`), cleared on `toolEndMsg`; persisted sessions never serialize
  `live` (blocks are in-memory only; SQLite holds llm.Messages).
- Pre-existing race on main: `TestScheduleFiresWakeup` reads
  `m.agent.Messages` while the turn goroutine appends (agent.go:287). Not
  caused by this diff (reproduces with the diff stashed); flagged for a
  follow-up fix, left out of scope here.
- Status: implemented, `task check` + full `go test ./...` green, `-race`
  green on tools/bashrun/tools/agent/tui (minus the pre-existing flake above).
