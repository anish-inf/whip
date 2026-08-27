package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRenamesInferenceProvider(t *testing.T) {
	cfg := &Config{
		DefaultProvider: "inference",
		CompactProvider: "inference",
		Providers: map[string]Provider{
			"inference": {Name: "Inference.net", BaseURL: InferenceNetBaseURL, API: "openai-completions"},
		},
		Models: map[string]Model{
			"kimi-k3": {Providers: []string{"inference", "openrouter"}},
		},
	}
	cfg.normalize()

	if _, ok := cfg.Providers["inference"]; ok {
		t.Error("legacy \"inference\" provider not removed")
	}
	if _, ok := cfg.Providers[InferenceNetProvider]; !ok {
		t.Fatal("provider not renamed to inference-net")
	}
	if got := cfg.Models["kimi-k3"].Providers; got[0] != "inference-net" || got[1] != "openrouter" {
		t.Errorf("model route not migrated: %v", got)
	}
	if cfg.DefaultProvider != "inference-net" || cfg.CompactProvider != "inference-net" {
		t.Errorf("default/compact provider not migrated: %+v", cfg)
	}
}

func TestNormalizeNoLegacyIsNoop(t *testing.T) {
	cfg := Default()
	before := len(cfg.Providers)
	cfg.normalize()
	if len(cfg.Providers) != before {
		t.Errorf("normalize changed a fresh Default: %v", cfg.Providers)
	}
	if _, ok := cfg.Providers[InferenceNetProvider]; !ok {
		t.Error("Default should already use inference-net")
	}
}

func TestUpsertInferenceNetModes(t *testing.T) {
	cfg := &Config{}
	cfg.UpsertInferenceNet("inf-literal", false)
	p := cfg.Providers[InferenceNetProvider]
	if p.APIKey != "inf-literal" || p.APIKeyEnv != "" {
		t.Errorf("literal mode: %+v", p)
	}
	cfg.UpsertInferenceNet("", true)
	p = cfg.Providers[InferenceNetProvider]
	if p.APIKeyEnv != InferenceNetEnvVar || p.APIKey != "" {
		t.Errorf("env mode: %+v", p)
	}
	// Machine-key login: no key material on the entry.
	cfg.UpsertInferenceNet("", false)
	p = cfg.Providers[InferenceNetProvider]
	if p.APIKey != "" || p.APIKeyEnv != "" {
		t.Errorf("machine-key mode should leave key fields empty: %+v", p)
	}
}

func TestProviderKeyFallsBackToStoredMachineKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WHIP_HOME", home)
	keyFile := filepath.Join(home, "inference-net.json")
	if err := os.WriteFile(keyFile, []byte(`{"machineKey":"mk-stored"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Provider{BaseURL: InferenceNetBaseURL}
	if got := p.Key(); got != "mk-stored" {
		t.Errorf("machine-key fallback: got %q", got)
	}
	// An explicit env var wins over the stored key.
	t.Setenv(InferenceNetEnvVar, "env-key")
	p.APIKeyEnv = InferenceNetEnvVar
	if got := p.Key(); got != "env-key" {
		t.Errorf("env should win: got %q", got)
	}
}
