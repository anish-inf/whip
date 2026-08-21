package tui

import (
	"os"
	"testing"
)

// TestMain is a safety net: several TUI code paths persist through
// config.Save() (setEffort, switchModel, compactCommand, /mouse). Without
// isolation those writes land in the REAL ~/.loopy/config.json — this exact
// bug corrupted the config twice. Point the whole test binary at a scratch
// LOOPY_HOME so even a future test that forgets t.Setenv cannot clobber the
// user's setup. Per-test overrides still apply on top.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "loopy-test-home")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	os.Setenv("LOOPY_HOME", dir)
	os.Exit(m.Run())
}
