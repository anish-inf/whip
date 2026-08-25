# loopy

A minimal coding agent harness in Go. Interactive bubbletea session, an LLM
tool-use loop (bash / read / write / edit), and provider-routable models.

## Install

Requires Go ≥ 1.27. Install straight from the repo in one command:

```sh
go install github.com/context-labs/loopy/cmd/loopy@latest
```

or, if you prefer curl-pipe-to-sh:

```sh
curl -fsSL https://raw.githubusercontent.com/context-labs/loopy/main/scripts/install.sh | sh
```

Both drop `loopy` into `~/go/bin`. From a cloned repo, `task install` does the same with the version stamped from git.

## Setup (with inference.net)

loopy defaults to inference.net models; the `inf` CLI provisions the key:

```sh
git clone https://github.com/context-labs/loopy && cd loopy
task install                        # builds + installs loopy (version stamped from git)

bun add -g @inference/cli           # the inf CLI
inf auth login                      # log in
inf team switch                     # pick your team
inf project switch                  # pick your project
inf claude on && inf claude off     # mints the API token loopy reads from ~/.inf/config.json
```

Then `loopy` and you're in. First things to try: `/context-doctor` (audit
what a fresh session injects, in tokens), `/goal <text>` (work until done),
drop a `.mcp.json` in the repo (MCP servers just appear — `/mcp` to see them).

## Run

```sh
task run                 # run locally from source
task run -- -m glm-5.2-fast          # pass flags after --
loopy                    # installed binary, default model
loopy -m kimi-k3-fast -p inference   # pick model AND provider
```

`task --list` shows the rest (build, test, fmt, vet, tidy).

In-session: `/model <name> [provider]`, `/tasks` (background subagents), `/clear`, `/help`, `/quit`. ctrl+c once interrupts; ctrl+c twice quits (and kills any agent-spawned child processes).

The `task` tool runs tool calls in **parallel** (per-path file-mutation locks keep edits to the same file serial) and supports `background: true` to launch a subagent that works concurrently and reports back when done.

See [docs/features.md](docs/features.md) for the full feature map and [docs/concurrency.md](docs/concurrency.md) for the channel design.

## Config — `~/.loopy/config.json`

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

Any OpenAI-compatible endpoint works as a provider. Key resolution:
`apiKeyEnv` env var → `apiKey` literal → for api.inference.net, the key stored
in `~/.inf/config.json` by the `inf` CLI.

## MCP

loopy connects to MCP servers and their tools appear in the agent as
`mcp__<server>__<tool>`. Three config styles all work — loopy reads your
existing setup:

- **claude-style**: a `.mcp.json` in the project root (`{"mcpServers": {...}}`)
- **codex-style**: `[mcp_servers.*]` tables in `~/.codex/config.toml`
- **loopy-native**: an `"mcp"` block in `~/.loopy/config.json` (wins on
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
each server's tools automatically. CLI: `loopy mcp list|add|remove`, and
`loopy mcp test <name>` to doctor one server (status, timing, tool names,
stderr tail; non-zero exit — validate a `.mcp.json` in CI). `loopy mcp
serve` runs loopy's own tools (read/bash/edit/write) as an MCP server for
other harnesses.

## Browser — drive your real, logged-in Chrome

`browser_exec` can drive your everyday browser (real cookies/sessions) four
ways via `browser.mode` in `~/.loopy/config.json`: `live` (attach to a
running Chrome with debugging on), `dedicated`/`headless` (a loopy-owned
Chrome, auto-launched as a fallback when nothing debuggable is running), and
`extension` — the only one that works on Chrome ≥ 136's default profile,
where direct CDP is blocked.

Extension mode uses a tiny unpacked Chrome extension: loopy runs a local
relay, the extension pipes raw CDP through `chrome.debugger` on the tab you
pin. Set it up once:

```
loopy browser install
```

That writes the extension to `~/.loopy/browser/extension/`, mints the relay
token, and opens `chrome://extensions` + the folder. Then three clicks
(Chrome forbids programmatic install): **Developer mode on → Load unpacked →
select the folder**. Set `"browser": { "mode": "extension" }` in
`~/.loopy/config.json`, open the tab you want, and click the loopy extension
icon (a green ● appears) to let loopy drive it; click again to detach. While
pinned, Chrome shows a "loopy is debugging this browser" bar — that's the
mechanism doing the work.
