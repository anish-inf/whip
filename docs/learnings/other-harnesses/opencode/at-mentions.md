# How opencode handles `@` file mentions

Source: `/home/abe/code/coding-harnesses/opencode` (TypeScript/SolidJS TUI, client/server).

## The core finding

opencode reads mentioned files eagerly — but it does **not** cat them into the user
message. It rewrites each mention into a **synthetic `Read` tool call/result pair**,
as if the model had read the file itself.

`packages/opencode/src/session/prompt.ts` ~786–970, for a `file:` URL part:

```
text (synthetic): Called the Read tool with the following input: {"filePath":"/abs/path","offset":40,"limit":41}
text (synthetic): <actual output of the real Read tool>
```

Details:

- It invokes the **real `read` tool** (`registry.named()` → `read.execute`) — same line
  numbering, truncation, and output format the model sees from its own reads. No second
  file reader.
- `extra: { bypassCwdCheck: true }` — `@` mentions escape the project-root guard that
  normal tool reads enforce; the user typing the path is the consent. Out-of-tree access
  is otherwise governed by the `external_directory` permission.
- Directories → `Read` on the dir yields a listing (~909–946).
- Binary/image → synthetic `Called the Read tool…` line plus a real file part with a
  base64 data URL (~948–970).
- Line ranges `@src/main.go#40-80` become `offset`/`limit`. If `start == end`, it queries
  **LSP `documentSymbol`** and expands the single line to the enclosing symbol's range
  (~838–855) — `@file.go#120` gives the whole function at line 120.
- Read failures become synthetic text the model can act on: `Read tool failed to read X
  with the following error: …`.

## Why this beats both alternatives

- **vs. cat-ing into the user turn:** the model won't re-read the file (it already sees a
  Read result in history), and compaction/caching treat it like any other tool result
  instead of a giant user message.
- **vs. mention-only (a bare pointer):** no wasted round-trip where the model asks to
  read the thing the user just pointed at.

## The mention-only pattern exists too — for ambient context

`formatEditorContext` (`packages/tui/src/component/prompt/index.tsx` ~120–140) injects
what's open in the user's IDE as a system-reminder note ("The user opened the file X.
This may or may not be relevant…"), with only the selected text inlined.

The distinction worth copying: **explicit mention → eager read framed as a tool call;
ambient/implicit signal → mention-only reminder.**

## loopy's decision

Abe's design for loopy is the **pointer** shape: a note in the user message that a file
was tagged (any path, relative or absolute), letting the model probe it with its own
read/bash/grep instead of receiving the content eagerly. Rationale: catting is wasteful —
the model usually wants to interrogate the file, not ingest it whole.

opencode's counterpoint above (eager read framed as a synthetic tool call) is the
researched alternative: it prevents the model re-reading what it was just handed and
avoids one round-trip. Revisit if pointer-style mentions measurably waste turns. The
mechanics below (parts on the message, path normalization, `#range` parsing, reference
storage) apply to either shape.

## Concrete shape for loopy (if the synthetic-read alternative is ever adopted)

Keep `[]Part` on the user message, not just a string. Parse `@…` at submit into
`{Path, Start, End}`; before the API call expand each into two synthetic messages:

```go
args, _ := json.Marshal(readArgs{FilePath: abs, Offset: start, Limit: n})
msgs = append(msgs,
    llm.Message{Role: "user", Content: "Called the read tool with the following input: " + string(args)},
    llm.Message{Role: "user", Content: readTool.Run(abs, start, n)}, // the real read tool
)
```

- Use the actual `read` tool with its normal numbering/truncation.
- Skip the root-check for mentions only; resolve relative against cwd, keep absolute
  as-is (cf. `normalizeMentionPath`, `autocomplete.tsx` ~285–297: `filepath.Rel`, keep
  absolute if the relative form escapes with `..`).
- Store the mention as a *reference* (path + range) in the session, expand at
  request-build time — that's what lets undo put attachments back in the prompt box.
- `#start-end` → offset/limit is ~5 lines. Defer the LSP symbol expansion until LSP
  exists anyway.

Skipped for now: mention-only mode for `@`, LSP symbol-range expansion, MCP-resource
mentions. Add mention-only mode only if large-file mentions measurably waste context —
the synthetic-Read framing already prevents re-reads, which is the actual cost.
