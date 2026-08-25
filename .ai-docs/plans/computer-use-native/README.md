# computer-use: the ultimate version — native, embedded, semantic-first

**Branch:** `computer-use-native` (off `main`) · **Status:** steps 1–4 built (INF-4999); TCC window (5) wired; granted-app E2E (6) pending the one-time user TCC grant

The plan for the full-desktop tier: everything codex's native driver does
(ScreenCaptureKit capture, CGEvent input, AX-tree reads), built into whip as
one binary with a seamless first-run. Codex's architecture is dissected in
`docs/learnings/other-harnesses/codex-computer-use-plugin.md` (INF-4997);
this plan beats it on grounding (their pixel-guessing → our AX-first), UX
(their separate app + in-turn TCC wait → our embedded helper + one grant
flow), and openness (theirs is closed → ours is in-repo).

## The one-sentence architecture

A single **`whip-computer` helper binary** (Swift, built in CI, signed,
embedded into the whip binary via `go:embed` per-arch), talking JSON-RPC
over a stdio pipe, behind `internal/computer`'s existing `Backend`-shaped
interface. Whip stays `go build ./...`-able on every OS; on macOS the
helper extracts to `~/.whip/bin/whip-computer` on first use and the user
grants TCC once.

## Why embed, and why it works

- **`go:embed` + extract-once**: the helper ships inside the whip binary,
  written to a stable path on first run. No separate installer, no app
  bundle to drag anywhere — the codex dance (a separate `Codex Computer
  Use.app` + Installer) disappears.
- **Stable identity = sticky TCC.** Permissions bind to the binary's code
  signature + path. A fixed path (`~/.whip/bin/whip-computer`) and a
  stable signature mean Accessibility/Screen Recording/Automation are
  granted *once*, forever — vs an ad-hoc binary re-prompting every run.
- **Why a helper at all (not CGO)?** TCC on macOS grants to the *process
  bundle*. CGO-in-whip would bind Screen Recording to the whip binary
  itself — fine, but a helper gives us: (a) crash isolation (a wedged
  driver never takes the agent loop down), (b) a separate code-signed
  identity with its own Info.plist usage strings (the permission prompts
  say "whip-computer needs…", not a generic terminal), (c) the Swift/SCK
  toolchain off the critical `go build` path (CI cross-compiles the helper
  on a mac runner; `go build` on Linux embeds a stub).
- **JSON-RPC over stdio, not XPC.** Codex used XPC + Mach ports + sender
  code-signing auth (`cua_ipc_sender_*`). We don't need it — the helper is
  spawned by whip and never shared cross-process, so stdio JSON-RPC with a
  handshake token (the parent passes it via env; the helper refuses
  connections without it) is the right-sized security boundary. Their XPC
  ceremony buys cross-app trust we don't have.

## The driver (Swift, ~1.5k LOC) — what it implements

From the dissection, the exact surface (`SkyComputerUseClient` equivalent),
ported and simplified:

| Capability | macOS API | Their approach | Ours |
|---|---|---|---|
| Screen capture | ScreenCaptureKit (`SCStream`) | full-fidelity Retina, normalize to points, JPEG | same: normalize to points, JPEG, quality knob |
| Mouse/keyboard | CGEvent posting | CGEventTap, source state, per-app focus enforcement | CGEvent; focus via AX raise |
| UI reads | Accessibility (`AXUIElement`) | tree + focus/selection tracking, observers | tree walk + focused/selected state |
| App control | NSRunningApplication / LaunchServices | launch-on-demand, per-app serial executor | same (serialize per app) |
| Lock screen | separate XPC helper app | password-authorized unlock | **v2** — refuse when locked (`com.apple.screenIsLocked` → actionable error) |

Theirs had a lock-screen auto-unlock broker and per-app steering files; ours
cuts both from v1 (lock → clear error; steering → later).

## The model-facing contract (where we're better)

Their loop: `get_app_state` → act → **mandatory re-query** (the state-gate).
Ours keeps that AND improves it:

1. **Mutations return state in-call** (our browser_exec already does this) —
   the re-query is folded in; one call instead of two.
2. **AX index stability**: every `ax()` response carries a monotonically
   increasing generation; an action with a stale index errors with "state
   changed — re-read" instead of clicking the wrong thing (their gate is
   per-turn; ours is per-action — tighter).
3. **The same `code` mini-language** as browser_exec/computer_exec — one
   tool, `computer_exec`, grows the new helpers; no new tool surface.

## Helper/tool surface (the model sees)

```
apps()                       running + recently-used apps (bundle id, name)
state(app)                   AX tree (indexed) + screenshot → in-call
click(app, index)            AX-element click (preferred) — state returned
click(app, x, y)             pixel click (fallback; point space, retina handled)
type(app, text)              CGEvent keyboard
press(app, key)              xdotool syntax ("Return", "super+c", "KP_0")
scroll(app, index, dir, n)   AX scroll
set(app, index, value)       settable AX element value
select(app, index, text)     select text / place cursor (prefix/suffix disambiguate)
menu(app, index, action)     secondary AX action by name (expand, show menu, …)
screenshot(app)              → inline image part (existing ScreenshotSink path)
```

## Permissions UX (the seamless part)

One first-run flow instead of codex's separate installer app:

1. First `computer_exec` call on macOS → helper extracts, launches, and
   **pre-flights TCC**: AX check → Screen Recording check → Automation.
2. Missing grants → the helper presents ONE window (like codex's
   `SystemSettingsCoordinator`) with deep links to the three panes
   (`x-apple.systempreferences:` anchors from the dissection), and the
   **tool busy-waits in-turn** ("grant Accessibility in the window — I'll
   continue when it's done") — codex's exact trick, kept because it's
   genuinely better than erroring.
3. Granted → cached for the stable binary; never re-prompts.

## Security (ported, tightened)

- Per-app consent: the existing `internal/computer/policy.go` (already
  codex-shaped) drives the gate; the helper refuses to touch unapproved
  apps (belt + suspenders: whip gates before sending, helper re-checks).
- The confirmation taxonomy from their SKILL.md (hand-off / confirm-always /
  pre-approve / never) becomes whip's reviewer rules — v1 wires the
  four-tier taxonomy into the tool's error strings and a
  `computer.review` config for the Guardian-style second-model pass (v2).
- Screen content stays "untrusted evidence" — the tool description says so,
  and any Guardian pass gets the same full-fidelity frame (not their
  downgraded reviewer image).

## Build & distribution

- `driver/` — Swift package (`Package.swift`), built in CI on macos runners,
  signed with the project's Apple cert (or ad-hoc + documented
  `xattr -d com.apple.quarantine` for dev), per-arch artifacts.
- `go:embed` in `internal/computer/embed_darwin_arm64.go` /
  `embed_darwin_amd64.go` (build-tagged); a `embed_stub.go` (other platforms)
  embeds nothing. `go:embed` needs the bytes at build time, so CI builds
  helper-then-whip; local dev on Mac uses `task driver` to rebuild.
- Version handshake: helper prints its protocol version on launch; whip
  refuses mismatches (codex's `CodexComputerUseIPC-4` lesson).

## Platforms

macOS v1 (the user's machine). The `internal/computer` interface seam keeps
Linux (AT-SPI + XDG portal screenshot + xdotool) and Windows (UI Automation +
SendInput + Desktop Duplication) as later ports — each is the same helper
shape on a different backend, behind the same JSON-RPC contract.

## Build order

1. ✅ `driver/` Swift package: JSON-RPC stdio server + TCC preflight + AX tree
   + CGEvent input + SCK capture. Tests on a mac runner. (INF-4999)
2. ✅ `internal/computer/embed*.go` + a `Helper` client (spawn, handshake,
   JSON-RPC round-trips, crash-restart). Fake-helper Go protocol tests.
3. ✅ Wire the new helpers into `computer_exec` (the mini-language grows the
   verbs above; existing chrome_* helpers stay). Screenshots ride the
   existing `ScreenshotSink`.
4. ✅ The AX-generation staleness guard (per-action state gate, tighter than
   codex's per-turn one). Stale index → code 4 "state changed — re-read".
5. ✅ Permissions window (single window, three panes, in-turn wait) —
   `permissions.request` busy-waits in-turn up to 120s behind a deep-link
   panel. Exercised: pending path verified; the grant-click is the user's.
6. ⏳ E2E on the Mac: handshake/apps/error-gates verified against the real
   helper. The granted AX walk + CGEvent input + SCK capture against
   Chrome/TextEdit needs the one-time TCC grant for `~/.whip/bin/
   whip-computer` (a user System Settings click — the design's whole point).

## What we do NOT copy

- XPC/Mach transport ceremony (stdio + token is enough — no cross-app trust
  boundary).
- Lock-screen auto-unlock (v2; v1 refuses with an actionable error).
- Per-app steering files (later; generic AX works for v1).
- Their `SerialExecutor`-per-app as the only synchronization — ours adds
  the in-call state return so the model doesn't pay a second round trip.

## Test plan

- Driver: Swift XCTest on a mac runner (AX walk of TextEdit, CGEvent
  round-trip, SCK capture size/format).
- Go: fake-helper protocol tests (handshake, version mismatch, crash
  restart), policy + staleness-gate units, E2E on the Mac via the real
  helper.
- Gate: `task check` + `-race` everywhere.
EOF
