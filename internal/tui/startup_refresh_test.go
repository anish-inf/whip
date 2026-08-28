package tui

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/context-labs/whip/internal/config"
)

// modelsServer serves GET /models with the given ids and counts requests.
// whip's own Provider.ResolveKey takes over for api.inference.net base URLs
// (machine-key fallback), so test providers must use the httptest URL.
func modelsServer(t *testing.T, hits *atomic.Int32, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[`)
		for i, id := range ids {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%q,"context_length":1000000}`, id)
		}
		fmt.Fprintf(w, `]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// (a) The reported scenario: default model kimi-k3 has no config entry and
// the catalog cache is missing; startup resolution force-refreshes the
// provider's /models and resolves it on the retry instead of aborting.
func TestBuildAgentWithRefreshRecoversFromMissingCatalog(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir()) // no models.json at all
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "kimi-k3",
		DefaultProvider: "inference-net",
		Providers: map[string]config.Provider{
			"inference-net": {BaseURL: srv.URL, API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{},
	}

	ag, mn, pn, err := buildAgentWithRefresh(cfg, "", "", "")
	if err != nil {
		t.Fatalf("refresh-and-retry should recover the launch: %v", err)
	}
	if mn != "kimi-k3" || pn != "inference-net" {
		t.Errorf("route: %s@%s", mn, pn)
	}
	if ag == nil || ag.Model != "kimi-k3" {
		t.Errorf("agent should run the catalog id, got %+v", ag)
	}
	if ag.ContextLimit != 1000000 {
		t.Errorf("context limit should come from the refreshed catalog, got %d", ag.ContextLimit)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh fetch, got %d", n)
	}
	// The refresh persisted the catalog, so a later plain resolve hits the cache.
	if _, _, _, err := cfg.Resolve("kimi-k3", ""); err != nil {
		t.Errorf("catalog should be cached after the refresh: %v", err)
	}
}

// (b) Happy path: the first resolve succeeds, so no network fetch happens.
func TestBuildAgentWithRefreshSkipsFetchWhenResolveSucceeds(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "glm-5.2-fast",
		DefaultProvider: "inference-net",
		Providers: map[string]config.Provider{
			"inference-net": {BaseURL: srv.URL, API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{
			"glm-5.2-fast": {Providers: []string{"inference-net"}},
		},
	}

	if _, _, _, err := buildAgentWithRefresh(cfg, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("successful resolve must not fetch, got %d requests", n)
	}
}

// (c) A genuinely unknown model still errors after exactly one refresh
// attempt, and the ORIGINAL "unknown model" error is what surfaces.
func TestBuildAgentWithRefreshStillErrorsForUnknownModel(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "nope",
		DefaultProvider: "inference-net",
		Providers: map[string]config.Provider{
			"inference-net": {BaseURL: srv.URL, API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{},
	}

	_, _, _, err := buildAgentWithRefresh(cfg, "", "", "")
	_, ok := errors.AsType[*config.UnknownModelError](err)
	if !ok {
		t.Fatalf("persistent miss should surface the original typed error, got %T (%v)", err, err)
	}
	if !strings.Contains(err.Error(), `unknown model "nope"`) {
		t.Errorf("original message lost: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh attempt, got %d", n)
	}
}

// The headless wrapper (whip run / whip acp) gets the same retry-on-miss.
func TestResolveWithRefreshRecoversFromMissingCatalog(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	var hits atomic.Int32
	srv := modelsServer(t, &hits, "kimi-k3")
	cfg := &config.Config{
		DefaultModel:    "kimi-k3",
		DefaultProvider: "inference-net",
		Providers: map[string]config.Provider{
			"inference-net": {BaseURL: srv.URL, API: "openai-completions", APIKey: "k"},
		},
		Models: map[string]config.Model{},
	}

	prov, mdl, id, err := ResolveWithRefresh(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if id != "kimi-k3" || prov.BaseURL != srv.URL {
		t.Errorf("route: id=%q base=%q", id, prov.BaseURL)
	}
	if mdl.Context != 1000000 {
		t.Errorf("synthetic model should carry catalog context, got %+v", mdl)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("want exactly 1 refresh fetch, got %d", n)
	}
}
