# chromedp spike — head-to-head against rod behind Backend

**Branch:** `chromedp-spike` (off `browser-use-exploration`)

## What this does

Implements `internal/browser`'s `Backend` contract on chromedp (as
`chromedpBackend` in `internal/browser/chromedp/`) and runs the existing
E2E suite against both drivers, producing real comparison data to confirm
or reverse the rod choice.

## Goal

A decision table, not vibes: for each Backend method (Navigate, Eval,
ClickAt, TypeText, PressKey, Fill, Scroll, WaitLoad, WaitElement,
Screenshot, AXTree, BoxModel, Tabs, UseTab, UploadFiles, Close) — works
identically on both drivers Y/N, latency, LOC cost, quirks hit.

## Non-goals

- No change to the tool layer, session manager, safety floor, or TUI —
  the seam is `Backend`; that's what it's for.
- No launcher replacement in v1 of the spike: chromedp's attach-only path
  covers live mode; dedicated/headless launch via our existing
  `launcher` (rod) or a 100-line exec-based launcher if we want zero rod
  imports.
- playwright-go stays disqualified (Node driver subprocess — verified in
  its run.go).

## Design

- `internal/browser/backend_chromedp.go` (build-tagged or plain; choose at
  spike time): `chromedpBackend` implementing `Backend` using
  `chromedp.NewContext` against a CDP endpoint from the existing
  `DiscoverLiveWS` / launcher URL. Reuse `attach.go`, `safety.go`,
  `session.go` untouched.
- An env knob `WHIP_BROWSER_DRIVER=rod|chromedp` selects the driver in
  `Open` for A/B runs.
- Test harness: `DRIVER=chromedp go test ./internal/browser/ -run TestE2E`
  — the same three mode tests run twice.

## Known landmines to probe deliberately (from the rod build)

1. Eval semantics: chromedp's `Evaluate` vs our raw `Runtime.evaluate`
   (rod's `page.Eval` body-wrap quirk forced us raw — check chromedp's
   defaults).
2. The closed-connection-after-nav failure the other session hit (bug
   under active investigation on rod — note whether chromedp shows it;
   that's decision-relevant signal, not just a test).
3. `Page.enable`/`Runtime.evaluate` settle race (see below).
4. Dialog handling: chromedp's event listener model vs rod's HandleDialog.
5. Screenshot sizing: chromedp `CaptureScreenshot` has no clip-scale —
   cost of implementing the 1568px ladder.

## Test plan

- Existing: `internal/browser` unit + E2E ×3 modes, run with
  `WHIP_BROWSER_DRIVER=chromedp`.
- New: `driver_parity_test.go` — table-driven, runs each Backend op
  against both drivers on one page, asserts equivalent outcomes.
- Gate: `task check` + `-race` on both drivers.

## Timebox & revisit

Half a day of code + runs. Revisit decision when the parity table exists;
switch triggers from the decision doc apply (WS-layer bugs, stale protos,
archival).
