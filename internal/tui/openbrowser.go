package tui

import (
	"context"
	"os/exec"
	"runtime"
)

// openBrowserURL opens url in the default browser; false when it can't
// (headless box, no opener). Best-effort: the auth flow prints the URL for
// manual opening regardless.
func openBrowserURL(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", url)
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", url)
	}
	return cmd.Start() == nil
}
