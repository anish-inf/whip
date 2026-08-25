# Browser: hermes-style auto-launch fallback

Branch: current working branch (browser work lands here)

## What this does

When the default `live` mode finds no debuggable browser, whip currently
returns `ErrNoLiveBrowser` and the model asks the user to set up debugging.
Port hermes-agent's `/browser connect` UX: **auto-fallback** — probe live,
and if nothing debuggable is running, silently launch (or reuse) a
dedicated-profile Chrome and use it for that session. Zero user setup.

## Goal

- Default config ("live") never dead-ends on `ErrNoLiveBrowser` when a
  Chromium binary exists: the tool call succeeds against a launched
  dedicated browser and its output carries a one-line notice.
- Port-squatter resilience: a launched dedicated browser survives something
  else holding 9222 (we bind port 0 / random free port — already the case)
  and discovery probes **both** loopbacks (127.0.0.1 + [::1]).
- Reuse: a previously-launched whip dedicated Chrome that is still alive
  is reattached (profile DevToolsActivePort scan) instead of spawning a
  second instance.

## Non-goals

- No browser extension relay (that's the follow-up exploration).
- No relaunch of the user's main Chrome with debug flags (dead end on
  Chrome ≥ 136 default-profile anyway).
- No new config keys, no new dependencies, no CLI/UI surface changes.
- Explicit `dedicated:foo` / `headless:foo` session prefixes are unchanged
  (user asked for that mode; no fallback semantics needed — though the
  launch path is shared).

## Prior art (citations)

- `hermes-agent/hermes_cli/browser_connect.py:187-207`
  (`discover_local_cdp_url`) — dual-stack loopback probing: a squatter on
  127.0.0.1 pushes Chrome to bind [::1] only.
- `hermes-agent/hermes_cli/browser_connect.py:209-225` (`local_port_in_use`,
  `find_free_debug_port`) — distinguish "port squatted by non-CDP app" from
  "free", pick a nearby free port. Go equivalent: we already pass
  `--remote-debugging-port=0` (kernel picks), so squatter-avoidance is free;
  only the *probe* needs dual-stack.
- `hermes-agent/hermes_cli/browser_connect.py:361-419`
  (`launch_chrome_debug`) — try candidate binaries in turn, classify
  ready/exited/starting, stderr-tail diagnostics. Rod's launcher already
  covers most of this; we keep the rod path.
- `hermes-agent/hermes_cli/cli_commands_mixin.py:2470-2500` — auto-launch
  on connect when nothing is listening, then poll until the endpoint is up.
- whip `internal/browser/browser.go:150-186` — existing dedicated launch
  incl. profile-quarantine retry. `attach.go:178-228` — live discovery.

## Design

Surfaces: `internal/browser` only (attach.go, browser.go, session.go).
Tools/TUI untouched — the fallback rides the existing per-session open path.

### 1. `attach.go` — dual-stack probe + dedicated-profile discovery

- `portLive(port)`: dial 127.0.0.1 then [::1] (currently IPv4 only).
- New `DiscoverWSForProfile(base string)`: read
  `<base>/DevToolsActivePort`, verify liveness (portLive + SingletonLock),
  resolve WS URL. Extracted from `DiscoverLiveWS`'s loop body so both the
  well-known-profile scan and the dedicated profile reuse it.
- Squatter robustness in `resolveWSURL`: if `/json/version` returns a
  non-Chrome body (our node-squatter case: Express 404 HTML), treat as
  "not a browser" and keep scanning rather than constructing a bogus WS
  URL. Guard: only trust the DevToolsActivePort wsPath fallback when the
  profile lock matches the profile we're scanning (already true in the
  scan path; the *port fallback* path at attach.go:211-223 must NOT use
  wsPath from an unrelated file — it passes "" today, keep it).

### 2. `browser.go` — Open gains fallback + dedicated reuse

```go
func Open(ctx context.Context, mode Mode) (*Browser, error)
// ModeLive:
//   ws, err := DiscoverLiveWS(ctx)
//   if err == nil            -> connect as today
//   if ErrPermissionBlocked  -> return as today (user must click Allow)
//   if ErrNoLiveBrowser      -> fall through to launchDedicated()
// ModeDedicated/Headless:
//   if ws, ok := DiscoverWSForProfile(dedicatedProfileDir); ok -> reattach
//   else launchDedicated() as today
```

`Open` records how the browser was obtained (new `obtained` field:
`live | launched | reattached`) so the session layer can say what happened.

### 3. `session.go` — surface the fallback once per session

`Session` gains a `notice string` set on first successful open when
`mode == ModeLive` but `b.obtained != live`. `Do` prepends the notice to
the first successful tool output:

> [Note: no debuggable live browser found — using whip's dedicated Chrome
> (logins live in its own profile). To use your everyday browser instead:
> chrome://inspect/#remote-debugging, or set browser.mode in config.]

One line, once per session — the model sees it and can relay naturally.

## Test plan

- `browser_test.go` (unit, fake profile dirs + httptest CDP endpoints):
  - dual-stack `portLive` against a [::1]-only listener.
  - `DiscoverWSForProfile` on a fake dedicated profile (live + stale file).
  - live-fallback: discovery fails (no profiles) → Open returns a launched
    browser — behind the existing E2E Chrome gate, see below.
  - squatter: httptest server on 127.0.0.1:<port> returning 404 HTML +
    a profile pointing at that port → discovery skips it (no bogus WS URL).
- `e2e_test.go` (real Chrome, existing skip-without-binary pattern):
  - dedicated reuse: Open(dedicated), keep Chrome alive, Close backend,
    Open(dedicated) again → same profile dir, session cookie survives,
    no second Chrome process (assert via process count or profile lock).
  - live fallback: with no debuggable live browser (can't guarantee on a
    dev box — simulate by pointing profileDirs at an empty temp dir via a
    package var hook) Open(live) launches dedicated and reports
    `obtained == launched`.
- `go test -race ./internal/browser/...` — session notice set-once under
  parallel `Do` calls is guarded by the existing session mutex.

## Docs plan

- `docs/features.md` Browser automation section: extend the "Three modes"
  bullet — live auto-falls back to a launched dedicated browser (hermes
  /browser connect model), dual-stack probing, dedicated-profile reattach;
  notice line behavior; updated test file references.
- `docs/learnings/browser-use-integration.md`: short addendum noting the
  hermes `/browser connect` auto-launch port (or a new
  `docs/learnings/other-harnesses/hermes-browser-connect.md` note if the
  addendum doesn't fit).
- Roadmap: no checkbox exists for this; no README change (no new config).

## Tasks (ordered)

1. attach.go: dual-stack `portLive`, extract `DiscoverWSForProfile`,
   squatter-guard the WS fallback. Unit tests. ✅
2. browser.go: `obtained` field, live→dedicated fallback, dedicated
   reattach-before-launch. Unit tests with fake dirs. ✅
3. session.go: once-per-session fallback notice prepended in `Do`. ✅
4. E2E: dedicated reuse + live fallback via profileDirs hook. ✅
   (shipped as: real-Chrome E2E on macOS via added Chrome binary candidates;
   `TestE2ELiveFallsBackToLaunched` + `TestE2EDedicatedReattach`)
5. `task check` + `go test -race ./internal/browser/...`, ponytail pass,
   adversarial subagent review of the diff. ✅ (check + race green)
6. docs/features.md + learnings addendum. ✅

## Deviations discovered during implementation

- **Squatter guard needed a WS-upgrade probe, not just a Browser-field
  check.** The plan's "verify it's actually Chrome" was implemented as two
  layers: `/json/version` must return a Chromium `Browser` field, AND the
  DevToolsActivePort-file wsPath fallback must answer a real WebSocket
  upgrade (101) before being trusted. The latter was added because the
  squatter unit test exposed that a stale profile file pointing at a
  squatted port would otherwise hand rod a bogus ws:// URL.
- **`rod.Browser.Close()` kills the whole browser process** (CDP
  `Browser.close`), even on a remote/live attach — contradicting the
  pre-existing "live detaches only" doc comment. Reattach-no-duplicate is
  impossible while Close kills the process. Fixed in `detach.go`: reflect+
  unsafe severs the CDP WebSocket without Browser.close for
  live/reattached/dedicated modes; headless still kills. A launched
  dedicated Chrome deliberately survives `Close` (it's the reattach target)
  and is reaped by the launcher's Leakless pid-guardian when the agent
  process exits.
- **E2E on macOS needed real-Chrome candidates.** `chromiumPath` only knew
  Linux Playwright paths, so E2E tests skipped on this dev box despite
  Chrome being installed. Added macOS .app binary paths so the suite
  actually runs here.
- **Session open indirection for tests.** `Session.get` calls a package var
  `openNamed` so session/fallback/notice orchestration is unit-testable with
  a fake Backend (no browser launch).
