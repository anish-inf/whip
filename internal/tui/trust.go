package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/context-labs/whip/internal/config"
)

// checkTrust gates startup on the folder-trust dialog. If the cwd is already
// trusted (~/.whip/trusted.json), it returns immediately. Otherwise it asks
// on the terminal (plain stdin/stdout — this runs before the TUI starts):
// "Yes, proceed" records the path; anything else declines. When stdin isn't
// a terminal (piped run, tests), we can't ask — decline safely.
func checkTrust() (bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	if config.Trusted(wd) {
		return true, nil
	}
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		// no terminal to ask on: don't read untrusted files silently
		return false, fmt.Errorf("folder %s is not trusted (run interactively once to trust it, or add it to ~/.whip/trusted.json)", wd)
	}
	fmt.Fprintf(os.Stderr, "\nDo you trust the files in this folder?\n%s\n\n", wd)
	fmt.Fprintln(os.Stderr, "whip may read files in this folder. Reading untrusted files may lead whip to behave in unexpected ways.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "With your permission whip may execute files in this folder. Executing untrusted code is unsafe.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprint(os.Stderr, "  1. Yes, proceed\n  2. No, exit\n\n❯ ")
	r := bufio.NewReader(os.Stdin)
	ans, err := r.ReadString('\n')
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(ans) == "1" {
		if err := config.Trust(wd); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
