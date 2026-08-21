// Package config loads and saves loopy's JSON configuration from ~/.loopy.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Provider is an API endpoint that can serve models.
type Provider struct {
	Name      string `json:"name,omitempty"`
	BaseURL   string `json:"baseUrl"`
	API       string `json:"api"`              // "openai-completions" is the only supported value for now
	APIKey    string `json:"apiKey,omitempty"` // literal key, or use apiKeyEnv
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// Key returns the resolved API key for the provider.
func (p Provider) Key() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	if p.APIKey != "" {
		return p.APIKey
	}
	// ponytail: special-case fallback to the inf CLI's stored key; generalize to apiKeyFile if more providers need it
	if strings.Contains(p.BaseURL, "api.inference.net") {
		return infKey()
	}
	return ""
}

// infKey reads apiKey/codingAgentApiKey from ~/.inf/config.json (written by `inf auth set-key`).
func infKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".inf", "config.json"))
	if err != nil {
		return ""
	}
	var c struct {
		APIKey            string `json:"apiKey"`
		CodingAgentAPIKey string `json:"codingAgentApiKey"`
	}
	if json.Unmarshal(data, &c) != nil {
		return ""
	}
	if c.APIKey != "" {
		return c.APIKey
	}
	return c.CodingAgentAPIKey
}

// Model routes a model to one or more providers that serve it.
type Model struct {
	Name      string   `json:"name,omitempty"`
	Providers []string `json:"providers"`    // provider keys, first is the default
	ID        string   `json:"id,omitempty"` // model id sent to the API; defaults to the map key
	MaxTokens int      `json:"maxTokens,omitempty"`
}

// Config is the root of ~/.loopy/config.json.
type Config struct {
	DefaultModel    string              `json:"defaultModel"`
	DefaultProvider string              `json:"defaultProvider,omitempty"` // override the model's first provider
	DefaultEffort   string              `json:"defaultEffort,omitempty"`   // reasoning effort for new sessions: "", "low", "medium", "high"
	Providers       map[string]Provider `json:"providers"`
	Models          map[string]Model    `json:"models"`
}

// Dir returns the loopy home directory (~/.loopy), creating it if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".loopy")
	return dir, os.MkdirAll(dir, 0o700)
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads ~/.loopy/config.json, writing a default config on first run.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		cfg := Default()
		return cfg, cfg.Save()
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &cfg, nil
}

// Save writes the config back to ~/.loopy/config.json.
func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

// Resolve picks the provider and API model id for a model name.
// provider may be "" to use the config default routing.
func (c *Config) Resolve(model, provider string) (Provider, Model, string, error) {
	if model == "" {
		model = c.DefaultModel
	}
	m, ok := c.Models[model]
	if !ok {
		return Provider{}, Model{}, "", fmt.Errorf("unknown model %q (models: %s)", model, keys(c.Models))
	}
	if provider == "" {
		provider = c.DefaultProvider
	}
	if provider == "" && len(m.Providers) > 0 {
		provider = m.Providers[0]
	}
	p, ok := c.Providers[provider]
	if !ok {
		return Provider{}, Model{}, "", fmt.Errorf("unknown provider %q (providers: %s)", provider, keys(c.Providers))
	}
	id := m.ID
	if id == "" {
		id = model
	}
	return p, m, id, nil
}

func keys[V any](m map[string]V) string {
	s := ""
	for k := range m {
		if s != "" {
			s += ", "
		}
		s += k
	}
	return s
}

// Default returns the first-run config, wired for inference.net.
func Default() *Config {
	return &Config{
		DefaultModel: "kimi-k3-fast",
		Providers: map[string]Provider{
			"inference": {
				Name:      "Inference.net",
				BaseURL:   "https://api.inference.net/v1",
				API:       "openai-completions",
				APIKeyEnv: "INFERENCE_API_KEY",
			},
		},
		Models: map[string]Model{
			"kimi-k3-fast":           {Providers: []string{"inference"}, MaxTokens: 131072},
			"glm-5.2-fast":           {Providers: []string{"inference"}, MaxTokens: 128000},
			"deepseek-v4-flash-0731": {Providers: []string{"inference"}, MaxTokens: 384000},
		},
	}
}
