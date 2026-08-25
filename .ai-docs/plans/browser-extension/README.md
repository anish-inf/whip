# Browser extension relay — drive the user's real Chrome

Branch: main

## What this does

A Chrome extension that turns the user's **real, already-running,
already-logged-in browser** into a browser_exec backend — the one thing CDP
can't do on Chrome ≥ 136 (default-profile CDP is blocked). whip runs a
local relay; the extension connects out to it; `browser_exec` commands route
to the tab the user pinned.

## Goal

- **Dead-simple install**: `whip browser install` writes the extension to
  `~/.whip/browser/extension/` and opens `chrome://extensions` + the
  unpacked dir. User does the one unavoidable manual step (toggle Developer
  mode, Load unpacked, pick the dir — Chrome forbids programmatic install).
- **One-click attach**: click the extension's toolbar icon on the tab you
  want whip to drive. The icon shows a badge when attached.
- **`browser.mode: "extension"`** (config) routes `browser_exec` to the
  attached tab with the user's real cookies/sessions. No other mode changes.

## Non-goals

- No Chrome Web Store / enterprise policy install (unpacked only).
- No multi-tab fan-out; one attached tab per relay at a time.
- No cloud/remote browser — loopback only, never exposed off localhost.
- No rewrite of the Browser/Backend abstraction — the extension backend
  implements the existing `Backend` interface.
- No new Go module dependencies (WebSocket via `gobwas/ws`, already vendored
  through go-rod).
- Firefox/Safari — Chrome/Chromium MV3 only.

## Why a relay (and how the extension drives the page)

Chrome extensions can't open a CDP socket to their own browser. But an MV3
**background service worker can hold an outbound WebSocket** to a loopback
relay, and once attached to a tab it can use **`chrome.debugger`** (raw CDP:
`sendCommand`/`onEvent`) — so the extension is effectively a CDP *tunnel*:
whip's relay speaks the same CDP wire protocol rod already uses, the
extension forwards it to the attached tab via `chrome.debugger`. This keeps
whip's existing rod Backend 100% intact — the relay is just a CDP endpoint
that happens to be the user's real Chrome.

Trade-off accepted: `chrome.debugger` shows Chrome's "… is debugging this
browser" infobar while attached. The alternative (content-script DOM
driving, OpenClaw-style) has no infobar but can't do trusted input, full
nav, or screenshots without reimplementing each — reusing rod over the CDP
tunnel is dramatically less code and more capable (ponytail: reuse the
existing Backend, don't write a second driver).

## Design

```
 browser_exec → browser.Manager (mode=extension)
      → extBackend (implements Backend; thin: rod over the relay's CDP ws)
            ↕ CDP WebSocket (127.0.0.1:PORT)
      relay (internal/browser/extrelay.go, served by the whip process)
            ↕ WebSocket + tiny JSON envelope {id, method, params | result | event}
      extension background SW (chrome.debugger.sendCommand → attached tab)
```

- **Relay** (`internal/browser/extrelay/`): stdlib `net/http` on an ephemeral
  127.0.0.1 port + `gobwas/ws` upgrade. Two WS endpoints: `/ext` (the
  extension) and `/cdp` (rod). Bridges CDP between them. Bearer token
  (per-process random) required on the extension handshake; written to
  `~/.whip/browser/extension/relay.json` (0600) by `whip browser install`.
- **Extension** (`internal/browser/extrelay/extension/`, embedded via
  `//go:embed`): `manifest.json` (MV3, perms: `debugger`, `activeTab`,
  `tabs`), `background.js` (action.onClicked → attach debugger → open WS →
  forward CDP; disconnect → detach + clear badge).
- **Backend**: `Open(ctx, ModeExtension)` connects rod to the relay's `/cdp`
  endpoint. Existing `Browser` methods work unchanged.
- **CLI**: `whip browser install` writes embedded extension files +
  `relay.json`, prints the exact 3 manual steps, opens the two windows.

## Test plan

- `extrelay` unit tests: WS handshake + token auth (reject bad/absent
  token), CDP request→extension→response round-trip against a fake extension
  client (gobwas/ws client in-test), event fan-in, extension disconnect →
  backend sees closed connection.
- E2E (existing real-Chrome gate): launch Chrome with the extension loaded
  (`--load-extension`), attach a tab, drive navigate/eval/screenshot through
  the relay — proves the full path on a real browser.
- `task check` + `go test -race ./internal/browser/...`.

## Docs

- `docs/features.md`: extension relay section under Browser automation.
- `docs/learnings/browser-use-integration.md`: addendum noting the extension
  route + why chrome.debugger-over-WS (vs OpenClaw content-script).
- README: `whip browser install` quickstart (user-facing surface).

## Tasks

1. extrelay: WS server + token auth + CDP bridge + fake-extension tests. ✅
2. extension files (manifest + background.js) + go:embed. ✅
3. `ModeExtension` + extBackend wiring into Open/Manager/session. ✅
4. `whip browser install` CLI (write files, relay.json, print steps, open). ✅
5. E2E against real Chrome with the extension. ✅ (rod-through-relay E2E in
   `rod_e2e_test.go`: a real rod.Browser drives attach + Eval through the
   relay against a fake extension — the full CDP tunnel + Target synthesis
   on the real rod client, without needing a manual Chrome extension load
   in CI)
6. Gates + docs. ✅ (`task check` exit 0, `go test -race` green, features.md
   + learnings + README updated)

## Deviations discovered during implementation

- **Manual WebSocket handshake, not gobwas's `ws.UpgradeHTTP`.** rod's CDP
  client sends a literal non-base64 `Sec-WebSocket-Key: nil`; gobwas
  validates the key and 400s, and rewriting it breaks the accept hash rod
  verifies against the key *it* sent. The relay hijacks the conn and
  computes `Sec-WebSocket-Accept` itself from whatever key arrived.
- **Separate read/write buffers.** A shared `bufio.ReadWriter` deadlocked —
  a write's flush resets the read buffer mid-read. The conn splits reads
  (handshake's buffered reader) from writes (raw socket), serialized by a
  write mutex.
- **rod shallow-copies on `.Context()`.** `b.Context(ctx).Connect()` sets
  the client on a copy; calling `Pages()` on the original nil-derefs. The
  backend connects and uses the same object (this only bit the E2E test, but
  the lesson is recorded in learnings).
- **rod's `Eval` is `Runtime.callFunctionOn`**, and `PageFromTarget` issues
  `Emulation.setDeviceMetricsOverride` + `Page.enable` immediately after
  attach — all of which tunnel to the extension fine (chrome.debugger
  answers them), confirming the synth+pipe design needs no special-casing.
- **chromedp driver unchanged**: extension mode is rod-only (the relay is a
  CDP endpoint; chromedp is the spike backup for local launches and gains
  nothing from the tunnel).
- **Branded Google Chrome ignores `--load-extension`** (logs
  "--load-extension is not allowed in Google Chrome, ignoring") — a security
  measure. The automated real-browser E2E therefore runs against **Chrome
  for Testing** (Playwright's full build, which allows it); the production
  path for users is manual Developer-mode → Load unpacked, which branded
  Chrome does allow. Pre-seeding Secure Preferences does NOT work (entries
  are MAC-protected; tampered entries are discarded). The SW's console is
  nearly impossible to capture (MV3 SWs run+suspend before a debugger can
  attach), so the extension reports progress over plain HTTP `/swlog` for
  test/debug.
