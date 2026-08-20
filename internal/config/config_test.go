package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSaveDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg, err := Load() // first run writes defaults
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "kimi-k3-fast" || cfg.Providers["inference"].BaseURL != "https://api.inference.net/v1" {
		t.Fatalf("defaults: %+v", cfg)
	}
	cfg.DefaultModel = "glm-5.2-fast"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load()
	if err != nil || cfg2.DefaultModel != "glm-5.2-fast" {
		t.Fatalf("reload: %+v %v", cfg2, err)
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".loopy"), 0o700)
	os.WriteFile(filepath.Join(home, ".loopy", "config.json"), []byte("{nope"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestProviderKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.inf fallback available
	t.Setenv("LOOPY_TEST_KEY", "from-env")

	if k := (Provider{APIKeyEnv: "LOOPY_TEST_KEY", APIKey: "literal"}).Key(); k != "from-env" {
		t.Fatalf("env should win: %q", k)
	}
	if k := (Provider{APIKeyEnv: "LOOPY_UNSET_VAR", APIKey: "literal"}).Key(); k != "literal" {
		t.Fatalf("literal fallback: %q", k)
	}
	if k := (Provider{BaseURL: "https://other.example.com"}).Key(); k != "" {
		t.Fatalf("no key expected: %q", k)
	}
}

func TestInfKeyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := Provider{BaseURL: "https://api.inference.net/v1"}
	if k := p.Key(); k != "" {
		t.Fatalf("missing ~/.inf should yield empty, got %q", k)
	}
	os.MkdirAll(filepath.Join(home, ".inf"), 0o700)
	os.WriteFile(filepath.Join(home, ".inf", "config.json"), []byte(`{"codingAgentApiKey":"inf_sk_x"}`), 0o600)
	if k := p.Key(); k != "inf_sk_x" {
		t.Fatalf("inf fallback: %q", k)
	}
	os.WriteFile(filepath.Join(home, ".inf", "config.json"), []byte(`{"apiKey":"inf_sk_main"}`), 0o600)
	if k := p.Key(); k != "inf_sk_main" {
		t.Fatalf("apiKey should win: %q", k)
	}
}

func TestResolveRouting(t *testing.T) {
	cfg := &Config{
		DefaultModel: "m1",
		Providers: map[string]Provider{
			"a": {BaseURL: "https://a", API: "openai-completions"},
			"b": {BaseURL: "https://b", API: "openai-completions"},
		},
		Models: map[string]Model{
			"m1": {Providers: []string{"a", "b"}, ID: "vendor/m1"},
		},
	}
	p, _, id, err := cfg.Resolve("", "")
	if err != nil || p.BaseURL != "https://a" || id != "vendor/m1" {
		t.Fatalf("default routing: %v %v %v", p.BaseURL, id, err)
	}
	p, _, _, err = cfg.Resolve("m1", "b")
	if err != nil || p.BaseURL != "https://b" {
		t.Fatalf("provider override: %v %v", p.BaseURL, err)
	}
	if _, _, _, err = cfg.Resolve("nope", ""); err == nil {
		t.Fatal("expected unknown model error")
	}
	if _, _, _, err = cfg.Resolve("m1", "nope"); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestHomeUnavailable(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := Dir(); err == nil {
		t.Fatal("expected Dir error")
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected Load error")
	}
	if err := (&Config{}).Save(); err == nil {
		t.Fatal("expected Save error")
	}
	if k := infKey(); k != "" {
		t.Fatalf("infKey with no HOME: %q", k)
	}
}

func TestInfKeyBadJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".inf"), 0o700)
	os.WriteFile(filepath.Join(home, ".inf", "config.json"), []byte("{bad"), 0o600)
	if k := infKey(); k != "" {
		t.Fatalf("bad json should yield empty key, got %q", k)
	}
}
