# Wait/poll mechanisms — cross-harness research distillation

Six harnesses researched (2026-08-05), full reports in session history. This
file is the durable distillation for the `wait`-tool design.

## The question

When the agent must wait for an external condition (CI checks, deploy, server
up), what does the harness offer instead of the model running
`sleep 30 && check` loops (each poll = a full LLM turn)?

## Per-harness findings

### codex (OpenAI, Rust) — session-handle long-poll

- No generic wait/watch/notify tool; no auto-wakeup ticker.
- **`exec_command(cmd, yield_time_ms)`** (`core/src/tools/handlers/unified_exec/exec_command.rs`,
  schema `shell_spec.rs:21-111`): blocks up to `yield_time_ms` (250–30_000,
  default 10s), returns a `session_id` handle if the process is still alive.
- **`write_stdin(session_id, chars, yield_time_ms)`**: empty-write polls wait
  5s–300s. The harness owns the PTY registry (`ProcessStore`, max 64 live,
  `unified_exec/mod.rs:77`) and an exit-watcher (`async_watcher.rs:165`) that
  emits a session/UI event on exit — but NOT into the model conversation.
  Completion re-enters the model only via the next `write_stdin` poll.
- **`clock.sleep(duration_ms)`** (`tools/handlers/sleep.rs:107-137`): blocks
  but `select!`s on `session.input_queue.subscribe_activity().changed()` —
  new user input interrupts the sleep ("Sleep interrupted by new input").
  Same pattern in `wait_agent` (mailbox watch) and `wait_for_environment`.
- **Async hooks** (`hooks/src/engine/dispatcher.rs:131-140`): background hook
  results are drained and injected as `ResponseItem`s **only at turn
  boundaries** (`turn.rs:161,410`) via `inject_if_running` → pending-input
  queue → activity `watch` channel. `SubagentNotification` renders subagent
  status changes as a user-role `<subagent_notification>` fragment injected
  the same way.
- `file-watcher` crate exists but is unused by core.
- **Port-worthy:** (1) yield_time_ms session handle — replaces sleep loops
  with a harness-owned long poll; (2) one session-level `watch.Sender`
  activity channel every blocking tool selects on; (3) turn-boundary-only
  injection with `has_pending_input` re-sample check; (4) model-visible bounds
  in the schema text so the model learns cadence by reading the def.

### opencode (TS) — Deferred await + synthetic injection + coalesced wake

- No wait/watch tool. Shell tool: only `timeout`; kills on expiry, tells model
  to retry bigger. No background mode on shell.
- Background jobs only for the `task` (subagent) tool:
  `core/src/background-job.ts` — registry with `Deferred<Info>` per job;
  `wait()` is a pure promise await (optional timeout → `{timedOut:true}`).
- Foreground task (`tool/task.ts:317-347`): races `wait(jobId)` vs
  `waitForPromotion(jobId)` in-call — synchronous delegation, no polling.
- Background task: tool returns immediately; a forked `notify(jobId)` fiber
  awaits the Deferred; on settle, `injectBackgroundResult` calls
  `ops.prompt()` on the PARENT session with a `synthetic:true` text part
  rendered `<task state=...>...</task>` (task.ts:216-254).
- Tool prompt explicitly: "You will be notified automatically when it
  finishes. DO NOT sleep, poll for progress…".
- **Wake machinery**: `V2Session.prompt()` (`core/src/session.ts:360-384`)
  admits input with `delivery:"steer"` → `execution.wake(sessionID)` →
  run-coordinator (`run-coordinator.ts:51-92`) sets `pendingWake` on the
  active drain and coalesces N wakes into exactly one follow-up drain. The
  drain (`runner/llm.ts:390-413`) re-checks `hasPending("steer")` after EVERY
  LLM turn, not just between drains.
- No setInterval/heartbeat in the agent loop anywhere. Event-driven push. The
  one 250ms poll (`cli/cmd/run/stream.transport.ts:867-872`) is a UI liveness
  fallback only — never generates model input.
- LLM retry policy: 2s initial, ×2, 0.25 jitter, honor Retry-After, cap 30s,
  max 5 (`session/retry.ts`).
- **Port-worthy:** notify-on-settle injection (message-shaped, once);
  coalesced wake (atomic pendingWake bool + single re-drain); steer re-check
  after every turn; event-first with poll only as UI safety net.

### pi (TS) — nothing (baseline)

- Tools: bash/edit/find/grep/ls/read/write only. bash has `timeout` +
  SIGTERM→SIGKILL escalation (`core/tools/bash.ts:123-144`). No async mode.
- Loop (`agent/src/agent-loop.ts:155-275`): strict
  stream→execute-tools→drain-queues→continue. Steer/follow-up queues drained
  synchronously at turn boundaries (`getSteeringMessages`/`getFollowUpMessages`,
  types.ts:222-257); NO event wakes a parked loop — a long bash can't be
  interrupted by steering.
- Subagent tool (example extension): spawns child processes, blocks until all
  exit; `onUpdate` partials go to UI only, never the model.
- `DeferredHandle` type (`ai/src/types.ts:409-419`, `pollAfterMs`,
  `expiresAt`) designed for provider deferred responses; harness that would
  drive it is a stub (`HarnessNotImplemented` everywhere). Designed, not wired.
- **Port-worthy:** drain-at-boundaries queue semantic (whip has this via
  Steer); bash timeout kill escalation (whip has); throttled partial-output UI
  channel (`BASH_UPDATE_THROTTLE_MS=100`); the lesson that designing a
  deferred surface without wiring it = it never ships.

### exo (Rust) — external scheduler daemon, wakeup User message

- No wait tool; shell tool blocks the turn synchronously and has NO timeout
  (`harness_tool.rs:894-916`).
- **Scheduler** is the wait primitive: model calls `schedule_sandbox_task`
  (argv command, `@every/@at/cron`, `missed` policy, sandbox scope,
  `reportPrompt`, `maxOutputBytes`).
- Timing owned by a separate host binary `exo-scheduler-runner`
  (`exo/scheduler-runner/src/main.rs:109-117`): `loop { run_due_tasks; sleep
  60s }` fixed poll.
- Fire → run command in sandbox → write result artifact →
  `send_conversation_wakeup` (`scheduler_runtime.rs:353`) →
  `HarnessConversation::send()` acquires per-conversation async lock → starts
  a FRESH model turn whose user input is the wakeup prompt
  (`harness_executor.rs:151-184`). Not an injection into a running turn.
- Durability: tasks + pending-fire records persisted (`scheduler_store.rs`);
  lease 10min, `(task_id, slot_ms)` dedupe → bounded at-least-once;
  `redeliver_pending_wakes` on startup. Grid-anchored fires
  (anchor + n×interval, no drift). `MissedPolicy::{Skip,Once,All}`, catch-up
  cap 100. Command timeout 10min. Wakeup file lock stale 30min.
- **Port-worthy:** wakeup-as-fresh-turn via conversation lock; durable
  pending-fire record written BEFORE delivery; grid anchor + missed policy;
  distinct command timeouts (scheduler has one, shell doesn't — port a shell
  default timeout too).

### claude-code (closed; documented/observed/inferred)

- Background bash + `TaskOutput` polling tool — but each TaskOutput call IS a
  turn; docs steer away from poll loops toward shell-level blocking (`wait
  $PID`) or sparse checks.
- 2.x: background task completion → status-bar notice → "remind to continue"
  injects ONE short `[Background command <id> finished]` system/user message.
  O(1) injections, never n polls. Process watcher lives in the harness/OS.
- Hooks (`SessionStart`, `Pre/PostToolUse`, `Notification`, `StatusLine`) let
  external scripts surface status with zero model turns at emission; folded
  into the next real invocation.
- `/bashes` list = TUI-side process table, human-operated kill/jump, zero
  model involvement.
- Invariant: **the timer lives outside the model; only terminal deltas
  (started/finished/failed) serialize into the transcript, once.**

### hermes-agent (Python, OpenHands-family) — closest structural match to Claude

- `terminal(background=true, notify_on_complete=true)` → returns session_id,
  process tracked in `process_registry.py`. `watch_patterns` = regex
  notification for never-exiting servers.
- Gateway asyncio watcher `_run_process_watcher` (`gateway/run.py:26066`):
  sleeps on check_interval, silent unless state changed; on exit (and not
  consumed) builds synthetic completion event →
  `_enqueue_process_completion_notification` → **new turn when idle, never
  spliced into a running turn** (run.py ~10323).
- Exactly-once: `is_completion_consumed` shared by foreground wait, poll/log,
  notify, auto-delivery.
- Anti-spam: watch patterns capped 1/15s (`WATCH_MIN_INTERVAL_SECONDS`,
  process_registry.py:73-84), 3 strikes → disabled → auto-promoted to
  notify_on_complete. Notifications truncated to ~2000 chars, line-snapped.
- `process(wait, session_id, timeout)`: blocking in-turn wait, max 180s,
  polls interrupt event so new user input aborts with `status:"interrupted"`;
  timeout framed as not-an-error with a nudge to use notify_on_complete.
- `delegate_task(background=true)` uses the same injection architecture for
  async subagents.
- **Port-worthy:** idle-only injection; consume-dedup across all read paths;
  watch-pattern rate limit + self-disable; "timeout is not an error" protocol.

### oh-my-pi (TS) — richest: auto-background + adaptive ladder + yield queue

- `hub wait` tool: blocks until FIRST of peer message / watched job settling /
  window elapsing / steering interrupt. Never "wait until all done" — model
  re-issues to keep waiting.
- **Adaptive poll ladder** `POLL_WAIT_LADDER_MS = [5s,10s,30s,60s,300s]`
  (`async/job-manager.ts:35`): consecutive quick re-polls climb the ladder;
  reset to floor after 60s of real work (`POLL_ESCALATION_RESET_MS`).
- **Auto-background**: bash/eval/task race
  `raceJobSettlement(job.completion, 60s, abortSignal, steeringSignal)`
  (`async/auto-background.ts`) — finish within 60s → inline result; else tool
  returns "Backgrounded as job bg_N; result delivered automatically."
- **Delivery**: job settles → `AsyncJobManager` delivery loop (retry 500ms
  ×2 cap 30s + jitter) → session sink → `async-result` custom message →
  per-session `YieldQueue` idle-flush → loop's `getFollowUpMessages()` at
  stop/yield boundary → `continue` with new turn (agent-loop.ts:1480).
  Multiple completions coalesce into ONE async-result turn.
- Steering signal aborts in-flight waits cooperatively
  (`STEERING_INTERRUPT_POLL_MS=250`).
- Bounds: max 15 running jobs, 5min result retention with eviction,
  `consumeJobResults` dedup.
- **Port-worthy:** auto-background race (threshold window then hand back a
  handle); adaptive ladder; coalesce multiple completions into one turn;
  yield-boundary-only delivery.

## The convergence (what "excellent" looks like)

Every good implementation agrees on:

1. **Harness owns the timer** (goroutine/fiber/asyncio task/daemon), never the
   model. Zero LLM turns during the wait.
2. **Completion enters the loop ONCE**, as a message, at a boundary:
   busy → steered into the running turn's next boundary (whip `Steer`);
   idle → wake → fresh turn (exo/opencode/oh-my-pi).
3. **Exactly-once delivery** with dedup when multiple read paths exist.
4. **Interruptible waits**: blocking tools select on user-input/steer signals
   (codex activity watch; oh-my-pi steering abort; hermes interrupt event).
5. **Anti-loop protocol in the tool docs**: "you will be notified — DO NOT
   poll"; timeouts framed as not-errors.
6. **Coalescing**: N concurrent completions → 1 follow-up turn (opencode
   pendingWake; oh-my-pi batched async-result).
7. **Bounds everywhere**: max live handles, retention, timeouts, rate limits.

## Proposed whip shape (candidate for sign-off)

`wait` tool: `{command, until? (regex), interval? (default 10s), timeout?
(default 10m, cap 1h)}` → returns immediately with a wait id; poller goroutine
loops `command` at interval; on condition/timeout/error → ONE message:
busy → `Agent.Steer("[wait <id> done] …")` (drains at next loop boundary);
idle → TUI wake hook → `submitTurn(msg, false)`.

Needs: idle-wake hook on Agent (TUI installs, nil headless); bounded
concurrent waits registry; reuse bashrun for command execution; system-prompt
line steering model from sleep loops to `wait`.

Later (from research, optional): auto-background for bash (60s race → handle),
adaptive interval ladder, wait-pattern watches with rate limits.
