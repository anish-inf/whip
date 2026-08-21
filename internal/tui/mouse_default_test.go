package tui

import (
	"testing"

	"github.com/context-labs/loopy/internal/config"
)

// Mouse capture defaults OFF so native drag-to-copy works out of the box
// (opencode/codex behavior); a config "mouse": true opts back into capture.
func TestMouseDefaultsOff(t *testing.T) {
	cfg := config.Default()
	if cfg.Mouse != nil {
		t.Fatalf("default config must not set mouse (nil = off), got %v", *cfg.Mouse)
	}
	b := false
	cfg2 := &config.Config{Mouse: &b}
	if cfg2.Mouse == nil || *cfg2.Mouse {
		t.Fatal("explicit false should stay off")
	}
}
