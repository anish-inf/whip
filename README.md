# loopy

A minimal coding agent harness in Go. Interactive bubbletea session, an LLM
tool-use loop (bash / read / write / edit), and provider-routable models.

## Install

```sh
task install   # → ~/go/bin/loopy
```

## Run

```sh
task run                 # run locally from source
task run -- -m glm-5.2-fast          # pass flags after --
loopy                    # installed binary, default model
loopy -m kimi-k3-fast -p inference   # pick model AND provider
```

`task --list` shows the rest (build, test, fmt, vet, tidy).

In-session: `/model <name> [provider]`, `/clear`, `/help`, `/quit`, ctrl+c interrupts.

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
    "kimi-k3-fast": { "providers": ["inference"], "maxTokens": 131072 }
  }
}
```

Any OpenAI-compatible endpoint works as a provider. Key resolution:
`apiKeyEnv` env var → `apiKey` literal → for api.inference.net, the key stored
in `~/.inf/config.json` by the `inf` CLI.
