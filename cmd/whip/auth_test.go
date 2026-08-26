package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/context-labs/whip/internal/config"
)

// fakeOpenRouter serves GET /models: 200 with a two-model list for the good
// key, 401 for anything else — mirroring OpenRouter's auth behavior.
func fakeOpenRouter(t *testing.T, goodKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "openai/gpt-5", "context_length": 400000, "input_modalities": []string{"text", "image"},
					"pricing": map[string]string{"prompt": "0.00000125", "completion": "0.00001"}},
				{"id": "anthropic/claude-sonnet-4.5", "context_length": 1000000, "input_modalities": []string{"text"}},
			},
		})
	}))
}

func TestAuthOpenRouterGoodKey(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	srv := fakeOpenRouter(t, "sk-or-good")
	defer srv.Close()

	if err := authOpenRouter(srv.URL, "sk-or-good", false); err != nil {
		t.Fatalf("auth failed: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Providers["openrouter"]
	if !ok {
		t.Fatal("openrouter provider not saved")
	}
	if p.APIKey != "sk-or-good" || p.BaseURL != config.OpenRouterBaseURL {
		t.Errorf("unexpected provider: %+v", p)
	}

	cats := config.LoadCatalogs()
	cat, ok := cats["openrouter"]
	if !ok || len(cat.Models) != 2 {
		t.Fatalf("catalog not prefetched: %+v", cats)
	}
	if got := cat.ContextLength("openai/gpt-5"); got != 400000 {
		t.Errorf("context length not carried into catalog: %d", got)
	}
	if in, _, _, ok := cat.Pricing("openai/gpt-5"); !ok || in == 0 {
		t.Errorf("pricing not carried into catalog: %v %v", in, ok)
	}
	if vis, found := cat.SupportsVision("openai/gpt-5"); !found || !vis {
		t.Errorf("vision modality not carried into catalog: %v %v", vis, found)
	}

	// The prefetched catalog makes catalog-only models resolvable with no
	// config entry — the "access all openrouter models easily" promise.
	_, m, _, err := cfg.Resolve("anthropic/claude-sonnet-4.5", "")
	if err != nil {
		t.Fatalf("catalog model should resolve: %v", err)
	}
	if len(m.Providers) != 1 || m.Providers[0] != "openrouter" {
		t.Errorf("catalog model should route to openrouter: %+v", m)
	}
}

func TestAuthOpenRouterBadKeyWritesNothing(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	srv := fakeOpenRouter(t, "sk-or-good")
	defer srv.Close()

	err := authOpenRouter(srv.URL, "sk-or-bad", false)
	if err == nil {
		t.Fatal("expected rejection for a bad key")
	}

	cfg, lerr := config.Load()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Error("a rejected key must not leave a provider entry behind")
	}
	if cats := config.LoadCatalogs(); len(cats) != 0 {
		t.Errorf("a rejected key must not write the catalog: %+v", cats)
	}
}

func TestAuthOpenRouterReauthKeepsOtherState(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	srv := fakeOpenRouter(t, "sk-or-new")
	defer srv.Close()

	cfg, _ := config.Load() // first run: default inference.net config
	cfg.UpsertOpenRouter("sk-or-old", false)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	if err := authOpenRouter(srv.URL, "sk-or-new", false); err != nil {
		t.Fatalf("re-auth failed: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.Providers["openrouter"].APIKey != "sk-or-new" {
		t.Error("re-auth should replace the key")
	}
	if _, ok := cfg.Providers["inference"]; !ok {
		t.Error("re-auth clobbered the default provider")
	}
	if len(cfg.Models) == 0 {
		t.Error("re-auth clobbered the model routes")
	}
}

func TestAuthCLIDispatch(t *testing.T) {
	if err := authCLI(nil); err == nil {
		t.Error("bare `whip auth` should print usage")
	}
	if err := authCLI([]string{"anthropic", "sk-x"}); err == nil {
		t.Error("unknown provider should be rejected")
	}
	// openrouter with no key anywhere errors cleanly (no prompt in tests:
	// stdin isn't a terminal, so the piped read hits EOF).
	t.Setenv("WHIP_HOME", t.TempDir())
	t.Setenv(config.OpenRouterEnvVar, "")
	if err := authCLI([]string{"openrouter"}); err == nil {
		t.Error("openrouter with no key should error, not hang or write config")
	}
}
func TestShellRC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	type tc struct{ shell, want string }
	for _, c := range []tc{
		{"/bin/zsh", home + "/.zshrc"},
		{"/usr/bin/bash", home + "/.bashrc"},
		{"/bin/fish", ""}, // unsupported shell: no rc target
		{"", ""},
	} {
		t.Setenv("SHELL", c.shell)
		if got := shellRC(); got != c.want {
			t.Errorf("SHELL=%q: got %q, want %q", c.shell, got, c.want)
		}
	}
}
