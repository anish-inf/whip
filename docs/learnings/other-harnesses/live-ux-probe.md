# Live UX probe: claude-code, codex, grok, opencode, pi (and loopy)

Method: launched each harness in a PTY (`/tmp/harness-probe/probe2.py`), answered
its terminal queries (OSC 11 background, cursor-position), drove it with
keystrokes (trust prompt → `/` menu → filter), captured the stripped screen.
Versions: claude 2.1.239, codex-cli 0.147.0 (broken install on this machine —
missing `@openai/codex-linux-x64`), grok 1.0.4, opencode 1.18.19, pi 0.84.2,
loopy @ mcp-support.

## What each does at first paint

**claude-code**: trust gate → a bordered "welcome back" card (tips, what's-new,
release notes, plan/org line, cwd), rotating placeholder in the input (`Try "how
do I log an error?"`), status footer with permission mode + effort + `← for
agents`. On a crashy prior launch it *tells you* it fell back to the classic
renderer and how to make fullscreen stick (`/tui default`). When the account hit
a spend limit mid-turn: inline notice, "continuing automatically at 2am · esc to
cancel", and it keeps the UI alive instead of dying.

**opencode**: ASCII logo, input with rotating example placeholder, `Build · Big
Pickle` model line, `tab agents · ctrl+p commands` hints, a *rotating tip* in
the footer ("Run /connect to add an AI provider"), cwd + version bottom-right.
`/` opens a live-filtered command list with right-aligned descriptions and
`esc/enter` hints; ctrl+p is the fuller action palette.

**pi**: the most information-dense startup. Footer teaches keys immediately
(`escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o
more`). Startup prints **loaded resources**: skills found, extensions loaded,
and — the standout — **[Skill conflicts]** with the exact problem
(`description exceeds 1024 characters (1089)`), plus a model-availability
warning with doc paths. `ctrl+o` expands full startup help. `/` autocomplete is
grouped with pagination `(1/29)`.

**grok**: could not probe — it hangs on this machine (sends OSC 11 + mode
enables, then waits for something our PTY never satisfies; 148 bytes total
output, no UI). That itself is a lesson: *a harness that blocks first paint on
a terminal query it never times out is bricked in any non-mainstream terminal.*

**codex**: install is broken here; from prior exploration (roadmap notes +
source at ~/code/coding-harnesses): quiet startup, `/goal`-style persistence,
queued steers.

**loopy (ours, for contrast)**: trust gate → one header line
(`loopy · model @ provider · cwd · 0% ctx ⚡ medium`) → `/` opens a clean
completion list. Solid, but *silent*: no tips, no "what's loaded", no next-step
guidance.

## Concrete UX gaps in loopy, ranked by value/line

1. **Startup resource report (pi's [Skills]/[Extensions]/[Skill conflicts]).**
   loopy already scans skills and MCP servers at startup — but says nothing.
   One block at first paint: `skills: 47 loaded · mcp: docs ✓ (4 tools), ghost ✗
   (see /mcp)` plus **validation warnings** (pi surfaces a skill whose
   description is too long! we have `maxDesc = 300` silently truncating — the
   user never learns their skill is broken). This is "observability at rest"
   and we already have all the data.

2. **Rotating input placeholder (claude/opencode).** `Try "…"` examples that
   teach features (mentions, $skills, /goal, MCP). Ours is static/blank. A
   `[]string` of examples + rotate on idle tick; zero deps.

3. **First-run "next steps" card (claude).** On a brand-new `~/.loopy`, show 3
   lines: set a key, try /goal, drop a .mcp.json. Disappears once config exists.
   We currently drop users into a header and silence.

4. **Degraded-mode honesty (claude's renderer fallback notice, spend-limit
   notice).** loopy has silent degradation: skills truncated at maxDesc, MCP
   discovery errors (we append one errStyle line — good), catalog fetch
   failures (silent). The rule from claude: *every fallback names itself and
   its remedy*.

5. **Keybind hint footer at idle (pi).** One line under the input cycling
   2-3 high-value hints (`esc interrupt · ctrl+p commands · @ file · $ skill`).
   We teach keys only in `/help` and palette hints — invisible until sought.

6. **`/` completion with right-aligned descriptions + pagination (pi).** Ours
   shows name + short text already; pi's is grouped and paged `(1/11)` — ours
   actually shows `(1/11)` too. On par; the gap is *grouping* (pi separates
   settings/actions/extensions).

7. **Never block first paint on a terminal query (grok's failure).** Our
   startup does OSC 11 + cursor-position queries — add a timeout so a
   non-answering terminal gets defaults, not a hang. (Ours didn't hang here,
   but grok's corpse is the warning.)

## Not worth copying

- claude's bordered welcome card with logo/what's-new/release-notes — heavy,
  marketing-flavored; the *information* (tips) matters, the box doesn't.
- opencode's ASCII-art logo — one paint of charm, then permanent noise.
- claude's auto-permission-mode banner complexity — ours is simpler (trust gate).

## Where loopy already beats them

- Trust gate is clearer than claude's (theirs buries the risk in chattiness).
- MCP failure UX after the polish pass (fail-fast + did-you-mean + first-settle
  notes + auto-reconnect) — none of the probed harnesses showed anything close.
- `/` completion exists at all (claude's is a filter-as-you-type; similar).
