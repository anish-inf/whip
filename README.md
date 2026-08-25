# whip

A minimal coding agent harness in Go. Interactive bubbletea session, an LLM
tool-use loop (bash / read / write / edit), and provider-routable models.

## Why whip

Coding agents are converging on the same shape — a model, a handful of tools,
a loop — but the popular harnesses pile on surface area: plugin marketplaces,
opaque permission systems, config you can't read, runtimes you can't audit.
whip is a bet that the interesting part is small, and that small is a feature:

- **You should be able to read the whole thing.** ~10k lines of Go, one
  binary, no framework magic. The agent loop is a single function
  (`Agent.Turn`). If the harness does something surprising, the answer is a
  file, not a forum post.
- **The tools are the model's hands, not a product.** bash, read, write,
  edit, a subagent runner. The model already knows how to code; the harness's
  job is to get out of the way while keeping the loop honest.
- **Boring where it can be, native where it pays.** SQLite for sessions,
  JSON for config, Markdown for memory and skills — files you can diff, grep,
  and back up. But where Go beats the TypeScript reference harnesses (pi,
  opencode), whip takes the win: parallel tool calls with per-path file locks,
  background subagents, a streaming TUI — all channel primitives the
  reference designs hand-roll with promises
  (see [docs/concurrency.md](docs/concurrency.md)).
- **Provider-agnostic by construction.** Any OpenAI-compatible endpoint is a
  provider; models route to providers and are discovered live from
  `GET /models`. No vendor lock-in, no model registry to update.

## Design decisions

The short list — each links to the doc with the reasoning and the code:

1. **One loop, one function.** `Agent.Turn`: append message → stream → run
   tool calls → repeat. Steered messages inject at loop boundaries, never
   mid-generation. → [docs/agent-loop.md](docs/agent-loop.md)
2. **Channels, not promises.** File-mutation safety is a 1-capacity channel
   per path; background task completion is a single channel close that wakes
   every waiter. → [docs/concurrency.md](docs/concurrency.md)
3. **Context is a budget, not a surprise.** Proactive compaction at a
   configurable % of the window, orphan-safe tail keeping, reactive retry on
   context-limit errors. → [docs/agent-loop.md](docs/agent-loop.md#compaction)
4. **Everything inspectable lives in plain files.** Sessions in
   `~/.whip/sessions.db`, config in `~/.whip/config.json`, model catalog
   cache in `~/.whip/models.json`, memory as Markdown checkboxes.
   → [docs/architecture.md](docs/architecture.md)
5. **Drive the user's real environment.** The browser tool attaches to your
   logged-in Chrome (four modes, including a CDP-extension relay for Chrome
   ≥ 136); computer-use drives your actual Mac desktop.
   → [docs/browser-computer-use.md](docs/browser-computer-use.md)
6. **Interoperate, don't isolate.** whip reads claude-style `.mcp.json` and
   codex-style `config.toml`, and `whip mcp serve` exposes whip's own tools
   as an MCP server for other harnesses. → [docs/features.md](docs/features.md#mcp)

## Install

Prebuilt binaries (Linux/macOS, x64/arm64) from GitHub Releases — checksum-verified:

```sh
curl -fsSL https://raw.githubusercontent.com/context-labs/whip/main/install.sh | sh
```

The script downloads the release asset for your platform, verifies it against the published `SHA256SUMS`, and drops `whip` into the first writable directory on your `PATH`. Pin a version with `WHIP_VERSION=v0.1.0`, force the install dir with `WHIP_BIN_DIR`.

From source instead (requires Go ≥ 1.27; macOS arm64 builds also embed the computer-use Swift helper via `task driver`):

```sh
go install github.com/context-labs/whip/cmd/whip@latest
```

From a cloned repo, `task install` does the same with the version stamped from git.

## Setup (with inference.net)

whip defaults to inference.net models; the `inf` CLI provisions the key:

```sh
git clone https://github.com/context-labs/whip && cd whip
task install                        # builds + installs whip (version stamped from git)

bun add -g @inference/cli           # the inf CLI
inf auth login                      # log in
inf team switch                     # pick your team
inf project switch                  # pick your project
inf claude on && inf claude off     # mints the API token whip reads from ~/.inf/config.json
```

Then `whip` and you're in. First things to try: `/context-doctor` (audit
what a fresh session injects, in tokens), `/goal <text>` (work until done),
drop a `.mcp.json` in the repo (MCP servers just appear — `/mcp` to see them).

## Run

```sh
task run                 # run locally from source
task run -- -m glm-5.2-fast          # pass flags after --
whip                    # installed binary, default model
whip -m kimi-k3-fast -p inference   # pick model AND provider
```

`task --list` shows the rest (build, test, fmt, vet, tidy).

In-session: `/model <name> [provider]`, `/tasks` (background subagents), `/clear`, `/help`, `/quit`. ctrl+c once interrupts; ctrl+c twice quits (and kills any agent-spawned child processes).

The `task` tool runs tool calls in **parallel** (per-path file-mutation locks keep edits to the same file serial) and supports `background: true` to launch a subagent that works concurrently and reports back when done.

See [docs/features.md](docs/features.md) for the full feature map and [docs/concurrency.md](docs/concurrency.md) for the channel design.

## Config — `~/.whip/config.json`

Models are routed to providers: a model lists the providers that serve it, and
you can switch providers without touching the model. Written with defaults for
inference.net on first run:

```json
{
  "defaultModel": "kimi-k3-fast",
  "providers": {
    "inference": {
      "name": "Inference.net",
      "baseUrl": "https://api.inference.net/v1",
      "api": "openai-completions",
      "apiKeyEnv": "INFERENCE_API_KEY"
    }
  },
  "models": {
    "kimi-k3-fast": { "providers": ["inference"], "context": 131072 }
  }
}
```

`context` is the model's **input** window (context limit); it drives the header's
% full and proactive compaction. The provider's `/models` `context_length`
overrides it when advertised. `maxOut` (optional) caps **output** tokens; 0 uses
the provider's `max_completion_tokens`, else `context`. The old `maxTokens` field
still parses (it always meant the context window) but is superseded by `context`.

**Catalog models need no config entry.** whip caches each provider's
`GET /models` (24h TTL in `~/.whip/models.json`), and any advertised model is
usable directly — `whip -m deepseek-v4-pro` or `/model deepseek-v4-pro` — with
context, vision, effort levels, and pricing taken from the catalog. Config
entries are authoritative overrides when present. Newly announced models appear
in the `/model` picker (dim, marked `(new)`) after `/model refresh` or the next
TTL cycle. If several providers advertise the same id, pass a provider
(`-p` / `/model <name> <provider>`) to disambiguate.

Any OpenAI-compatible endpoint works as a provider. Key resolution:
`apiKeyEnv` env var → `apiKey` literal → for api.inference.net, the key stored
in `~/.inf/config.json` by the `inf` CLI.

## MCP

whip connects to MCP servers and their tools appear in the agent as
`mcp__<server>__<tool>`. Three config styles all work — whip reads your
existing setup:

- **claude-style**: a `.mcp.json` in the project root (`{"mcpServers": {...}}`)
- **codex-style**: `[mcp_servers.*]` tables in `~/.codex/config.toml`
- **whip-native**: an `"mcp"` block in `~/.whip/config.json` (wins on
  name conflicts):

```json
{
  "mcp": {
    "docs": { "command": ["npx", "-y", "@docs/mcp"], "env": { "API_KEY": "$DOCS_KEY" } },
    "web":  { "url": "https://mcp.example.com/mcp", "headers": { "Authorization": "Bearer $TOKEN" } }
  }
}
```

`/context-doctor` audits what a fresh session injects (skills, MCP tool schemas,
server instructions, built-in tool schemas) with per-source token estimates —
useful when arriving from a heavier harness.

Servers connect in the background at startup and lazily on first use — a
slow or broken server never blocks the loop (calls fail fast with an
actionable message, and dropped sessions auto-reconnect with backoff).
`/mcp` shows live status; `/mcp <name> reconnect|enable|disable` manages
servers without restarting. Server instructions teach the model how to use
each server's tools automatically. CLI: `whip mcp list|add|remove|import`
(`import [--dry-run]` copies imported servers into whip's own config), and
`whip mcp test <name>` to doctor one server (status, timing, tool names,
stderr tail; non-zero exit — validate a `.mcp.json` in CI). `whip mcp
serve` runs whip's own tools (read/bash/edit/write) as an MCP server for
other harnesses.

## Browser — drive your real, logged-in Chrome

`browser_exec` can drive your everyday browser (real cookies/sessions) four
ways via `browser.mode` in `~/.whip/config.json`: `live` (attach to a
running Chrome with debugging on), `dedicated`/`headless` (a whip-owned
Chrome, auto-launched as a fallback when nothing debuggable is running), and
`extension` — the only one that works on Chrome ≥ 136's default profile,
where direct CDP is blocked.

Extension mode uses a tiny unpacked Chrome extension: whip runs a local
relay, the extension pipes raw CDP through `chrome.debugger` on the tab you
pin. Set it up once:

```
whip browser install
```

That writes the extension to `~/.whip/browser/extension/`, mints the relay
token, and opens `chrome://extensions` + the folder. Then three clicks
(Chrome forbids programmatic install): **Developer mode on → Load unpacked →
select the folder**. Set `"browser": { "mode": "extension" }` in
`~/.whip/config.json`, open the tab you want, and click the whip extension
icon (a green ● appears) to let whip drive it; click again to detach. While
pinned, Chrome shows a "whip is debugging this browser" bar — that's the
mechanism doing the work.

Gate the claude/codex imports with the `"mcpImport"` block — useful when
another app writes MCP entries into `~/.codex/config.toml` you don't want
(blocked servers stay visible in `/mcp` and `mcp list` instead of silently
loading):

```json
{
  "mcpImport": {
    "codex": { "enabled": true, "exclude": ["node_repl"] }
  }
}
```

Per source: `enabled` kills the whole source, `only` is a name allowlist,
`exclude` a denylist (wins over `only`). No block = import everything.

## Docs

How it works, from the top down:

- [docs/architecture.md](docs/architecture.md) — the moving parts and how a
  keystroke becomes a tool call: TUI, agent loop, LLM client, tools, MCP,
  storage. Start here.
- [docs/agent-loop.md](docs/agent-loop.md) — `Agent.Turn` in detail: the
  stream-tools-repeat cycle, parallel tool execution, compaction, steering.
- [docs/concurrency.md](docs/concurrency.md) — the two channel patterns
  behind parallel tool calls (per-path locks) and background subagents.
- [docs/tools.md](docs/tools.md) — the tool set the model gets: bash, file
  tools, subagents, browser, computer-use, and how schemas are defined.
- [docs/models-providers.md](docs/models-providers.md) — provider routing,
  live model discovery, token/cost bookkeeping.
- [docs/browser-computer-use.md](docs/browser-computer-use.md) — driving your
  real Chrome (live / dedicated / headless / extension modes) and your Mac
  desktop.
- [docs/features.md](docs/features.md) — the full feature map, each section
  linked to code and tests.
- [docs/roadmap.md](docs/roadmap.md) — what's shipped vs. what's next,
  cross-referenced to the harnesses that inspired each item.
- [docs/learnings/](docs/learnings/) — exploration reports from other
  harnesses (pi, opencode, exo) that informed the design.
