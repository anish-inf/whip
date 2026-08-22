// Package config loads and saves loopy's JSONC configuration from ~/.loopy.
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
	// Context is the model's context window (max INPUT tokens). The provider's
	// /models context_length overrides it when advertised; this is the fallback
	// and the value shown for providers that don't report one.
	Context int `json:"context,omitempty"`
	// MaxOut caps OUTPUT tokens (the max_tokens request param). 0 uses the
	// provider's max_completion_tokens when advertised, else a sane default.
	MaxOut int `json:"maxOut,omitempty"`
	// MaxTokens is the legacy field name for Context (it was misnamed: it held
	// the context window, not an output cap). Read on load for back-compat.
	MaxTokens int `json:"maxTokens,omitempty"`
}

// ContextWindow returns the model's context (input) size, honoring the legacy
// maxTokens field for configs written before the rename.
func (m Model) ContextWindow() int {
	if m.Context > 0 {
		return m.Context
	}
	return m.MaxTokens
}

// Config is the root of ~/.loopy/config.json (JSONC: comments allowed).
type Config struct {
	DefaultModel    string              `json:"defaultModel"`
	DefaultProvider string              `json:"defaultProvider,omitempty"` // override the model's first provider
	DefaultEffort   string              `json:"defaultEffort,omitempty"`   // reasoning effort for new sessions: "", "low", "medium", "high"
	CompactModel    string              `json:"compactModel,omitempty"`    // model for compaction summaries; "" = the conversation's model
	CompactProvider string              `json:"compactProvider,omitempty"` // provider for the compaction model; "" = the conversation's provider
	Theme           string              `json:"theme,omitempty"`           // "light", "dark", or "" (auto-detect at startup)
	Mouse           *bool               `json:"mouse,omitempty"`           // false disables capture so native terminal selection works
	GoalMaxRounds   int                 `json:"goalMaxRounds,omitempty"`   // global goal-loop round cap; 0 = DefaultGoalMaxRounds; projects.json may override per folder
	Providers       map[string]Provider `json:"providers"`
	Models          map[string]Model    `json:"models"`
	// MCPServers is loopy's own MCP server block (loopy-native shape; see
	// internal/mcp.ServerConfig for the normalized semantics). On load it is
	// merged over imported claude/codex configs: loopy always wins per name.
	MCPServers map[string]MCPServer `json:"mcp,omitempty"`
}

// MCPServer is the config-file form of an MCP server entry. It mirrors
// mcp.ServerConfig without importing that package (config is a leaf).
type MCPServer struct {
	Command        []string          `json:"command,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Enabled        *bool             `json:"enabled,omitempty"`
	Note           string            `json:"note,omitempty"`
	StartupTimeout int               `json:"startupTimeout,omitempty"`
	ToolTimeout    int               `json:"toolTimeout,omitempty"`
}

// Dir returns the loopy home directory (~/.loopy), creating it if needed.
// LOOPY_HOME overrides the location — used by tests to keep fixture writes
// far away from the real config.
func Dir() (string, error) {
	if d := os.Getenv("LOOPY_HOME"); d != "" {
		return d, os.MkdirAll(d, 0o700)
	}
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

// fingerprint summarizes a config for the operation log: enough to spot a
// clobbering write (providers/models collapsing, fixture values appearing)
// without logging secrets.
func (c *Config) fingerprint() string {
	return fmt.Sprintf("providers=%d models=%d default=%q compact=%q",
		len(c.Providers), len(c.Models), c.DefaultModel, c.CompactModel)
}

// Load reads ~/.loopy/config.json, writing a default config on first run. The
// file is JSONC: comments and trailing commas are allowed.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		cfg := Default()
		logf("config.load", "missing file, writing defaults (%s)", cfg.fingerprint())
		return cfg, cfg.Save()
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := parseJSONC(data, &cfg); err != nil {
		logf("config.load", "PARSE FAILURE %s: %v (%d bytes)", p, err, len(data))
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	// Recover from a clobbered/empty config: no providers and no models is
	// never a usable state, so prefer the backup, else regenerate defaults —
	// BUT preserve any MCP server entries: an mcp-only config is valid (the
	// user may configure servers before providers), and regenerating defaults
	// would silently wipe them.
	if len(cfg.Providers) == 0 && len(cfg.Models) == 0 {
		logf("config.load", "CLOBBERED/EMPTY config detected (%d bytes on disk), attempting recovery", len(data))
		if bak, err := os.ReadFile(p + ".bak"); err == nil {
			var restored Config
			if parseJSONC(bak, &restored) == nil && (len(restored.Providers) > 0 || len(restored.Models) > 0) {
				logf("config.load", "restored from .bak (%s)", restored.fingerprint())
				if len(restored.MCPServers) == 0 && len(cfg.MCPServers) > 0 {
					restored.MCPServers = cfg.MCPServers // keep the user's servers
				}
				return &restored, restored.Save()
			}
		}
		def := Default()
		def.MCPServers = cfg.MCPServers // mcp-only configs are valid; keep them
		logf("config.load", "no usable .bak; regenerated defaults (%s), keeping %d mcp entries", def.fingerprint(), len(cfg.MCPServers))
		return def, def.Save()
	}
	logf("config.load", "ok (%s)", cfg.fingerprint())
	return &cfg, nil
}

// Save writes the config back to ~/.loopy/config.json. The write is atomic
// (temp file + rename) and the previous contents are kept in config.json.bak.
// As a safety net, Save refuses to overwrite an existing healthy config (one
// with providers/models) with a structurally empty one — that path has only
// ever been reached by a bug, never intentionally.
func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if len(c.Providers) == 0 && len(c.Models) == 0 {
		if existing, err := os.ReadFile(p); err == nil {
			var cur Config
			if parseJSONC(existing, &cur) == nil && (len(cur.Providers) > 0 || len(cur.Models) > 0) {
				logf("config.save", "REFUSED empty overwrite of healthy config (disk had providers=%d models=%d)", len(cur.Providers), len(cur.Models))
				return fmt.Errorf("refusing to overwrite %s: existing config has providers/models but the value being saved is empty", p)
			}
		}
	}
	data, err := marshalConfig(c)
	if err != nil {
		return err
	}
	// log the before/after fingerprint so a bad write is attributable
	if existing, err := os.ReadFile(p); err == nil && len(existing) > 0 {
		var cur Config
		if parseJSONC(existing, &cur) == nil {
			logf("config.save", "before=(%s) after=(%s)", cur.fingerprint(), c.fingerprint())
		} else {
			logf("config.save", "before=(unparseable, %d bytes) after=(%s)", len(existing), c.fingerprint())
		}
		// best-effort backup before replacing
		_ = os.WriteFile(p+".bak", existing, 0o600)
	} else {
		logf("config.save", "first write (%s)", c.fingerprint())
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		logf("config.save", "write tmp failed: %v", err)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		logf("config.save", "rename failed: %v", err)
		return err
	}
	return nil
}

// marshalConfig renders the config as JSONC with a header comment.
func marshalConfig(c *Config) ([]byte, error) {
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	header := "// loopy configuration — JSONC: comments and trailing commas are allowed.\n" +
		"// providers: declare each API endpoint once. models: route each model to one or\n" +
		"// more providers (first is the default). defaultModel/defaultProvider pick the route.\n"
	out := append([]byte(header), body...)
	return append(out, '\n'), nil
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
			"kimi-k3-fast":           {Providers: []string{"inference"}, Context: 131072},
			"glm-5.2-fast":           {Providers: []string{"inference"}, Context: 128000},
			"deepseek-v4-flash-0731": {Providers: []string{"inference"}, Context: 384000},
		},
	}
}
