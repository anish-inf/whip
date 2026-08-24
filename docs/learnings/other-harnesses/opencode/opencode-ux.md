# opencode UX catalog

Source: `/home/abe/code/coding-harnesses/opencode`. Framing note: this opencode is no
longer Go/bubbletea — `packages/tui` is TypeScript + SolidJS on `@opentui/core` /
`@opentui/keymap`, and the architecture is client/server (TUI is a thin client over a
local HTTP server + SDK). The ideas port; the code does not. Paths below are relative to
the repo root. The curated to-do view of all this lives in [../../../roadmap.md](../../../roadmap.md).

## 1. TUI / interaction

### Keybind + command registry (the single best idea to steal)
- `packages/tui/src/config/keybind.ts` — one flat table of ~180 named actions, each
  `keybind(defaultKeys, description)`. From that single table it derives the JSON schema
  for user overrides, the description map, and a `CommandMap` from keybind name → dotted
  command id (`session_compact` → `session.compact`).
- Keybinds, command palette, help text, which-key, and config schema are all generated
  from one declaration. Add an action once, it appears everywhere. In Go: a
  `map[string]struct{Default, Desc string}` plus a `Command` registry keyed by the same names.
- Multi-key defaults in one string (`"ctrl+c,ctrl+d,<leader>q"`); `"none"` unbinds.
- Binding values can be objects: `input_paste: {key: "ctrl+v", preventDefault: false}` —
  observe a key without swallowing it (native paste still fires).
- Unknown keybind names in user config are a **hard error listing them** (`keybind.ts`
  449–464), not a silent no-op.

### Leader key + which-key
- Leader `ctrl+x`, rebindable, `leader_timeout` 2000ms (`registerTimedLeader`,
  `keymap.tsx`). Escape clears a pending sequence; backspace pops one token.
- `feature-plugins/system/which-key.tsx` (608 lines) — full which-key panel with
  dock/overlay layout and pending-sequence preview. Overkill early; the pending-hint
  line is cheap and high-value.
- Key aliases both ways: users write `enter`/`esc`/`pgup`, hints display naturally.

### Mode stack
- `keymap.tsx` `createOpencodeModeStack` — push/pop stack of modes (`base`, `modal`,
  `autocomplete`, `question`). Dialogs push on mount, pop on cleanup; bindings declare
  their mode. Kills the "escape does the wrong thing" bug class.

### Command palette
- `component/command-palette.tsx` — `ctrl+p`, 79 lines, a projection over the keymap
  registry filtered to reachable commands. Shows title, description, category, and the
  current keybind (the palette teaches the shortcuts). `suggested: true` entries pin to
  a "Suggested" category when the filter is empty.

### One fuzzy-select widget for every picker
- `ui/dialog-select.tsx` (791 lines): fuzzysort filter, category headers, per-row
  description/details/footer/gutter, `current` auto-scroll, mouse hover/click with
  keyboard/mouse input-mode tracking, and `actions` — extra keybound verbs on the
  highlighted row (ctrl+f favorite, ctrl+d delete). Model/session/theme/timeline pickers
  are each ~40-line configs of it.
- `ui/dialog.tsx` — dialog *stack* (`push`/`replace`/`clear`, size presets); escape pops
  one level and is suppressed while a text selection is active.
- Two-step destructive confirm without a modal: ctrl+d turns the row red, "Press ctrl+d
  again to confirm" (`dialog-stash.tsx`, `dialog-session-list.tsx`).

### Input editor (`keybind.ts` 161–200)
- Distinct **logical vs visual line** home/end (matters once prompts wrap), each with
  shift-select variants; word motions in both emacs and CUA styles.
- `input_newline: "shift+return,ctrl+return,alt+return,ctrl+j"` — four bindings because
  terminals disagree about which they can send.
- Undo/redo inside the input (`ctrl+-`, `super+z`).
- `registerManagedTextareaLayer` scopes the whole input keymap to focused-textarea only.

### Paste handling
`component/prompt/index.tsx` (~1149–1250, 1396–1425), `prompt/part.ts`,
`prompt/local-attachment.ts`:
- Paste ≥3 lines or >150 chars collapses into a `[Pasted ~N lines]` extmark placeholder;
  real text expands on submit; copying out re-expands.
- Pasted `file://` URLs / quoted paths become attachments (read the file); clipboard
  images become file parts; SVG inlines; CRLF/ConPTY normalization; empty bracketed
  paste on old Windows Terminal falls back to the clipboard image reader.

### External editor
- `editor.ts` `openEditor()` — temp `.md`, `renderer.suspend()`, `$VISUAL || $EDITOR`,
  read back, resume. ~40 lines. After edit, mention extmarks are re-anchored by
  searching for their virtual text; deleted mentions drop their attachments.
- `editor-zed.ts` reads Zed's SQLite DB for buffer+selection; `~/.claude/ide/*.lock`
  discovery for other IDEs. The selection is injected as ambient context (see
  [at-mentions.md](at-mentions.md)).

### Scrolling & mouse
- `util/scroll.ts` (28 lines): flat `scroll_speed` or macOS-style acceleration.
- Message-level navigation: next/prev message, jump to last user message, on
  `ctrl+alt+*` to avoid input collisions.
- `"mouse": false` config disables capture so native terminal selection works
  (`app.tsx` ~205). Mouse used meaningfully: click tool row to expand error, click
  reasoning header to expand, clicks suppressed while text is selected.

### Status bar / spinners / toasts
- Footer: cwd left; right side shows pending-permission count (warning color), LSP/MCP
  counts with status dots, `/status` hint; alternates with a "Get started /connect"
  nudge when no provider (`routes/session/footer.tsx`).
- Subagent footer: position "2 of 5", token count with **% of context window**, cost in
  USD, parent/sibling navigation keys (`subagent-footer.tsx`).
- Spinner is a gradient sweep across the label text itself ("Thinking: …" shimmers) with
  live elapsed duration and running cost (`ui/spinner.ts`). All animation killable via a
  persisted toggle.
- `ui/toast.tsx` (102 lines): single-slot top-right toast, 4 theme-colored variants, 5s,
  `toast.error(err)` convenience. Every command's success/failure lands here.

### Diff rendering
- Split vs unified auto-chosen by terminal width (>120 → split), `diff_style` override.
  Fine-grained theme keys: per-side backgrounds, per-side line-number backgrounds, sign
  colors (`routes/session/permission.tsx` ~37–85).
- Full diff viewer route (`feature-plugins/system/diff-viewer.tsx`, 1077 lines): three
  sources (working tree / vs main / last turn), file tree pane, hunk/file navigation,
  split toggle, prefs persisted to KV. Modest version worth copying: `diff_open` on the
  working tree with `]`/`[` hunk jumps.

### Markdown / highlighting
- Streaming-aware markdown (`<markdown streaming={true}>`) so partial output doesn't
  flicker; grid tables; 18 markdown + 9 syntax theme keys.
- **Conceal** (`<leader>h`): hides markdown syntax noise while keeping styling — vim
  conceallevel for chat. Reasoning blocks render with a desaturated syntax palette and a
  `thinkingOpacity` theme knob.

### Tool-call display (`routes/session/index.tsx` ~1708–2600)
- Per-tool icon + one-line collapsed form: `$ npm test`, `→ Read src/main.go`,
  `✱ Grep "handler"`, `% <url>`, `◈ web search`. While pending: `~ <gerund>` — concrete
  verbs per tool ("Writing command…", "Preparing edit…"), not generic "running".
- Denied tools render **strikethrough**; failed tools turn red and click-to-expand the
  error (suppressed while text is selected).
- `util/collapse-tool-output.ts` — 19 lines: truncate to N lines and M chars, whichever
  first, return overflow flag.
- `setPreLayoutSiblingMargin` — blank line between rows only when the neighbor is
  multi-line: dense single-line stacking, breathing room around blocks.

### Permission prompts (`routes/session/permission.tsx`, 719 lines)
- Per-type rendering (edit → scrollable diff, bash → `$ cmd`, glob/grep → pattern,
  webfetch → URL, task → subagent + description, doom_loop → explanation).
- **Allow once / Allow always / Reject**; "always" is a second screen showing exactly
  which patterns it installs and their scope; "reject" in a subagent opens a free-text
  "Tell OpenCode what to do differently" box sent back to the model. Escape = reject.
  `ctrl+f` fullscreen for big diffs.

### Attention
- `attention.ts`: desktop notifications **only when the terminal is blurred** (focus
  tracking) + sounds per event (`question`, `permission`, `error`, `done`,
  `subagent_done`); swappable sound packs. Off by default. "when: blurred" is the detail
  that makes it not-annoying.
- Terminal title updating (toggleable), proper `ctrl+z` suspend (renderer suspend +
  SIGTSTP + SIGCONT resume), SIGHUP cleanup.

## 2. Session UX

- **Share links**: `/share` → consent dialog once (`share_consent` in KV), URL to
  clipboard, title flips to "Copy share link"; `/unshare` revokes.
- **Clickable links**: a `Link` component (`packages/tui/src/ui/link.tsx`) opens
  `href` via `open()` on `onMouseUp` — but it's only used in auth dialogs, never
  in the chat transcript. loopy instead emits terminal-native OSC 8 hyperlinks
  transcript-wide (URLs + existing local files), so every link is clickable with
  no per-widget mouse plumbing (`internal/tui/links.go`).
- **Rename** (ctrl+r), **fork** from timeline or from any message's action menu (fork
  carries the message's text + file parts into the new session's prompt).
- **Timeline** (`<leader>g`, `dialog-timeline.tsx`): user messages newest-first;
  `onMove` live-scrolls the transcript as you browse — picker doubles as a scrubber.
- **Undo/redo** (`<leader>u`/`r`, index.tsx ~616–690): undo aborts the running turn,
  git-reverts file changes, deletes the message, and **restores the prompt text + file
  parts into the input** for edit-and-resend. `util/revert-diff.ts` shows per-file +/-.
- **Compact** (`/compact`, alias `/summarize`), toast-guards on no model.
- **Subagents**: parent/child/sibling navigation, `ctrl+b` detaches a synchronous
  subagent to the background so you get your prompt back.
- **Pins**: ctrl+f pins; `<leader>1`–`9` jump; pinned sessions always survive filtering.
- **Session list**: 150ms-debounced server-side search, optimistic delete, two-press
  confirm, directory-scope filter.
- **Export/copy**: `/export` → checkbox dialog (thinking? tool details? filename? open
  without saving) → markdown → `$EDITOR`; `/copy` → clipboard.
- **Queued messages**: prompts submitted while busy render inline with a `QUEUED` badge;
  `<leader>q` edits/removes queued items (`runtime.queue.ts` is the well-commented
  serial-queue reference). Interrupt = **triple escape within 5s** (counter resets).
- **Prompt stash**: git-stash for prompts — push/pop/list, 50 entries, JSONL
  (`prompt/stash.tsx`).
- **Prompt history**: JSONL, 50 entries, dedup, stores *structured* prompts (text +
  parts + mode); up/down only navigate at cursor offset 0 (`prompt/history.tsx`).
- **Draft retention**: drafts ≥20 chars survive session switches; agent/model/variant
  re-derived from the session's last user message on switch.

## 3. Input niceties

- **`@` mentions**: fuzzy server-side file search, extmark tokens, structured
  `FilePart`s, `@file#10-40` line ranges, frecency ranking
  (`frequency / (1 + age_days)`, JSONL, 1000 cap). Also `@agent`, MCP resources, and
  configured reference aliases. Expansion strategy: see [at-mentions.md](at-mentions.md).
- **`!` shell mode**: only at cursor offset 0; border/label/placeholder change; escape
  or backspace-at-0 exits; output lands as a tool result the model can see.
- **`/` slash commands**: generated from the same command registry (`slashName` +
  `slashAliases`); multi-line input keeps line 1 as command+args, rest appended.
- **Custom commands**: markdown/config templates with `$ARGUMENTS`/`$1..$n`, optional
  pinned agent/model, `subtask: true` runs in a subagent; MCP prompts and skills fold
  into the same namespace. Built-ins `/init`, `/review`.

## 4. Agents / permissions

- Agents replace build/plan modes: `{model, variant, system, description, mode:
  subagent|primary|all, hidden, color, steps, permissions}`. Tab cycles primary agents;
  `@name` invokes subagent-only ones.
- **Model variants**: ctrl+t cycles reasoning-effort variants; picking a model
  auto-opens the variant picker when needed.
- **Permission rules** `{action, resource(glob), effect: allow|deny|ask}`, per-agent.
- **`permission/arity.ts`** — steal outright: a table of command-prefix arities
  (`git`→2, `npm run`→3, flags never count) so "allow always" on
  `git checkout some-branch` installs a rule for `git checkout`, not the exact argv.

## 5. Config / theming

- Two config files by concern: `tui.json(c)` presentation vs `opencode.json` runtime;
  published `$schema` so editors autocomplete; keybinds merge over defaults.
- **34 JSON themes**; format is `defs` (named colors) + values that are hex, a def ref,
  or a `{dark, light}` pair — one file serves both modes. Layered precedence:
  defaults < plugins < user files < generated "system" theme built from the terminal's
  OSC-queried palette (prewarmed to avoid a flash). `selectedForeground()` computes
  readable selection contrast by luminance for transparent terminals.
- **KV prefs** (`context/kv.tsx`): file-locked atomic `kv.json`; every palette toggle
  persists (animations, diff wrap, tips, conceal, tool details, pinned sessions,
  share consent…). For loopy: one `settings(key, value)` table in sessions.db.
- `/status` dialog: MCP/LSP/formatters/plugins with per-item status colors.
- Rotating home-screen tips, auto-hidden after first session.

## 6. CLI surface

Entry `packages/opencode/src/index.ts` (yargs). Worth copying:

- **`run`** (`cli/cmd/run.ts`): `-c/--continue`, `-s/--session`, `--fork`, `--command`,
  `-m provider/model`, `--agent`, `--variant`, `-f/--file` (repeatable), `--title`,
  `--share`, `--format default|json` (raw event stream = machine-readable transcript),
  `--auto` ("dangerous!"), `--dir`, `--attach <url>`. **Merges piped stdin with the
  positional message.** Non-interactive output reuses the TUI's icon/verb vocabulary.
- **`serve` / `attach <url>`** — HTTP server + full TUI against a remote server;
  `--replay` re-renders history on resume and after terminal resize.
- **`--mini`** — a third UI: scrollback-writing interactive mode that doesn't take over
  the screen (`cli/cmd/run/` — scrollback writer, footer menu/permission/question,
  `runtime.queue.ts`).
- **`service start|stop|restart|status`** — background daemon management.
- **`api <operationId | METHOD path>`** — curl your own running server by OpenAPI
  operation id. Great debugging affordance once an HTTP server exists.
- `session list --format json` / `session delete`, `export`/`import`, `models`,
  `providers`, `upgrade`/`uninstall`, `completion` (one yargs line).
- Global flags: `--print-logs`, `--log-level`, `--pure` (no external plugins — great for
  bug reports). Env markers for children: `AGENT=1`, `OPENCODE=1`, `OPENCODE_PID`.

## Cheapest-first shortlist for loopy

1. Keybind/command single registry → palette, help, slash commands, config schema.
2. One DialogSelect-equivalent fuzzy widget; every picker becomes ~40 lines.
3. Toasts + KV prefs table.
4. Paste summarization + pasted-path-becomes-attachment.
5. `@` mentions with `#ranges` + frecency (see at-mentions.md for expansion strategy).
6. `!` shell mode gated on cursor-at-0.
7. `$EDITOR` with suspend/resume.
8. Tool rows: icons + gerund verbs + strikethrough-on-deny + click-to-expand errors;
   `collapseToolOutput` (19 lines).
9. Permission prompt (once/always/reject + arity-scoped always + reject-with-message).
10. Undo that restores the prompt, not just the git state.
11. Theme JSON with defs + {dark,light} variants; hard-error on unknown config keys.
12. `run --format json` + piped stdin; shell completion.

Skip until the pain is real: which-key, sound packs, the 1077-line diff viewer route,
Zed selection discovery, plugins, `--mini`.
