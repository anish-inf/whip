# /goal-from-context

Branch: TBD

## What this does

`/goal-from-context` takes the last two conversation messages (typically the
user's latest ask + the assistant's latest reply), asks the **current model**
to distill them into a single concrete goal statement via one non-streaming
call, sets the result as the session goal (exactly like `/goal <text>`), and
immediately submits it — kicking off the goal loop until `GOAL_MET`.

## Goal

- New TUI command `/goal-from-context` (no args) in the `command()` switch.
- Pure prompt builder `goalFromContextPrompt(msgs)` (transcript rendering in
  the style of `agent.buildSummaryPrompt`, `truncateField` caps included).
- Formulation call: `agent.Client.Complete` on the **current model only**
  (deliberately ignores the compact-model override — user asked for current).
- On success: `m.setGoal(goal)` (persists via `store.SetGoal`) + `m.submit(goal)`
  so the loop starts immediately — identical UX to `/goal <text>`.
- Errors (`Complete` failure, empty reply, <2 messages, busy) are transcript
  notes, never aborts.

## Non-goals

- No palette sub-panel (the existing Goal panel already edits/launches goals).
- No streaming for the formulation call (one-shot `Complete`, like compaction).
- No changes to the goal loop itself (`goal.go`, `goalContinuePrompt`, rounds).

## Design

Surfaces: TUI command only. Files touched:

- `internal/agent/agent.go` (or new `goal_prompt.go`): `BuildGoalFromContextPrompt(msgs []llm.Message) string` — pure, exported so the TUI can use it; renders the tail messages as a short transcript and asks for one concrete, verifiable goal line.
- `internal/tui/tui.go`: `case "/goal-from-context"` in `command()`:
  - busy → note `(busy — /goal-from-context after this turn)`, return (don't queue: the context may change by then).
  - tail = last 2 messages of `m.agent.Messages` after the system prompt; if fewer than 2 exist → error note.
  - set `m.busy`, append `◎ formulating goal…`, spawn goroutine mirroring the `/compact` pattern: `context.WithCancel` stored in `m.cancel`, `ag.Client.Complete(ctx, llm.Request{Model: ag.Model, MaxTokens: 256, Messages: [...]})` → `p.Send(goalFromContextMsg{goal, err})`. Headless (`m.prog == nil`): run synchronously like other commands' test paths.
  - `case goalFromContextMsg` in Update: on err → `errStyle` note; on empty → note; else `setGoal(goal)`, append `◎ goal set: …`, `submit(goal)`.
- `internal/tui/complete.go`: add to `commands` + `execNow`.
- `/help` text in `tui.go`: one line.
- `docs/features.md`: new bullet under the `/goal` entry.
- `docs/roadmap.md`: no existing checkbox (verified — no prior art).

Message-type note: `turnDoneMsg{}` clears busy in other flows; the
goal-from-context goroutine will `p.Send` its own msg then rely on the
subsequent `submit()` to keep busy state — on error paths it sends
`turnDoneMsg{}` to clear busy, same as `/compact`.

## Test plan (stdlib testing, headless)

In `internal/tui/goal_test.go` (httptest server like `compactCmdModel`):

- `TestGoalFromContextPrompt` — pure builder: renders last-2 messages, truncates long fields.
- `TestGoalFromContextSetsGoal` — httptest returns `"fix the flaky test"`;
  after the command, `m.goal` == that, session store got `SetGoal` (or at
  minimum `m.goal` + transcript note).
- `TestGoalFromContextErrors` — server 500 → error note, goal untouched.
- `TestGoalFromContextNeedsHistory` — <2 messages → error note.
- Busy path: note appended, no call made.

`task check` + `go test -race ./internal/...` (goroutine touched).

## Docs plan

- `docs/features.md`: extend the `/goal` bullet with `/goal-from-context` →
  code → tests, same style as existing entries.

## Tasks

1. [x] `BuildGoalFromContextPrompt` + `GoalFromContextMessages` in agent + unit test
2. [x] `goalFromContextMsg` + `case "/goal-from-context"` + Update handler
3. [x] completion + execNow + /help line
4. [x] headless tests (success / error / history / busy) — headless path runs
   the formulation inline (synchronous), mirroring /compact's test path
5. [x] `task check` + `go test -race ./internal/...` — green
6. [x] adversarial pass — found + fixed: (1) trailing `turnDoneMsg{}` after a
   successful formulation cancel-proofed and double-ran the fresh goal turn →
   handler now owns busy/cancel, no trailing msg; (2) esc-cancel rendered as an
   error → `(interrupted)` note; (3) failure-path `turnDoneMsg{}` would have
   re-engaged a paused goal's loop → dropped entirely; (4) `/resume` and
   `/clear` now refuse while busy (stale formulation could clobber a resumed
   session's goal); (5) extracted shared `writeTranscript` helper; (6) headless
   path appends the same transcript notes as live. Also made `submitTurn`'s
   turn goroutine nil-prog-safe (pre-existing panic gap) via a `send` helper.
7. [x] `docs/features.md` update
