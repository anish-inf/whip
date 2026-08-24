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
	{"/cd", "[dir] — change working directory (bare prints it)"},
	{"/clear", "Reset the conversation"},
	{"/compact", "[model] [provider]|off — compact now, or pick the compaction model"},
	{"/context-doctor", "Audit what a fresh session injects (skills, MCP, tool schemas) and its token cost"},
	{"/effort", "[level] — reasoning effort: off·low·medium·high (bare cycles)"},
	{"/fork", "[name] — copy the conversation into a new named session"},
	{"/goal", "<text> — work until done; also: resume, clear, rounds <n>|default [--global]"},
	{"/goal-from-context", "Formulate a goal from the last two messages and work until it's done"},
	{"/help", "Show available commands"},
	{"/mcp", "[name] [reconnect|enable|disable] — MCP servers: status, reconnect, toggle"},
	{"/model", "<model> [provider] — switch model"},
	{"/mouse", "Toggle mouse capture (off = native terminal selection)"},
	{"/pwd", "Print working directory"},
	{"/rename", "[title] — retitle this session"},
	{"/theme", "[light|dark|auto] — color scheme (bare toggles)"},
	{"/quit", "Exit loopy"},
	{"/resume", "[id] — browse and resume previous sessions"},
	{"/tasks", "[id] — background subagents: focus the dock, or open one task's live view"},
}

// execNow lists commands the menu runs immediately on enter (they act
// sensibly with no arguments); others insert themselves for arguments.
var execNow = map[string]bool{
	"/clear": true, "/compact": true, "/context-doctor": true, "/effort": true, "/goal": true, "/goal-from-context": true, "/help": true,
	"/mcp": true, "/model": true, "/mouse": true, "/pwd": true, "/quit": true, "/resume": true, "/tasks": true,
}

// completions splits val into an untouched head and candidates for its last
// token: slash commands, /model or /effort arguments, $skills, or filesystem
// paths. nil efforts uses the default /effort candidates.
func completions(val string, models, providers, skillCands, efforts []cand) (head string, cands []cand) {
	if efforts == nil {
		efforts = effortCands
	}
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
	case len(fields) == 1 && fields[0] == "/effort":
		cands = filterPrefix(efforts, token)
	case len(fields) == 1 && fields[0] == "/compact":
		cands = filterPrefix(append([]cand{{"off", "compact with the current model"}}, models...), token)
	case len(fields) == 2 && fields[0] == "/compact":
		cands = filterPrefix(providers, token)
	case strings.HasPrefix(token, "$"): // codex-style skill invocation
		cands = filterPrefix(skillCands, token)
	case strings.HasPrefix(token, "@"):
		// @file mentions: path-like queries (with a separator, ~, or leading
		// dot) complete like paths; bare words fuzzy-match the recursive
		// index so "@roadmap" finds docs/roadmap.md without the full path.
		if q := token[1:]; isPathQuery(q) {
			for _, c := range mentionPathMatches(q) {
				cands = append(cands, cand{"@" + c.Text, c.Desc})
			}
		} else {
			for _, f := range fuzzyFiles(q, menuRows) {
				cands = append(cands, cand{"@" + f, ""})
			}
		}
	case strings.HasPrefix(val, "/"): // other slash-command args: nothing to complete
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

// isPathQuery reports whether an @mention query looks like a path (has a
// separator, ~, or leading dot) and should use plain glob completion rather
// than the recursive fuzzy index.
func isPathQuery(q string) bool {
	return q == "" || strings.ContainsAny(q, "/\\") || strings.HasPrefix(q, "~") || strings.HasPrefix(q, ".")
}

// mentionPathMatches globs an @mention path query against the mention root
// (not the process cwd): absolute and ~ queries glob as-is; relative ones are
// joined to the root and returned root-relative, with dirs keeping their
// trailing slash.
func mentionPathMatches(q string) []cand {
	if filepath.IsAbs(q) || q == "~" || strings.HasPrefix(q, "~/") {
		return pathMatches(q)
	}
	root, err := currentRoot()
	if err != nil {
		return nil
	}
	var out []cand
	for _, c := range pathMatches(filepath.Join(root, q)) {
		dir := strings.HasSuffix(c.Text, "/")
		if rel, err := filepath.Rel(root, strings.TrimSuffix(c.Text, "/")); err == nil {
			c.Text = filepath.ToSlash(rel)
			if dir {
				c.Text += "/"
			}
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
