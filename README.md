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
