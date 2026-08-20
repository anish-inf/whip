package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cand is one completion candidate with an optional description.
type cand struct {
	Text string
	Desc string
}

var commands = []cand{
	{"/clear", "Reset the conversation"},
	{"/help", "Show available commands"},
	{"/model", "<model> [provider] — switch model"},
	{"/quit", "Exit loopy"},
	{"/resume", "[id] — browse and resume previous sessions"},
}

// completions splits val into an untouched head and candidates for its last
// token: slash commands, /model arguments, or filesystem paths.
func completions(val string, models, providers []cand) (head string, cands []cand) {
	i := strings.LastIndexByte(val, ' ')
	head, token := val[:i+1], val[i+1:]
	fields := strings.Fields(head)
	switch {
	case strings.HasPrefix(val, "/") && len(fields) == 0:
		cands = filterPrefix(commands, token)
	case len(fields) == 1 && fields[0] == "/model":
		cands = filterPrefix(models, token)
	case len(fields) == 2 && fields[0] == "/model":
		cands = filterPrefix(providers, token)
	default:
		cands = pathMatches(token)
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].Text < cands[b].Text })
	return head, cands
}

func filterPrefix(all []cand, prefix string) []cand {
	var out []cand
	for _, c := range all {
		if strings.HasPrefix(c.Text, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func pathMatches(prefix string) []cand {
	p := prefix
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + p[1:]
		}
	}
	matches, _ := filepath.Glob(p + "*")
	var out []cand
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			out = append(out, cand{m + "/", "dir"})
		} else {
			out = append(out, cand{m, ""})
		}
	}
	return out
}
