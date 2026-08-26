// Permission gating for the tools that touch the world: bash, write, edit.
// The TUI installs Gate; a gated call blocks until the user answers Allow
// once / Allow always / Reject. "Always" records a rule at command-prefix
// arity (so "git checkout main" allows future "git checkout …", not the whole
// string). No gate (tests, headless) means allow — the gate is a UX layer,
// not a sandbox.
package tools

import (
	"strings"
)

// GateDecision is the user's answer to a permission prompt.
type GateDecision int

const (
	GateAllowOnce GateDecision = iota
	GateAllowAlways
	GateReject
)

// GateRequest describes one gated tool call for the prompt.
type GateRequest struct {
	Tool    string // bash | write | edit
	Command string // the bash command or the file path
	Rule    string // the rule "always" would install (arity-collapsed)
}

// Gate is the installed permission hook. It returns the decision and, on
// reject, a free-text redirect that goes back to the model. Nil = allow.
var Gate func(GateRequest) (GateDecision, string)

// arity maps a command prefix to how many tokens define "the command" —
// longest prefix wins, flags never count. Compact version of opencode's
// generated table (permission/arity.ts); the common cases carry the value.
var arity = map[string]int{
	// one-token commands: the binary alone is the rule
	"ls": 1, "cat": 1, "pwd": 1, "grep": 1, "find": 1, "echo": 1,
	"rm": 1, "mv": 1, "cp": 1, "mkdir": 1, "touch": 1, "which": 1,
	// two-token: binary + subcommand
	"git": 2, "npm": 2, "pnpm": 2, "yarn": 2, "go": 2, "cargo": 2,
	"docker": 2, "kubectl": 2, "brew": 2, "apt": 2, "pip": 2,
	// three-token where the shorter prefix under-specifies
	"npm run": 3, "pnpm run": 3, "go tool": 3, "docker compose": 3, "git submodule": 3,
}

// CommandRule collapses a shell command to its arity rule: the prefix "always"
// should install. Only the first command of a pipeline/chain is considered —
// "git checkout main && rm -rf /" is not a "git checkout" rule.
func CommandRule(command string) string {
	cmd := strings.TrimSpace(command)
	// stop at the first shell operator: the rule covers one command, not a chain
	for i, r := range cmd {
		if r == '&' || r == '|' || r == ';' || r == '>' || r == '<' {
			cmd = strings.TrimSpace(cmd[:i])
			break
		}
	}
	tokens := strings.Fields(cmd)
	for i := 0; i < len(tokens); i++ {
		if strings.Contains(tokens[i], "=") && !strings.HasPrefix(tokens[i], "-") && i == 0 {
			// leading VAR=value assignments aren't part of the command
			tokens = tokens[1:]
			i = -1
		}
	}
	if len(tokens) == 0 {
		return ""
	}
	// longest matching prefix wins
	for n := len(tokens); n > 0; n-- {
		prefix := strings.Join(tokens[:n], " ")
		if a, ok := arity[prefix]; ok {
			return strings.Join(tokens[:min(a, len(tokens))], " ")
		}
	}
	return tokens[0] // unknown command: the binary is the rule
}

// checkGate runs the installed gate for a tool call; "" means proceed, a
// non-empty string is the rejection fed back to the model.
func checkGate(tool, command string) string {
	if Gate == nil {
		return ""
	}
	decision, redirect := Gate(GateRequest{Tool: tool, Command: command, Rule: CommandRule(command)})
	if decision == GateReject {
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	}
	return ""
}
