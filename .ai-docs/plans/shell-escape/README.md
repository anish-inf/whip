# Shell escape (`!`), `/cd`, `/pwd`

Branch: current working tree (uncommitted, alongside busyCmd change)

## What this does

Adds a local shell escape and directory commands to the TUI:

- `!<cmd>` typed in the input (or queued) runs `<cmd>` locally via the existing
  `bashrun` runner — no model turn. Output lands in the transcript as a
  collapsed tool-style block **and** in `Agent.Messages` as a user message
  (`$ <cmd>` + output), so the model can see it on the next turn (opencode
  `session.shell`, `prompt/index.tsx:1059`).
- `/cd [dir]` changes loopy's process working directory (`os.Chdir`); no arg
  prints it. `~` expands. Busy-safe.
- `/pwd` prints the working directory. Busy-safe.

## Goal

The user can poke at the filesystem (list a dir, check git status, jump to
another project) without spending a model turn or queueing junk at the model,
and the model sees the result so follow-up questions have context.

## Non-goals

- opencode's shell *mode* (border/placeholder change, esc/backspace-at-0 exits,
  cursor-at-0-only trigger). ponytail: the `!` prefix at parse time covers the
  95% case; mode chrome can come later (`// ponytail` marker).
- Making bash output a real `tool` role message with a synthetic tool_call id
  (matches opencode most closely, but complicates provider compat and rewind
  pairing; a user-role message conveys the same content).
- Interactive/PTY `!` commands (the agent's bash tool already has that path;
  `!` is for quick non-interactive checks).
- Persisting cwd in the session (resume restores loopy's launch cwd).
- `/cd` mid-*message*-queue semantics: `/cd` is busy-safe and runs immediately,
  so it never queues.

## Design

All in `internal/tui` (TUI interaction surface) — the agent loop and tools
packages already expose everything needed.

### `!` shell escape — tui.go

- `runShell(text string)` on `*model`:
  - strips the leading `!`, refuses empty (`(! <command>)` note).
  - echoes `❯ !ls -la` to the transcript.
  - runs `bashrun.Run(ctx, {Command, Timeout: 120s})` with
    `context.Background()` (documented: no in-flight work to cancel; ponytail:
    wire into esc-interrupt if wanted).
  - appends the output as a `blockTool` block (reuses collapsed-preview,
    ctrl+e/click expand, resize re-wrap) with the same tail truncation +
    `(exit …)` / `(timed out)` formatting as the bash tool.
  - appends `$ <cmd>\n<output>` as a non-authored user message to
    `Agent.Messages`, then `m.persist()`. Non-authored: keeps it out of
    input-history recall; rewind's authored-user-message cuts never slice it
    off from anything (it has no paired assistant reply).
  - seedTranscript: user messages already render as `❯ …` — the `$ …` prefix
    makes resumed transcripts self-explanatory. No new block kind.
- Call sites (enter handling in `key`):
  - idle, before the `/` check: `strings.HasPrefix(text, "!")` → runShell.
  - busy queue branch: same check before queueing (shell escape never queues).
  - `turnDoneMsg` queue drain: if `queue[0]` starts with `!`, pop + runShell
    and re-handle the rest of the queue (loop, not recursion).
- History: `!cmd` goes into `m.hist` like any submission.

### `/cd` & `/pwd` — tui.go

- `case "/pwd"`: append cwd.
- `case "/cd"`: no arg → append cwd (like the shell). Else expand `~`, then
  `os.Chdir`; on success append the new cwd. Errors append red.
- Both in `busyCmd` (settings/inspection — no turn state touched).
- Completion entries in `complete.go` (`commands` list; `/cd` in `execNow`? No —
  it takes an arg; bare inserts for editing. `/pwd` yes).
- `// ponytail: directory-aware completion for /cd args` (mentionPathMatches
  covers files; dirs-only completion is a nicety).
- Help text + palette? Help text yes; palette already has "Resume session"
  etc. — skip palette (ponytail).

### Docs

- `docs/features.md`: TUI section bullet for shell escape + cd/pwd.
- `docs/roadmap.md`: check the `!` prefix shell mode box with a note on the
  delta (no mode chrome; user-role message not tool-role).

## Prior art

- opencode `!` shell mode: `packages/tui/src/component/prompt/index.tsx:1059`
  (submit → `session.shell`, output visible to the model), `:815` (mode gate).
  Report: `docs/learnings/other-harnesses/opencode/opencode-ux.md` §3.
- Roadmap line 29 tracks this feature.
- claude-code has the same `!` bash escape; output is model-visible.

## Test plan (queue_test.go / new shell_test.go in internal/tui)

- `TestBusyCmdAllowList`: extend with `/cd`, `/cd /tmp`, `/pwd`.
- `runShell` headless (`m.prog == nil` safe — no program messages):
  - output block appended (blockTool), message appended to Agent.Messages
    (non-authored, `$ ` prefix), persist not called when store nil.
  - empty `!` appends usage note, no message.
  - failing command: `(exit …)` marker present, still appended.
  - truncation marker on huge output (`seq 1 100000`).
- Enter routing: idle `!cmd` executes (no queue, no busy); busy `!cmd` executes
  immediately; busy plain text still queues.
- Queue drain: turnDoneMsg with `["!echo hi", "follow up"]` executes the shell
  line then submits "follow up" (fake: check queue empties and busy flips).
- `/pwd` headless appends cwd; `/cd` to t.TempDir() changes `os.Getwd` and
  appends it; `/cd /nope` appends an error; `/cd ~` lands in $HOME
  (t.Setenv). Restore cwd with t.Cleanup.
- Resume: seedTranscript over a message list containing a `$ …` user message
  renders it (covered by existing seed path; assert block count/content).

## Task breakdown

1. [x] `busyCmd` extend: `/cd`, `/pwd`
2. [x] `runShell` + transcript/message/persist wiring (`shell.go`)
3. [x] Enter routing: idle, busy, queue-drain (for-loop in turnDoneMsg)
4. [x] `/cd` + `/pwd` command cases, completion entries (`/pwd` in execNow),
       help text
5. [x] Tests — `shell_test.go` (13 + 3 edge-case tests), busyCmd list extended
6. [x] `task check` + `go test -race ./internal/tui ./internal/tools/...` green
7. [x] features.md + roadmap checkbox (checked with delta note)

## Adversarial review (post-implementation, second pass)

Findings and resolutions:
1. CRITICAL — runShell appended Agent.Messages from the TUI goroutine while
   the turn goroutine marshals it (Stream/stripAuthored copy, EstimateTokens).
   FIXED: conversation injection now happens in the shellDoneMsg handler;
   busy → Agent.Steer (mutex-guarded, lands at the next loop boundary), idle
   → new mutex-guarded Agent.AppendUser.
2. HIGH — synchronous bashrun.Run blocked the UI goroutine up to 120s,
   freezing esc/ctrl+c/streaming. FIXED: goroutine + shellDoneMsg, same
   pattern as /compact.
3. HIGH — steered (rather than appended) output can no longer strand between
   an interrupted authored message and its tool calls; consecutive-user-message
   risk noted, accepted (same shape as existing steered messages).
4. HIGH — /cd mid-turn changes process cwd under a running bash command:
   documented as least-surprise (running process keeps its cwd per POSIX;
   only future spawns follow). Not gated on the bash lock (TUI can't reach
   agent fileLocks without a new export; documented instead).
5. MEDIUM — /effort mid-turn field race on a.Effort: PRE-EXISTING via the
   palette (ctrl+p has no busy guard); busyCmd widens exposure but doesn't
   introduce it. Left as-is; a fix is per-request field snapshots in turn.
6. MEDIUM — busyCmd empty-fields panic guard added.
7. MEDIUM — drain-loop double echo fixed (runShellQueued, echo=false);
   sync-in-drain resolved by the goroutine change.
8. LOW — runShell flushes the in-flight assistant line before echoing.

## Deviations from the design

- `tools.TruncateTail` exported (was `truncateTail`) so the shell escape
  formats output identically to the bash tool; test references updated.
- `runShell` skips `m.persist()` while busy — the mid-turn `saved` counter
  would skip the message (nothing else bump it); the turnDoneMsg persist
  picks it up instead.
- Queue drain became a `for` loop (peel consecutive `!` lines) rather than
  recursion; drained escapes skip the transcript re-echo (the queue view
  already rendered them).
- runShell is async (goroutine + shellDoneMsg), not synchronous — see review.
- Edge-case tests added beyond the plan: whitespace-only `!`, mid-string `!`
  is not an escape, multiline commands run through bash -c.
