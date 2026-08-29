# Why opencode feels fast — research distillation (sub-7, 2026-08-05)

User observation: whip and opencode on the same models — opencode feels
"blazing fast". Suspicion was parallel command dispatch. Research says the
dominant factors are elsewhere.

## Ranked causes (file:line in opencode repo, ~/code/coding-harnesses/opencode)

### 1. Provider prefix caching (dominant — cuts TTFT on every intra-turn call)

- **Native runtime auto cache policy, always on:**
  `packages/llm/src/cache-policy.ts` — stamps `cache_control: ephemeral`
  breakpoints at (a) last tool definition, (b) last system part, (c) **latest
  user message**. Comment: "the latest user message stays put while a single
  turn explodes into many assistant/tool round-trips, so caching at that
  boundary lets every intra-turn API call hit the prefix". Applied at
  `route/client.ts:344-345`, policy "auto" default, restricted to
  anthropic-messages/bedrock-converse.
- **AI-SDK path also stamps caching:** `provider/transform.ts:366-407`
  `applyCaching` — cache_control on first 2 system + last 2 non-system
  messages (anthropic/openrouter/bedrock/openai-compatible/copilot).
- **OpenAI: stable `promptCacheKey = sessionID`**
  (`provider/transform.ts:1260-1273`; gpt-5 also :1317-1321 +
  `reasoningSummary:"auto"`). Same key all session → cached prefix reused
  across turns.
- Micro-opt: cache-policy avoids `.map()`, mutates a `messages.slice()` copy
  because it runs per request on long conversations.

Effect: intra-turn tool round-trips (5–20 per task) get cached-prefix TTFT
(tens of ms) instead of full-prompt TTFT (seconds). Most agent-loop wall-clock
is sequential first-token waits — this is plausibly THE "feels fast" factor.

### 2. O(1)-per-delta rendering (Solid fine-grained store)

- Deltas publish immediately: `session.updatePartDelta` →
  `events.publish(PartDelta)` fire-and-forget (`session/session.ts:877-884`).
- SSE transport unbuffered: `Cache-Control: no-cache, no-transform`,
  `X-Accel-Buffering: no` (`server/routes/.../handlers/event.ts:79-84`).
- TUI mutates one part field via Solid `produce`/`reconcile` inside `batch()`
  (`packages/tui/src/context/sync.tsx:343,376-415,505`) — only subscribed
  components re-render. NO full-transcript re-render per delta.
- No throttle/debounce/rAF anywhere in the streaming path.

### 3. Tool execution overlapped with the stream

- AI-SDK path: SDK dispatches tools, results stream back into `fullStream`
  (`session/llm.ts:280`, `processor.ts:642-646`). Native path: tool calls
  dispatched to `FiberSet.run(..., {startImmediately:true})`, results queued
  back into the LLM event stream (`session/llm/native-runtime.ts:103-137`).
- Per-file `Semaphore(1)` only for `edit` (`tool/edit.ts:35-45,88`);
  write/apply_patch unlocked; no global tool mutex.
- Tool parts materialize in the UI on `tool-input-start` — user sees the tool
  BEFORE it runs (`processor.ts:315-335`).

### 4. Non-critical work forked off the hot path

Title gen, session summary, pruning, compaction all `Effect.forkIn(scope)` /
`Effect.ignore` (`prompt.ts:1133-1139,1253,1338`, `processor.ts:476`) — never
delay the next step.

### 5. What it does NOT do

- Loop is strictly serial per turn (`prompt.ts:1141-1336`) — NO speculative
  execution, NO next-request pipelining. The user's "parallel dispatch"
  intuition is only partially right.
- No bespoke HTTP pooling (undici default keep-alive).
- No server-side delta batching; no second throttle.

## Implications for whip (Go + bubbletea)

whip already has: 25fps coalesced streaming, goroutine-per-tool parallel
dispatch with per-path semaphores + global bash lock, detached prog.Send.

Gap analysis (to verify):
1. **`internal/llm` likely sends no cache_control / prompt_cache_key** —
   highest-value change. Add: byte-stable system prompt + deterministic tool
   ordering, ephemeral breakpoints at [last tool def, last system part, latest
   user message] for anthropic-shaped endpoints, `prompt_cache_key =
   sessionID` for OpenAI-shaped ones.
2. **Render cost per delta** — bubbletea re-runs `View()` per message; if
   View re-renders the whole transcript string per delta that's O(transcript)
   per tick. whip coalesces to 25fps which bounds it, but check transcript
   block caching (blocks re-render on resize — is there a width-keyed cache?).
3. Tool spawn feedback — ensure OnToolStart renders before execution (probably
   already; verify for subagent calls — see spawn-lag thread).

No action on speculative execution — opencode does none.
