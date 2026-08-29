package tui

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/config"
)

// The startup gate: a trusted cwd passes without a prompt; an untrusted one
// declines when there's no terminal to ask on.
func TestTrustGate(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	wd, _ := os.Getwd()
	if err := config.Trust(wd); err != nil {
		t.Fatal(err)
	}
	ok, err := checkTrust(bufio.NewReader(strings.NewReader("")))
	if err != nil || !ok {
		t.Fatalf("trusted cwd should pass: %v %v", ok, err)
	}
}
