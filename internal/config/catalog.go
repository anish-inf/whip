package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// catalogTTL is how long a provider's fetched model list stays fresh.
const catalogTTL = 24 * time.Hour

// Catalog is the cached model list of one provider.
type Catalog struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	BaseURL   string          `json:"baseUrl"`
	Models    []ModelInfoLite `json:"models"`
}

// ModelInfoLite is the subset of the provider's /models entry loopy uses.
type ModelInfoLite struct {
	ID               string   `json:"id"`
	ContextLength    int      `json:"contextLength,omitempty"` // model's context window, 0 if unadvertised
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
}

// ContextLength reports the advertised context window for a model id
// (0 when the catalog has no entry for it — callers must fall back).
func (c Catalog) ContextLength(id string) int {
	for _, mi := range c.Models {
		if mi.ID == id {
			return mi.ContextLength
		}
	}
	return 0
}

func catalogPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models.json"), nil
}

// LoadCatalogs reads ~/.loopy/models.json. A missing or unreadable file is
// not an error and yields an empty (non-nil) map, so callers can always write
// into the result.
func LoadCatalogs() map[string]Catalog {
	cats := map[string]Catalog{}
	p, err := catalogPath()
	if err != nil {
		return cats
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return cats
	}
	if json.Unmarshal(data, &cats) != nil || cats == nil {
		return map[string]Catalog{}
	}
	return cats
}

// SaveCatalogs writes ~/.loopy/models.json.
func SaveCatalogs(cats map[string]Catalog) error {
	p, err := catalogPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cats, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

// Stale reports whether the cached catalog should be refetched.
func (c Catalog) Stale() bool { return time.Since(c.FetchedAt) > catalogTTL }
