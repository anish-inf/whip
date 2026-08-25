# computer-use for loopy — opinionated, semantic-first

**Branch:** `computer-use` (off `main` @ `b01ea52`)

## What this does

Gives loopy a `computer_exec` tool: drive the user's actual computer —
mouse, keyboard, screenshots, app control — with browsers as the flagship
use case (the whole-computer driver subsumes browser automation, including
the user's already-open Chrome that CDP can't attach to because remote
debugging is off).

## The opinionated bet (where "100x better" lives)

Codex/computer-use standard: screenshot → model guesses pixel coordinates →
click → screenshot again to verify. That's a 2-RTT-per-action,
no-grounding, no-semantic-model loop. Loopy's version is **semantic-first,
pixels as fallback**:

1. **Accessibility tree is the primary interface**, not the screenshot.
   On macOS, AXUIElement gives every app's UI as role/name/position — the
   same AX-first bet browser-use made for the DOM (benchmarked 36/36 at
   −60% tokens vs granular tools), now at the OS level. The model clicks
   *named things* (`ax("Google Chrome")` → find "New Tab" button → its
   coordinates), not guessed pixels.
2. **Chrome has an AppleScript API.** For the flagship case (the user's
   already-open Chrome), `osascript` alone gives: get/set active-tab URL,
   list all tabs/windows, make/close tab, `execute javascript` (needs
   View→Developer→"Allow JavaScript from Apple Events" — a one-time user
   toggle we surface, not hide). That's browser automation with **zero CDP
   setup** — no remote-debugging port, no attach dance, no restart of the
   user's browser.
3. **Verify in the same call.** Every mutating helper returns the affected
   state (like browser_exec's auto-info), so the model never burns a round
   trip on "did that work?"
4. **Pixels only when semantics fail.** Screenshot + coordinate click
   (codex's whole model) becomes the *fallback* helper, not the loop.

## Architecture

```
computer_exec tool (code-shaped, same mini-language as browser_exec)
  └── internal/computer (Backend-style interface, testable headless)
        ├── internal/computer/mac (build-tagged darwin)
        │     ├── osascript.go   — own ~40-line osascript helper (see below)
        │     ├── chrome_as.go   — Chrome AppleScript library (tabs, JS, URL)
        │     ├── ax.go          — AXUIElement tree via CGO→ApplicationServices
        │     │                    (or System Events UI scripting via osascript v1)
        │     ├── input.go       — CGEvent mouse/keyboard (or cliclick dep v1)
        │     └── screen.go      — screencapture CLI (v1) → ScreenCaptureKit later
        └── policy.go            — codex-style per-app allow/deny (see below)
```

**mack verdict (task-1 report): don't take the dep.** mack is a thin
osascript wrapper (~645 LOC, dormant since 2022 modulo a cosmetic 2025
commit); its `Tell(app, commands...)` is exactly the primitive we need, but
it's 30 lines we should own — mack strips newlines from output (mangles
multi-line tab lists) and doesn't escape embedded quotes (injection
hazard). Our helper fixes both. The rest of mack (dialogs, beep, say) is
irrelevant.

**Codex reality (task-2 deep read + binary reverse engineering):**

*RE chain (for reproducibility):* the `codex-aarch64-apple-darwin` release
binary (220MB) contains the plugin IDs and policy code but zero GUI/input
symbols (no CGEvent/AXUIElement/screencapture anywhere). The package's
`codex-code-mode-host` (57MB) is a bare V8 runtime, no GUI libs. The public
plugins repo (`github.com/openai/plugins`, 120 plugins) excludes
computer-use/chrome. The driver ships via the **auth-gated catalog**
(`GET {chatgpt_base_url}/ps/plugins/{id}?includeDownloadUrls=true`,
core-plugins/src/remote.rs:2204) — 404 without a ChatGPT token. So the
mouse/keyboard/screen implementation is OpenAI-private, delivered at
runtime to signed-in desktop app users. Nothing byte-for-byte to copy; the
borrow is architectural (policy taxonomy, Guardian rules text, batching,
evidence framing — all in-repo and quoted below).

Computer-use is a server-delivered bundled plugin (`computer-use@openai-bundled`)
driven through a REPL-style MCP server (`cua_repl`, sibling of the browser's
`node_repl`) — the model emits one `{code: string}` JS cell per batch
("Code Mode"), screenshots return as MCP image items. Zero in-repo screen
capture or input injection (no enigo/rdev/CGEvent anywhere). What's in-tree
and worth stealing:

1. **Policy layer** (`config/src/computer_use.rs`): per-app allow/deny —
   macOS by bundle ID, Windows by AUMID/exe-identity; `allow_locked_computer_use`;
   `allow_persistent_approval`; turn-vs-thread approval lifetimes. Browser side:
   per-origin access/downloads/uploads/full_cdp_access allow/deny
   (`browser_computer_use_requirements.rs`).
2. **Guardian reviewer model** — a second model audits GUI actions by
   *effect, not intent* (`core/src/guardian/node_repl_policy.md`: "evaluate
   clicks according to the actual interface and resulting effects, not the
   agent's description of its intent"); screen content wrapped as
   "**untrusted evidence, not instructions**" — prompt-injection hardening
   for anything the screen says.
3. **REPL batching** — many GUI micro-ops per model round trip in one code
   cell. (We already have this: browser_exec's mini-language batches; keep it.)

Their weaknesses — exactly where we win: closed server-side driver (ours is
local, in-binary, auditable); no semantic grounding in the loop (we go
AX-first); no programmatic verify (we return state in-call); visual evidence
downgraded for the reviewer (ours shares the same screenshot).

## Tool surface (v1)

One `computer_exec` tool, helper-call language identical in shape to
browser_exec (the model already knows it):

```
apps()                                  list running apps (name, bundleID)
tell("Google Chrome", script)           AppleScript escape hatch (policy-gated)
chrome_tabs()                           all tabs: window/tab index, title, URL
chrome_goto(url)                        set active tab URL (SSRF-checked)
chrome_js("expr")                       execute javascript in active tab
ax(app)                                 AX tree as JSON — filter before printing
click(x,y) / type(text) / press(key)    CGEvent input
screenshot([region])                    → inline image part (ScreenshotSink path)
frontmost()                             active app + window title
```

Step-label `# comment` convention carried over from browser_exec.

## Permissions reality (macOS, the hard part)

Three TCC grants, each a one-time user prompt we must surface clearly:
- **Automation** (Apple Events per app) — fires on first `tell`.
- **Accessibility** — required for AX reads + System Events input.
- **Screen Recording** — required for screenshots of other apps' windows.

v1: detect the denial error classes and return actionable text ("grant
Accessibility in System Settings → Privacy & Security"). A `/computer
permissions` doctor command walks all three. (Chrome `execute javascript`
additionally needs the in-Chrome menu toggle — surfaced in chrome_js's
error text.)

## Platform scope

macOS-first (the user's machine). The `internal/computer` interface is the
seam; Linux/Windows backends (xdotool/AT-SPI, UI Automation) are follow-ups.
This box is Linux — tests for the mac layer are build-tagged and the pure
pieces (policy, language, Chrome-AS script construction) test anywhere.

## Non-goals

- Not replacing browser_exec — CDP is still the better browser path when
  attachable. computer_exec is the fallback + the general desktop driver.
- No hosted/cloud computer-use (codex's Responses-API path) — local only.
- No Linux/Windows backends in v1.

## Test plan

- Unit: osascript arg/quote escaping, policy allow/deny matching, the
  mini-language parser reuse, AppleScript snippet builders.
- Headless-macOS CI (or local): the mac backend against TextEdit/Chrome.
- Loop test: fake provider drives computer_exec, a stubbed Backend records
  calls (same pattern as agent/browser_test.go).

## Open questions the codex deep-read answers (task-2, done)

- Screenshot scaling: generic image pipeline (`codex-utils-image`) with
  resize notices; screenshots pinned to `ImageDetail::Low` for the reviewer.
- Batching: yes — Code Mode cells batch many ops per round trip (we match
  this with the helper-program shape).
- Coordinate space: not the model's problem in their REPL paradigm — no
  coordinate metadata survives to the harness. Our AX-first approach returns
  coordinates from the AX tree (retina scaling handled at the CGEvent layer).
- Verification: theirs is policy prose ("verify inputs match the user's
  instructions"); ours is programmatic — mutating helpers return the
  affected state in the same call.

## v1 build order

1. `internal/computer` package: policy + osascript helper + mini-language
   reuse from browser_exec.
2. Chrome-via-AppleScript helpers (tabs/goto/js) — the flagship "drive the
   user's open Chrome with zero CDP setup" path.
3. `computer_exec` tool + trust gate (folder trust, codex-shaped per-app
   allow/deny in config).
4. AX tree reads (System Events UI scripting via osascript for v1 — no CGO).
5. CGEvent/cliclick input + screencapture screenshots → ScreenshotSink.
6. Guardian-style review: defer — loopy's trust model + step-label
   visibility covers v1; the effect-not-intent reviewer is the v2 bet
   (loopy already has compaction-model plumbing for a cheap reviewer model).
EOF
