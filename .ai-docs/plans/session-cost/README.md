# Session cost reporting in the status line

Branch: session-cost

## What this does

Adds a cumulative session cost (USD) to the TUI status line, computed from
provider-reported token usage and per-token prices advertised by the
provider's `GET /models` endpoint (already fetched at startup).

## Goal

`~/code/whip   kimi-k3-fast (high)   inference   31.1k(20.7k)/360 tok · $0.0134`

Cost formula (per-million-token rates, matching pi's `models.ts:878`
`calculateCost`):

```
cost = (prompt - cached) * inRate + cached * cacheReadRate + completion * outRate
```

If the provider advertises no `input_cache_read` rate, cached tokens are
billed at the full input rate (conservative). If the provider advertises no
pricing at all, the cost segment is hidden entirely.

## Non-goals

- Per-turn cost display (session cumulative only).
- Config overrides for providers that don't report pricing.
- Persisting cost in the session store / resume (usage itself is
  session-scoped and already resets on resume — cost inherits that).
- Cost tracking inside pi-style per-day aggregates (`scripts/cost.ts` analog).

## Pricing source (verified)

inference.net `GET /v1/models` returns per entry (verified by curl,
2026-… this session):

```json
{"id":"claude-haiku-4-5", ...,
 "pricing":{"prompt":"0.000001000000","completion":"0.000005000000",
            "input_cache_read":"0.000000100000"}}
```

Rates are **USD per token**, decimal strings (OpenRouter uses the identical
shape). Parse with `strconv.ParseFloat`.

## Design

### 1. `internal/llm/openai.go` — extend `ModelInfo`

```go
type ModelInfo struct {
    ID                  string   `json:"id"`
    ContextLength       int      `json:"context_length,omitempty"`
    MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
    ReasoningEfforts    []string `json:"reasoning_efforts,omitempty"`
    Pricing             *Pricing `json:"pricing,omitempty"`
}

// Pricing is the provider's per-token USD rates (inference.net / OpenRouter
// shape). Decimal strings; a nil Pricing means the provider doesn't
// advertise prices.
type Pricing struct {
    Prompt         string `json:"prompt"`
    Completion     string `json:"completion"`
    InputCacheRead string `json:"input_cache_read,omitempty"`
}
```

### 2. `internal/config/catalog.go` — carry rates through the cache

Add to `ModelInfoLite` (parsed floats, USD per token, 0 = unadvertised):

```go
InPrice        float64 `json:"inPrice,omitempty"`
OutPrice       float64 `json:"outPrice,omitempty"`
CacheReadPrice float64 `json:"cacheReadPrice,omitempty"`
```

Plus a `Pricing(id string) (in, out, cacheRead float64, ok bool)` accessor
mirroring `ContextLength`/`MaxCompletionTokens`, and a pure
`Cost(u llm.Usage, p Pricing-ish) float64` — put the pure function in
`internal/llm` (it operates on `llm.Usage`) or `internal/config`; decide at
write time, keep it pure and unit-testable:

```go
// SessionCost returns the USD spend for cumulative usage u at the given
// per-token rates. Cached tokens are billed at cacheRead when advertised,
// else at the full input rate.
func SessionCost(u Usage, in, out, cacheRead float64) float64
```

### 3. `internal/tui/tui.go` — wire the catalog into the model and render

- `fetchCatalogs` (tui.go:463-471): copy `mi.Pricing` into
  `ModelInfoLite` (parse strings → floats here; parse failures = 0).
- New helper on `model`: `costFor(u llm.Usage) (float64, bool)` — looks up
  `m.catalogs[m.provName].Pricing(m.agent.Model)`, returns false when no
  pricing advertised.
- `statusView` (tui.go:3016-3020): after the spend string, append
  ` · $%.4f` when ok. Under $0.01 show 4 decimals; ≥ $1 show 2 — one small
  `fmtCost` helper next to `fmtTok`.

Catalog freshness: `fetchCatalogs` already refreshes stale/missing catalogs
in the background at startup and the agent is created before it lands, so
`costFor` must tolerate a missing catalog (false → hidden) and light up on
the next redraw (usageMsg/catalogsMsg both trigger renders already).

## Prior art

- pi `packages/ai/src/models.ts:878` — `calculateCost`: per-million rates,
  `cost = input*in + output*out + cacheRead*cr + cacheWrite*cw`. We have no
  cache-write signal from OpenAI-compatible usage, so that term drops.
- roadmap.md:55 — "Token/cost tracking per session (pi models.json carries
  cost: {input, output, cacheRead, cacheWrite})" → check the box.
- roadmap.md:42 — "Spinner … + cost in status line" — partially satisfied
  (status line only, spinner untouched).

## Test plan

- `internal/llm`: `SessionCost` table tests — cached fully/partially/no
  cacheRead rate/zero usage.
- `internal/llm/openai_test.go`: `ModelInfo` unmarshals a `pricing` block
  (fixture mirroring the verified inference.net JSON above).
- `internal/config/catalog_test.go`: round-trip of new `ModelInfoLite`
  fields; `Pricing` accessor hit/miss.
- `internal/tui/status_test.go`: statusView shows `· $0.0134` with priced
  catalog, omits it when catalog missing or model unpriced. Follow existing
  headless-model test patterns in that file.
- `go test -race ./...` — catalog map is read in statusView; confirm the
  existing catalogsMsg handoff already serializes access (it does: catalogs
  are only mutated in fetchCatalogs before Send, and assigned on the UI
  goroutine in updateCatalogs — verify at write time).

## Docs plan

- `docs/features.md`: extend the status-line/session-usage entry with cost.
- `docs/roadmap.md`: check "Token/cost tracking per session" (line 55);
  line 42 gets cost checked off within its entry.

## Tasks

1. `internal/llm`: `Pricing` struct + `ModelInfo.Pricing` + `SessionCost` + tests.
2. `internal/config`: `ModelInfoLite` price fields + `Pricing` accessor + tests.
3. `internal/tui`: `fetchCatalogs` copies pricing; `costFor` + `fmtCost`;
   `statusView` segment + tests.
4. `task check` (gofmt -s, vet, tests) + `go test -race ./...`.
5. Docs: features.md section, roadmap checkboxes.
