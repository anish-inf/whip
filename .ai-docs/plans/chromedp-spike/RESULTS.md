# chromedp spike — results

**Date:** 2026-02-24 · **Branch:** `chromedp-spike` · **Status:** done — decision below.

## The parity table (real data, `TestDriverParity`, headless warm)

| Backend op | rod | chromedp | notes |
|---|---|---|---|
| Navigate | ✅ 10ms | ✅ 10ms | identical |
| Eval | ✅ 0ms | ✅ 0ms | both raw `Runtime.evaluate` + illegal-return IIFE retry |
| Info | ✅ 0ms | ✅ 0ms | identical |
| AXTree | ✅ | ✅ | identical output shape |
| ClickAt | ✅ 1ms | ✅ 1ms | identical (Input.dispatchMouseEvent) |
| Screenshot | ✅ 37ms | ✅ 30ms | chromedp needs explicit clip-scale (no `Scale` field) — implemented |
| Fill (focus) | ✅ 3ms | ✅ 3ms | same eval+key-event strategy |
| Scroll | ✅ 32ms | ✅ 7ms | chromedp's wheel event is cheaper here |
| WaitElement | ✅ | ✅ | identical poll |
| WaitLoad | ✅ | ✅ | both poll readyState via Eval |
| Tabs / UseTab | ✅ | ✅ | chromedp's `WithTargetID` is cleaner than rod's PageFromTarget |
| UploadFiles | ✅ | ✅ | identical (DOM.setFileInputFiles) |
| Back | ✅ | ✅ | chromedp: explicit navigation-history call; rod: history.back() eval |
| Close | ✅ | ✅* | *chromedp lingers ~150ms after `allocStop()` — handled with a wait |

**E2E ×3 modes, full suite ×3 runs per driver:** rod `ok ok ok` (~2.5s each) · chromedp `ok ok ok` (~3.4s each) after the Close-linger fix.

## LOC cost

- rod backend: 659 lines (`browser.go`) — includes the live-attach glue
- chromedp backend: 480 lines (`chromedp_backend.go`) — ~27% smaller for the
  same surface; the exec allocator + `WithTargetID` carry work rod makes you
  do by hand. The live-attach discovery (`attach.go`) is shared unchanged.

## Quirks hit (the real differentiators)

**rod:**
1. `page.Eval` wraps the snippet as a function *body* — bare expressions
   TypeError. Forced a raw-CDP `Eval` (which we now use everywhere). A
   helper-API footgun that cost real debugging time.
2. `WaitLoad` evals a rAF-loop helper that wedged against some pages/server
   combos during bring-up — we replaced it with a readyState poll.
3. Launcher (process management, leakless guard, profile dir) is rod's big
   win — chromedp's exec allocator covers the same ground but you set it up.

**chromedp:**
1. `chromedp.Flag("remote-debugging-port", 0)` (int) → opaque
   `invalid exec pool flag`; must be a string. Cost one cycle.
2. `Close()` returns before the Chrome process exits → TempDir cleanup
   races (75% failure in the dedicated E2E before the 150ms settle).
3. cdproto's `.Do(ctx)` returns are positional multi-value soup
   (`GetLayoutMetrics` returns 7 values) — verbose but mechanical.

## Decision

**Stay on rod for main; keep chromedp behind `LOOPY_BROWSER_DRIVER=chromedp`.**

Why not switch despite the smaller backend:
- **Full behavioral parity** — no capability gap in either direction; the
  switch would be a churn-only move.
- rod's launcher + `Leakless` process guard is load-bearing for the
  dedicated/headless modes (self-kill on loopy exit); chromedp's allocator
  is equivalent in the spike but less battle-tested *here*.
- The two rod quirks we hit are already worked around in shipped code (raw
  eval, readyState poll) — sunk, tested, race-clean.
- chromedp's wins (cleaner tab switching, 27% fewer LOC, active cdproto
  regeneration) are real but not user-visible today.

**Switch triggers, armed by this spike** (now with a working fallback ready
to promote): rod WS-layer bug that chromedp doesn't show · a CDP method
rod's stale protos lack (cdproto regenerates monthly) · rod archived.
Promoting is `Driver = "chromedp"` default + delete the rod file.

## Artifacts

- `internal/browser/chromedp_backend.go` — the chromedp `Backend`.
- `internal/browser/parity_test.go` — `TestDriverParity` (the table above,
  regenerated on every run).
- `Open` returns `Backend`; `LOOPY_BROWSER_DRIVER=rod|chromedp` selects;
  `Session`/`Do` and the tool layer are driver-agnostic.
EOF
