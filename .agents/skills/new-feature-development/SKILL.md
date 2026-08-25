---
name: new-feature-development
description: "Playbook for building a NET-NEW feature in whip. Use when the user wants to add a feature, tool, slash command, integration, or UX behavior ('build…', 'add…', 'implement…'), even without the word 'feature'. Mines docs/roadmap.md + docs/learnings/. NOT for debugging or narrow golang-* changes."
---

# New Feature Development

You are the **architect** for a new feature in whip, a minimal coding-agent
harness in Go. Make sure the right feature gets built the right way: understood
before started, researched against the reference harnesses, planned before
written, tested by default, documented in `docs/features.md`, and green on
`task check` before it's called done.

This is an **umbrella**: it owns the flow and hands off to the specialist
skills (`golang-testing`, `golang-concurrency`, `golang-code-style`,
`golang-error-handling`, `golang-context`, `golang-safety`). When a step says
"use skill X," read its SKILL.md — don't reimplement from memory.

Read repo docs lazily, when the step needs them:

- `docs/features.md` — map of what's shipped: behavior → code → tests. **Read
  before designing; name the files the plan will touch.**
- `docs/concurrency.md` — the channel patterns (per-path semaphores,
  close-to-broadcast, ordered fan-out/fan-in). **Read before writing anything
  concurrent.**
- `docs/roadmap.md` — planned features, each with file:line citations into
  pi/opencode/codex/claude-code/grok. **Read first: the "new" feature is often
  already specced here. Check the box when you ship.**
- `docs/learnings/other-harnesses/` — harness exploration reports. **Read when
  porting a behavior — the research may already be done.**

## Design principles (apply throughout)

- **Minimal is the product — ponytail is on by default.** Before writing code,
  run the ladder: does this need to exist? is it already here (check
  `docs/features.md`)? does stdlib cover it? is it a small composition over an
  already-imported dep? Only then write the minimum that works. A new
  dependency is a big deal — `go.mod` fits on one screen; justify additions in
  the plan. Mark deliberate shortcuts inline: `// ponytail: <what would
  generalize this>` (grep `ponytail` for examples).
- **Learn from the other harnesses, port the Go way.** Don't transliterate
  TypeScript — find the Go-native shape (pi's promise chains became a
  1-capacity channel semaphore; opencode's `Deferred` registry became one
  channel close — see `docs/concurrency.md`). Cite the reference (file:line)
  in the plan and doc comments.
- **Channels over locks; capacity is the contract.** Size results channels to
  the batch; a 1-buffer channel is a mutex; `close(ch)` once wakes all waiters.
  Every goroutine has an obvious owner and exit; everything concurrent passes
  `go test -race`.
- **Errors are tool output, not loop-abort.** Tool failures return
  `"Error: <msg>"` strings fed back to the model (`tools.Execute`) — a broken
  tool never kills the turn. Reserved `error` returns are for infrastructure.
  Bound everything: `maxOutput` truncation markers, timeouts always.
- **Context flows, cancellation is real.** `ctx` threads from TUI keypress
  through `Agent.Turn` into every tool; ctrl+c must actually stop in-flight
  work. No `context.Background()` in library code.
- **Config is guarded on write.** `~/.whip/config.json` writes are atomic
  (tmp+rename, `.bak` kept) with a clobber-refusal guard. Never bare
  `os.WriteFile` persisted state.
- **Docs are part of the diff.** A feature without its `docs/features.md`
  section (behavior → code → tests) and roadmap checkbox is incomplete.

## The loop

Steps 0–2 are cheap and prevent expensive mistakes; don't skip them because the
feature "seems small." Steps 3–7 are the build.

### 0. Clarify ambiguity before doing anything

Building the wrong thing well is the most expensive outcome. Ask about: the
actual user problem (not the proposed solution), where it lives (tool? agent
loop? TUI? config? session store?), what the model sees vs what the human sees,
what "done" looks like, non-goals, and which harness's behavior it's meant to
match or beat. Short batch of concrete questions. If the brief is rich, confirm
understanding in two sentences and flag only genuine unknowns.

Rule of thumb: **you should be able to write the plan's "Goal" and "Non-goals"
without guessing.**

### 1. Mine the prior art

Read `docs/roadmap.md` and `docs/learnings/` first. If porting harness behavior
and the research isn't done, do it now: find the reference implementation (pi
at `~/code/pi`, opencode under `~/code/coding-harnesses/`), understand *why*
it's built that way, sketch the Go-native port. Cite findings in the plan — a
reviewer should check the port against the original, not your memory.

### 2. Understand the architecture you're building into

Read `docs/features.md` end to end. Non-negotiables:

- **Five surfaces.** A new tool (`internal/tools` — a `Tool{Def, Run}` appended
  in `agent.New`; remember subagents get `tools.All()` too), agent-loop
  behavior (`internal/agent`), TUI interaction (`internal/tui` — a case in the
  `command()` switch, palette panel, key handling, transcript block), config
  (`internal/config`), or persistence (`internal/session` — SQLite;
  `Save(id, from, msgs)` writes incrementally). The plan names surfaces and
  files.
- **The tool contract is load-bearing.** Defs go to the provider verbatim;
  outputs are strings capped at `maxOutput`; failures return error strings.
  Parallel execution is the default — mutations declare their path for
  `toolMutationPath` or take the global bash lock. Design for parallel calls
  and `-race`.
- **Two places for state.** `Agent.Messages` + transcript blocks in-session;
  `session.Store` across sessions. No third place without justification.
- **The TUI is a busy/idle state machine.** Turns run in goroutines that
  `p.Send` messages back; keys mean different things busy vs idle (two-stage
  ctrl+c). Rendering is width-dependent; blocks re-render on resize.

### 3. Write a living plan

Create `.ai-docs/plans/<slug>/README.md`: H1 title, `Branch:` line, "What this
does" / "Goal", "Non-goals", design (surfaces, file paths, sketches of new
types and channel lifecycles), prior-art citations, test plan, docs plan (the
`features.md` section + roadmap checkbox), ordered task breakdown.

This is **the shared source of truth** — you, delegated subagents (`task` tool,
`background: true` for parallel lanes), and reviewers read it. Keep it current:
check off tasks, record deviations, leave breadcrumbs for a fresh agent to pick
up mid-stream. A stale plan is worse than none.

**Checkpoint:** get user sign-off before writing code.

### 4. Implement — minimal, concurrent-safe, channel-idiomatic

Type it yourself or delegate well-specified units to `task` subagents; keep
judgment (architecture, interfaces, verifying diffs) yourself.

- Follow the golang-* skills (`golang-code-style`, `golang-naming`,
  `golang-concurrency`, `golang-error-handling`, `golang-safety`,
  `golang-context`).
- **Pure logic at the core, I/O at the edges** — the pattern behind
  `buildSummaryPrompt`, `truncate`, `toolMutationPath`: trivially tested pure
  functions, thin syscall/network wrappers.
- **Extend, don't parallel-track.** A case in the slash-command switch, a field
  on `Config`, a block type — not a second dispatcher/config file/renderer.
- New concurrency follows `docs/concurrency.md` patterns or the plan explains
  why not. Race-clean under `go test -race ./...`.

Keep the `.ai-docs` plan current as code lands.

### 5. Session persistence and resume are part of the feature

If the feature adds message-adjacent state (new message fields, block types,
goal-like loops), ask: does a resumed session behave correctly?
`session.Store.Save` serializes `llm.Message`s; `--resume`/`/resume` re-renders
the transcript. New persisted shapes need a resume test, not just a
live-session test. Record deliberately when a feature is intentionally
live-only.

### 6. Test at the right level — race included

Use `golang-testing`. The repo is stdlib `testing` all the way down — stay
consistent. Choose by what you're de-risking:

- **Unit** — pure logic (canonicalization, truncation, parsing). The default.
- **Loop tests against a fake provider** — `agent_test.go` spins an httptest
  server speaking streaming chat-completions; that's how Turn, compaction,
  parallel tools, and background tasks are pinned. New loop behavior gets one.
- **Headless TUI tests** — drive the bubbletea `model` directly (`m.prog ==
  nil` paths exist for this), assert on rendered blocks; include resize cases
  where width matters.
- **Concurrency proofs** — tests that would fail under interleaving
  (concurrency counters, many-waiter wakeups), everything under `-race`.

Under-tested = a plausible regression fails no test. Name the tests in the
`docs/features.md` section like existing entries do.

### 7. Gate on `task check`, then review adversarially

- **`task check` must pass** — `gofmt -s` + `go vet` + `go test ./...`, what
  the pre-commit hook runs (`task hooks` installs it). Add `go test -race
  ./...` when you touched concurrency. `go.mod` changed → `task tidy` +
  justification.
- **Least-code pass, then adversarial.** Re-read the diff against the ponytail
  ladder — cut or `// ponytail:`-mark anything deletable. Then attack it: what
  breaks under parallel tool calls? ctrl+c mid-turn? resume from SQLite? narrow
  terminal? clobbered config? Race the diff past a background subagent told to
  find correctness and simplicity problems. Fix what survives.

### 8. Close the loop: docs and roadmap

Same change: the `docs/features.md` section (behavior → code → tests), the
`docs/roadmap.md` checkbox if listed, `docs/concurrency.md` if you added a
pattern worth teaching, `docs/learnings/` notes if you researched a harness.
README only if the user-facing surface changed (flag, config key, command).

## Anti-patterns to refuse

- Building something `docs/features.md` or `docs/roadmap.md` already describes
  under another name — check the map first.
- Transliterating TypeScript from pi/opencode instead of the Go-native shape.
- A tool that aborts the loop on failure (return `"Error: …"` instead), or
  returns unbounded output with no truncation marker.
- A goroutine with no owner or exit, an unbounded channel "to be safe," a mutex
  where a sized channel says it better — or any concurrency that hasn't seen
  `go test -race`.
- `context.Background()` in library code; work ctrl+c can't stop.
- Bare `os.WriteFile` on config or any persisted file; persisting without
  asking what a clobbered/partial file does on next load.
- New dependencies stdlib or an existing import already covers.
- Business logic in the TUI update loop that belongs in `internal/agent` or
  `internal/tools` (the TUI renders and routes keys; the loop does the work).
- Skipping clarify/plan and building a fuzzy spec.
- Calling it done with `task check` red, without `-race`, without the
  `features.md` section, or without the adversarial pass.
