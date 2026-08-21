package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var rangeRe = regexp.MustCompile(`#(\d+)(?:-(\d+))?$`)

// expandMentions finds @file tokens (any path: relative, absolute, or ~, with
// optional #start-end line ranges) and appends a pointer note. File contents
// are never inlined — the model inspects tagged files with its own tools.
func expandMentions(text string) string {
	var notes []string
	for _, tok := range strings.Fields(text) {
		if !strings.HasPrefix(tok, "@") || len(tok) < 2 {
			continue
		}
		p := strings.TrimRight(tok[1:], ".,;:!?)\"'")
		lines := ""
		if m := rangeRe.FindStringSubmatch(p); m != nil {
			p = strings.TrimSuffix(p, m[0])
			lines = " (lines " + m[1]
			if m[2] != "" {
				lines += "-" + m[2]
			}
			lines += ")"
		}
		abs := p
		if abs == "~" || strings.HasPrefix(abs, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				abs = home + abs[1:]
			}
		}
		if !filepath.IsAbs(abs) {
			if wd, err := os.Getwd(); err == nil {
				abs = filepath.Join(wd, abs)
			}
		}
		if _, err := os.Stat(abs); err != nil {
			continue // not a real path; leave the token alone
		}
		notes = append(notes, abs+lines)
	}
	if len(notes) == 0 {
		return text
	}
	return text + "\n\n[note: the user tagged " + strings.Join(notes, "; ") +
		" — contents are not inlined; inspect with your tools as needed]"
}
