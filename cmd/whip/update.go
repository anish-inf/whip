package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/context-labs/whip/internal/update"
)

// installURL is the same curl-pipe-sh installer the README documents; update
// just re-runs it — the script resolves the latest release, verifies the
// checksum, and swaps the binary in place.
const installURL = "https://raw.githubusercontent.com/context-labs/whip/main/install.sh"

// updateCLI implements `whip update`: re-run the install script to get the
// latest release.
func updateCLI() error {
	fmt.Printf("whip %s — updating to the latest release via\n  curl -fsSL %s | sh\n\n", version, installURL)
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "curl -fsSL "+installURL+" | sh")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("update failed: %w", err)
	}
	update.Acknowledge() // the pending startup notice is now satisfied
	fmt.Println("\nwhip updated — restart any running sessions to use the new version.")
	return nil
}
