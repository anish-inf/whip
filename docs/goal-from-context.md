# `/goal-from-context`

Turn the tail of your conversation into a goal, then let whip work on it
until it's verifiably done — without you writing the goal statement yourself.

Type it mid-conversation:

```
/goal-from-context
```

whip reads the last 8 messages, distills them into a concrete goal, sets it
(exactly like `/goal <text>`), and starts the goal loop immediately.

## The 30-second version

1. Chat with whip until the task is clear in the transcript ("that test is
   flaky, here's the failure…").
2. Type `/goal-from-context` — whip shows `◎ formulating goal from the last
   8 messages…`, then `◎ goal set: …` with what it distilled.
3. whip keeps working — running tools, verifying — until the model declares
   `GOAL_MET`. Walk away.

## Examples

**From a bug discussion to a fix, hands-free:**

```
you>  TestSamePathEditsSerialize flakes under -race, here's the output…
whip> The semaphore acquire happens before the canonical-path lookup…
you>  /goal-from-context

◎ formulating goal from the last 4 messages…
◎ goal set: Fix the flaky TestSamePathEditsSerialize under -race.
  - root cause: semaphore acquired before canonical-path resolution
  - internal/agent/filelocks.go must pass go test -race -count=20
  - no regression in TestToolCallsRunInParallel
```

The loop takes it from there — whip edits, runs the race tests, checks the
criteria, and stops only when verified.

**Bigger window when the task spread over a long conversation:**

```
/goal-from-context 20
```

distills the last 20 messages instead of the default 8.

## How it works

1. **Window.** `agent.GoalFromContextMessages` takes the last *n*
   conversation messages (default 8, clamped to history; fewer than 2 →
   error "chat a bit first"). The system prompt is skipped.
2. **Distill.** One non-streaming `Complete` call on the **current model**
   (the compact-model override is deliberately ignored — this goal is for
   the model you're talking to). The prompt
   (`agent.BuildGoalFromContextPrompt`) asks for exactly one thing: a first
   line stating the concrete outcome, then bullets of checkable completion
   criteria — files to change, commands that must pass, behavior to confirm.
   Long fields are truncated (2000 chars text, 500 for tool bits), so the
   call is cheap.
3. **Set + submit.** On success the TUI calls `setGoal(goal)` (persisted via
   `store.SetGoal`, so it survives `/resume`) and `submit(goal)` — identical
   UX to typing `/goal <text>` yourself. The goal loop then re-prompts after
   every turn until the model replies with the literal token `GOAL_MET`
   (details: `internal/tui/goal.go`), capped at the goal-rounds limit
   (`/goal rounds`).
4. **Safety.** While a turn is in flight the command refuses with a note
   instead of queueing — by the time the turn ends, the context (and the
   right goal) has changed. Esc cancels the formulation call. A failed or
   empty formulation lands as a transcript note; nothing aborts, no goal is
   touched. `/resume` and `/clear` refuse while a formulation is in flight.

## When to use it

- **The conversation already contains the spec.** You've been debugging or
  designing in chat; the what and the done-criteria are on screen. Re-typing
  them as a `/goal` line is friction — let the model do the distilling.
- **You want the goal to stand alone.** The distilled goal carries file
  paths, function names, and error messages forward even as compaction folds
  old turns away.
- **You're stepping away.** `/goal-from-context` + the loop = whip verifies
  its own work instead of stopping at "looks right."

When *not* to use it: you already know the exact one-line goal — just type
`/goal <text>`; formulating from zero context (fewer than 2 messages) — the
command will tell you to chat first.

## Why it exists

`/goal` is whip's "work until done" mode, but writing a *good* goal
statement — concrete outcome plus verifiable criteria — is the hard part,
and the information is almost always already in the transcript. So the
command moves the work from your keyboard to the model that has the context:
read the tail, write the goal, start the loop. One keystroke from "we just
figured it out" to "it's being finished."

## Where it lives

| piece | file |
| --- | --- |
| window + prompt (pure, tested) | `internal/agent/agent.go` — `GoalFromContextMessages`, `BuildGoalFromContextPrompt` |
| command + goroutine + Update handler | `internal/tui/tui.go` — `case "/goal-from-context"`, `goalFromContextMsg` |
| goal loop | `internal/tui/goal.go` |
| tests | `internal/tui/goal_test.go` (`TestGoalFromContext*`) |
