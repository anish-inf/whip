# Rewind, /fork, /rename — conversation time travel

Branch: `rewind-fork-rename` — **SHIPPED** (working tree; task check + race green)

## What shipped

1. **Rewind** — double-`esc` while idle opens a picker over authored user
   messages (newest first); transcript live-scrolls while browsing; enter
   rewinds to just before the selection with the text restored into the
   input. Reopening while rewound lists clipped messages dimmed (`(rewound)`)
   and enter on one moves **forward**. Submitting new input or compaction
   discards the redo stack. esc cancels and restores the scroll position.
2. **`/fork [name]`** — copies the conversation (through a picked message, or
   in full) into a NEW session with a chosen title and switches to it. Bare
   `/fork` prompts inline with `<title> (fork #N)` auto-suggested. `f` in the
   rewind picker forks from the selection (the copy keeps the picked message;
   rewind would drop it). The original session is untouched.
3. **`/rename [title]`** — retitles the current session; bare prompts
   prefilled with the current title. Both prompts stash/restore the input
   draft. Palette entries for all three; slash completion updated; /help
   updated.

## Decisions recorded

- **seq == conversation index** in the messages table (`Save` persists
  `msgs[i]` at seq i; system prompt never stored). `DeleteFrom(id, cut)` and
  `Fork(id, uptoSeq)` (copies `seq <= uptoSeq`) take conversation indices.
  (The adversarial pass caught a `cut-1` data-loss bug here.)
- Rewind is destructive in the DB; redo stack is in-memory only. Quitting
  while rewound leaves the DB at the rewound point. Deliberate.
- `/fork`, `/rename`, palette rewind/fork/rename refuse while busy (fork
  mid-turn would split the in-flight turn across two sessions).
- Idle esc dismisses open UI first (menu/queue/dock/prompt); dismissal does
  NOT count toward the double-esc (stale-arm fix).
- No file/git revert (opencode `revert.ts` snapshots) — conversation-only.
- `msgBlock` maps conversation index → transcript block for live-scroll;
  rebuilt by `seedTranscript`/`rebuildTranscript`, extended in `submitTurn`
  (index captured BEFORE the turn goroutine launches — a race fix), cleared
  on compaction//clear/resume.

## Files

- `internal/session/session.go` — `DeleteFrom`, `SetTitle`, `Fork`,
  `ForkTitle`, `likeEscape`
- `internal/tui/rewind.go` — picker state, keys, applyRewind, view
- `internal/tui/fork.go` — namePrompt, forkCommand/openForkPrompt/fork,
  renameCommand/rename
- `internal/tui/tui.go` — double-esc case, msgBlock, prompt esc/enter,
  command cases, View strip + hints
- `internal/tui/palette.go`, `internal/tui/complete.go` — entries + completion
- Tests: `internal/session/fork_test.go`, `internal/tui/rewind_test.go`,
  `internal/tui/fork_test.go`

## Adversarial pass findings → fixes

1. HIGH: DeleteFrom(cut-1) off-by-one ate the last kept message in the DB —
   fixed to `cut`, docs corrected, partial-rewind DB test added.
2. Stale esc arm across modal dismissal — cleared on dismiss + test.
3. namePrompt destroyed the input draft — stash/restore + test.
4. fork/rename/palette entries not busy-gated — gated.
5. Compaction while rewound left a stale redo stack — `future`/`msgBlock`
   cleared in compactMsg handler.
6. Fork semantics settled: **copy keeps the selected message** (inclusive),
   rewind is exclusive (drops it); fork truncates the live conversation at
   the picked point (it IS the switch to the copy).
7. ForkTitle: trim, strict suffix scan (manual renames don't inflate N).
8. Dead code cut: entryCut indirection inlined, discardFuture one-lined.
9. A real race found by `-race`: submitTurn read len(agent.Messages) after
   spawning the turn goroutine — captured before.

## Verification

- `task check` green (gofmt -s, vet, tests)
- `go test -race -count=1 ./internal/...` green
- Docs: `docs/features.md` "Conversation time travel"; roadmap boxes checked
  (`/rename`, `/fork`, Timeline, conversation-half of Undo).
