# OpenRouter auth: `whip auth openrouter` + `/auth openrouter`

Branch: main (working tree)

## What this does

One command takes a user from an OpenRouter API key to a fully wired
provider: key validated live against OpenRouter, `openrouter` provider
upserted into `~/.whip/config.json`, and the model catalog pre-fetched into
`~/.whip/models.json` so `/model` immediately lists every OpenRouter model
(catalog-model resolution means **no per-model config entries are needed**).

## Goal

- `whip auth openrouter [key]` — CLI: masked prompt (or arg / `OPENROUTER_API_KEY`
  env passthrough for scripting) → validate → save → prefetch catalog.
- `/auth openrouter` — same flow inside the TUI (key entered via the normal
  input box, masked), then a live catalog refresh.
- Docs: how to use an OpenRouter key with whip (docs/models-providers.md,
  features.md, /help).

## Non-goals

- OAuth / PKCE browser flow (OpenRouter supports it; overkill vs. paste-a-key
  for v1).
- Per-model config generation for the ~300 OpenRouter models (catalog
  resolution already covers it — that's the whole point of catalogs).
- Generic `whip auth <any-provider>` framework (ponytail: generalize when a
  second provider wants an auth command).
- Anthropic-native API flavor.

## Design

OpenRouter is OpenAI-compatible (`https://openrouter.ai/api/v1` +
`Authorization: Bearer`), and `llm.Client.Models` already parses its GET
/models shape (`context_length`, `pricing`, `input_modalities` — see
internal/llm/openai.go). So the entire feature is key capture + config
upsert + catalog prefetch.

Key storage decision: whip has no `"$VAR"` value resolution yet (roadmap
unchecked), so `apiKeyEnv` requires the env var to exist at runtime. Flow:

1. `--env` flag (or interactive prompt): write `apiKeyEnv:
   OPENROUTER_API_KEY` and offer to append `export OPENROUTER_API_KEY=…` to
   ~/.zshrc / ~/.bashrc.
2. Default: literal `apiKey` in config.json (0600 perms, like every other
   harness's auth store; same as a literal today). Masked entry.

### Files

- `internal/config/openrouter.go` (new): `UpsertOpenRouter(cfg, key, useEnv)`,
  `OpenRouterBaseURL` const. Pure-ish config manipulation, unit-testable.
- `cmd/whip/auth.go` (new): `authCLI(args)` subcommand, dispatched from
  main.go like `mcp`/`browser`. Validate via `llm.New(base, key).Models(ctx)`,
  then config load → upsert → Save → catalog prefetch via the same
  `llm.Client.Models` → `config.SaveCatalogs`.
- `internal/tui/auth_cmd.go` (new): `/auth openrouter [key]` in the
  `command()` switch; bare form swaps the input box into a masked
  one-shot prompt (textarea EchoMode=EchoPassword) that on submit completes
  the flow instead of starting a turn. Kick `m.fetchCatalogs(true)` after.
- Tests: `internal/config/openrouter_test.go`, `cmd/whip/auth_test.go`
  (httptest fake OpenRouter), `internal/tui/auth_cmd_test.go` (dispatch-level).
- Docs: `docs/models-providers.md` OpenRouter section, `docs/features.md`
  entry, `/help` line, docs/README.md config mention if needed.

### Validation UX

- 401 → "key rejected by OpenRouter", exit 1, config untouched.
- Network error → same, config untouched. Never save an unvalidated key.
- Prefetch failure after save is non-fatal (TTL refresh picks it up).

## Prior art

- CatalogModel resolution: internal/config/config.go `resolveFromCatalog`
  (models need no config entry when advertised by a provider catalog).
- inference.net key fallback: config.Provider.Key() `infKey()` — precedent
  for provider-specific key ergonomics.
- opencode `auth login` writes ~/.local/share/opencode/auth.json; we write
  config.json through the guarded Save (atomic + clobber-refusal), which is
  strictly safer.

## Test plan

- httptest server faking GET /models (401 on bad key, 200 with a small list
  on good) → authCLI end-to-end against WHIP_HOME fixture: config has the
  provider, models.json has the catalog.
- Upsert idempotence: second run replaces the key, keeps other
  providers/models.
- TUI: `/auth openrouter sk-or-bad` appends an error block; dispatch-level,
  no network (patch the validator).
- `task check` green; `go test -race ./internal/... ./cmd/...`.

## Tasks

- [x] Explore config/llm/tui surfaces
- [x] config.UpsertOpenRouter + tests
- [x] cmd/whip/auth.go CLI + tests
- [x] /auth TUI command + masked prompt + tests
- [x] docs (models-providers, features, /help, README)
- [x] task check + adversarial pass

## Shipped / deviations from the sketch

- **Key-leak hardening** (found in the adversarial pass, all pinned by
  `TestAuthKeyNeverLeaksIntoHistoryOrQueue`): `/auth <key>` is kept out of
  ↑-recallable input history (idle and busy submit paths); `/auth` added to
  `busyCmd` so an inline key typed mid-turn runs immediately instead of
  queueing as a chat message (which would have sent the key to the model and
  stored it in sessions.db); the esc-esc draft-clear stash is disarmed after
  a masked prompt so cancel never records the key.
- **Masked render** shares the textarea's "┃ " prompt instead of a stray
  newline; `maskedValue` on `namePrompt` (fork.go) does the masking.
- **catalogLites** moved to tui.go and shared with `fetchCatalogs` (deduped
  the identical conversion loop).
- **Test isolation**: catalog seeding + the background `fetchCatalogs(true)`
  are guarded on `m.prog != nil` so driving the command directly in tests
  writes no cache and spawns no network (this was the source of a real
  ~/.whip/models.json pollution bug caught by TestPaletteModelPanelPreviewsLive).
- go.mod: `golang.org/x/term` promoted from indirect to direct (masked CLI
  prompt). One line; no new dependency.
- Pre-existing unrelated race in `TestScheduleFiresWakeup` (reproduces on a
  clean tree, 3 warnings) — not from this work; left for a separate fix.
