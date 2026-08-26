package config

import (
	"os"
	"path/filepath"
	"strings"
)

// me.md: the user's standing instructions for the agent. The file is
// APPENDED to the built-in operating rules (they carry the safety rails —
// secrets review, no force-push — and never go away), so the seed is a
// commented template, not a copy of the defaults that would silently
// diverge. /me opens the file in $EDITOR.

// MeSeed is what ~/.whip/me.md starts with.
const MeSeed = `# Your standing instructions for whip — appended to every session's
# system prompt, after the built-in operating rules. Lines starting with #
# are comments. Edit freely; /me opens this file.

# Examples:
# - Always run tests with pnpm, never npm.
# - I review every commit message before you commit — always show me the message first.
# - Never touch files under deploy/prod/ without asking.
`

// MePath returns ~/.whip/me.md (seeding the template on first run); "" when
// the home dir is unavailable.
func MePath() string {
	dir, err := Dir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "me.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(MeSeed), 0o600); err != nil {
			return ""
		}
	}
	return path
}

// MeInstructions loads the user's standing instructions from ~/.whip/me.md,
// comments and blank lines stripped. "" means nothing to append.
func MeInstructions() string {
	path := MePath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
