# Clickable links (OSC 8 hyperlinks) in the transcript

Branch: `feat/clickable-links`

## What this does

Makes URLs and locally-accessible file paths in the whip transcript
clickable: terminal-level OSC 8 hyperlinks (cmd/ctrl-click opens browser /
editor / file manager, per terminal config). Pure render-layer; zero new
dependencies (`github.com/charmbracelet/x/ansi.SetHyperlink` is already
imported).

- `[label](https://…)` in assistant markdown → label underlined, clickable
  (href no longer shown twice).
- Bare `https://…` autolinks in assistant markdown → clickable, shown once.
- Markdown links to existing local files (`[x](./foo.md)`) → `file://` URI
  resolved against the process CWD at render time.
- Bare `path/to/file.go[:N]` in user messages and assistant text → clickable
  `file://` link, but only when the file exists on disk. Click lands in the
  terminal's default handler for `file://` (usually `$EDITOR` or the file
  manager); no custom click-to-open-code in v1.

## Goal

A human reading the transcript can click any URL or real file reference
without select-copy-paste. Assistant blocks, user blocks, resume, streaming
flush, and resize all render identically.

## Non-goals

- Custom mouse hit-testing / our own click handling (the terminal owns OSC 8
  clicks; v1 adds nothing to the TUI mouse layer).
- Underlining arbitrary styled `blockText` blocks (tool call lines, status
  lines) — only assistant markdown and user input lines.
- Opening files at a line number / in an editor of our choosing.
- Changing what the model sees: this is display-only.

## Prior art

- opencode renders clickable links via opentui: `Link` component with
  `onMouseUp → open(href)` — `~/code/coding-harnesses/opencode/packages/tui/src/ui/link.tsx:15`.
  Only used in auth dialogs, not the chat transcript. We go one better:
  terminal-native OSC 8, works in the transcript with zero mouse plumbing.
- glamour v1.0.0 renders link text + appended href, both with `Underline:
  true` (`styles/styles.go:221-227`), no OSC 8 (`ansi/link.go`). So the
  current transcript shows `label https://url`, doubly underlined, not
  clickable.
- `charmbracelet/x/ansi.SetHyperlink(uri)` emits `ESC ] 8 ; ; URI BEL` —
  width-aware: `StringWidth`/`Strip`/`Wrap`/`Hardwrap` all handle OSC 8
  (verified: wrapped hyperlink keeps sequences intact, width counts only the
  label).

## Design

All in `internal/tui` (one new file, small edits to `markdown.go` and
`tui.go`).

### New file: `links.go`

Pure-ish core, I/O (file existence) at the edge, process-CWD-relative:

```go
// linkifyFilePaths rewrites mentions of existing local files into OSC 8
// file:// hyperlinks. s may contain SGR styling; only already-ANSI text is
// passed in (user lines), or raw markdown BEFORE glamour (assistant text).
func linkifyFilePaths(s string, exists func(string) bool) string

var fileRefRE = regexp.MustCompile(
    `(?:\.{0,2}/)?[A-Za-z0-9_@+~-][\w@+~.-]*(?:/[\w@+~.-]+)+\.[A-Za-z0-9]{1,10}(?::\d+)?`)
// requires ≥1 '/' and a dotted extension; trailing :N line ref kept inside
// the link; matches preceded by ']' or '(' (markdown link internals) skipped
// via lookbehind-free check of the preceding byte.
```

`exists` defaults to a tiny wrapper over `os.Stat` (not-a-dir). Absolute
paths resolved as-is; relative against `os.Getwd()` (whip runs in the
project root; sessions resume with the same CWD contract).

### Assistant blocks (`markdown.go` renderMarkdown)

1. Pre-pass `linkifyFilePaths` on the raw markdown — safe because it only
   fires when the file exists and skips matches already inside
   `[text](target)`.
2. Post-pass `hyperlinkGlamourLinks(rendered)` on the glamour output:

```go
// linkStyleRE matches a run of text wrapped in glamour's link SGR (4;36m on
// dark, 4;33m light — underlined cyan/yellow). Each contiguous run is one
// rendered link: label part + optional " href" part (glamour appends the
// destination after a space for [label](url) links).
```

For each run:
- if it ends with ` http(s)://X` (or `mailto:`): label = run minus the
  href suffix; emit `OSC8(X)+label+OSC8()`. If label == href (autolink),
  emit once.
- if the whole run is an existing local path: wrap with
  `OSC8(file://abs)+run+OSC8()`.
- otherwise (anchor-only `#x`, mailto text, non-file relative targets):
  leave as-is.

The OSC 8 wrap goes *inside* the existing underline SGR so styling is
untouched; width/Hardwrap verified safe.

Width independence: style prefixes are per-theme constants, so the regex
works at any width; a width change re-renders and re-linkifies (renderAt
already invalidates).

### User blocks (`tui.go`)

Three sites build `youStyle.Render("❯ ") + text` blocks: submit echo
(tui.go:2257), resume replay (tui.go:527), steer echo (tui.go:1192). Route
the text portion through `linkifyFilePaths` before styling (plain text in →
OSC-8-bearing plain text out; the block stays `blockText`, wrap-safe).

### Streaming

`textMsg` flushes complete lines mid-turn into assistant blocks; the partial
tail renders through the same path on flush (`flushCurrent`). A link split
across the flush boundary can render unlinked mid-stream, then correctly on
the next flush — same tradeoff as mid-stream markdown formatting today. No
special handling.

### Terminal support / degradation

Unsupported terminals (screen/tmux without passthrough config) show the
label with its existing underline and ignore OSC 8 — no breakage, no escape
soup (verified shape: sequences are self-contained `ESC]8;;…BEL … ESC]8;;BEL`).
Copy/selection uses `ansi.Strip`, which drops OSC 8 — unchanged.

## Test plan (`links_test.go`, extend `markdown_test.go`)

stdlib `testing`, no new deps.

- `linkifyFilePaths`: exists-injection table — relative, `./`, absolute,
  `:N` line refs, path at line start/end, path inside `[text](…)` untouched,
  nonexistent path untouched, no-extension `foo/bar` untouched.
- `hyperlinkGlamourLinks`: glamour-render a fixture with `[label](https://)`,
  bare autolink, file link, anchor `#x`; assert OSC 8 present with correct
  URI, href not doubled, `ansi.Strip` output unchanged, `ansi.StringWidth`
  matches pre-linkify width.
- Headless model test: submit `look at internal/tui/tui.go` (real file),
  assert rendered user block contains `]8;;file://`.
- Resume path: `tui.go:527` goes through the same helper — covered by the
  same unit test on the helper plus one replay assertion.
- `task check` (gofmt -s, vet, tests). No new concurrency → `-race` via the
  normal suite run.

## Docs plan

- `docs/features.md`: new "Clickable links" section (behavior →
  `internal/tui/links.go`, `markdown.go` → `links_test.go`).
- `docs/roadmap.md`: no entry exists; none to check.
- `docs/learnings/other-harnesses/opencode/opencode-ux.md`: one line noting
  opencode's Link component + that whip does transcript-wide OSC 8 instead.

## Deviations from the original sketch

- **File-path linkification moved post-render.** The sketch injected OSC 8
  into the raw markdown before glamour; glamour treats the sequences as
  wrappable text and splits them mid-sequence. `linkifyRenderedFilePaths`
  runs on the rendered output instead, where refs are contiguous word atoms.
- **`hyperlinkGlamourLinks` is a byte scanner, not regexes.** Glamour inserts
  a newline mid-atom when the wrap falls inside a link (breaking the
  label/href pair across lines), which one regex can't express. The scanner
  walks link atoms (`38;5;35;1`/`30;4` dark, `29;1`/`36;4` light) and
  tolerates the wrap-split separator.
- **File-ref shape gate is in code, not the regex.** Go's regexp `{2,10}`
  under-matches (accepts "go"); the extension-length rule lives in
  `isFileRef`. Bare filenames with an extension are allowed through to the
  existence check (tui.go is a real ref when the file exists).
- **glamour normalizes `./x` destinations to `/x`** — `targetURI` tries the
  dot-relative reading first.

## Tasks

1. [x] `links.go`: `fileRefRE`, `linkifyFilePaths`, existence wrapper.
2. [x] `hyperlinkGlamourLinks` + wire into `renderMarkdown`.
3. [x] User-block wiring (submit, resume replay, steer).
4. [x] Unit + headless tests above; `task check` green.
5. [x] `docs/features.md` section; opencode-ux.md note; close plan.
