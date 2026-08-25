// Package tools implements the agent's built-in tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/tools/bashrun"
)

// Tool is a named executable tool with a JSON schema.
type Tool struct {
	Def llm.Tool
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

// InteractiveRunner runs an interactive bash command with PTY-backed live I/O.
// The TUI installs one so the agent's bash tool can hand interactive prompts
// (sudo, ssh, gpg) to the user. ctx caps the whole run; keys feeds keystrokes
// the user types; the returned string is fed back to the model as tool output.
// Implementations must be safe to call from a goroutine that is not the UI
// thread, and must not block forever when no input arrives.
type InteractiveRunner interface {
	Run(ctx context.Context, command string, timeout time.Duration, keys <-chan []byte) string
}

// InteractiveBash is the hook installed by the TUI; nil means the agent's bash
// tool runs interactive commands itself using the non-interactive fallback
// (which fast-fails sudo-style prompts instead of hanging).
var InteractiveBash InteractiveRunner

// LSP, when non-nil, feeds language-server diagnostics back to the model by
// appending a <diagnostics> block to write/edit tool output (see
// internal/lsp). Installed by the TUI at startup; nil in tests and headless
// runs. Implementations must be safe for concurrent use (parallel tool
// calls) and must honor ctx (ctrl+c cancels the wait).
var LSP interface {
	WaitDiagnostics(ctx context.Context, path string) string
}

// All returns the built-in tool set.
func All() []Tool {
	return []Tool{bashTool(), readTool(), writeTool(), editTool()}
}

// Defs returns the llm.Tool definitions for a tool set.
func Defs(ts []Tool) []llm.Tool {
	defs := make([]llm.Tool, len(ts))
	for i, t := range ts {
		defs[i] = t.Def
	}
	return defs
}

// Suggester returns the closest known tool names for an unknown one —
// installed by the agent (which knows the live MCP tool set) so a stale or
// typo'd tool call nudges the model toward the right name instead of
// dead-ending the turn.
var Suggester func(name string) []string

// Execute runs the named tool. Errors are returned as strings so they can be
// fed back to the model rather than aborting the loop.
func Execute(ctx context.Context, ts []Tool, name string, args json.RawMessage) string {
	for _, t := range ts {
		if t.Def.Function.Name == name {
			out, err := t.Run(ctx, args)
			if err != nil {
				return "Error: " + err.Error()
			}
			if out == "" {
				out = "(no output)"
			}
			return out
		}
	}
	msg := fmt.Sprintf("Error: unknown tool %q", name)
	if Suggester != nil {
		if hints := Suggester(name); len(hints) > 0 {
			msg += " — did you mean " + strings.Join(hints, " or ") + "?"
		}
	}
	return msg
}

const maxOutput = 50_000 // bytes of tool output fed back to the model

// Truncate caps tool output at maxOutput with a marker; exported for the MCP
// bridge, which flattens remote results into the same budget.
func Truncate(s string) string {
	return truncate(s)
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + fmt.Sprintf("\n... [truncated %d bytes]", len(s)-maxOutput)
}

// lspDiagnostics appends the LSP diagnostics block for a just-written file.
// Never fails the tool: a nil hook, an uncovered file, or a slow server all
// yield "" (the wait is capped inside internal/lsp).
func lspDiagnostics(ctx context.Context, path string) string {
	if LSP == nil {
		return ""
	}
	return LSP.WaitDiagnostics(ctx, path)
}

// TruncateTail caps tool output at maxOutput bytes, keeping the tail (the end
// is usually where the error is). Exported for the TUI's `!` shell escape,
// which formats output exactly like the bash tool.
func TruncateTail(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return fmt.Sprintf("[... first %d bytes truncated]\n", len(s)-maxOutput) + s[len(s)-maxOutput:]
}

func bashTool() Tool {
	return Tool{
		Def: llm.NewTool("bash",
			"Execute a bash command in the current working directory and return its combined stdout/stderr. Use for running programs, git, searching (grep/rg), listing files, etc.",
			`{"type":"object","properties":{"command":{"type":"string","description":"The bash command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (default 120)"},"interactive":{"type":"boolean","description":"Run in a PTY so sudo/ssh-style password prompts work. Whip stays in control of the terminal and forwards your keystrokes; the command is killed after 15s of no input. Use only for commands that genuinely need a password."}},"required":["command"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Command     string  `json:"command"`
				Timeout     float64 `json:"timeout"`
				Interactive bool    `json:"interactive"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if a.Timeout <= 0 {
				a.Timeout = 120
			}
			dur := time.Duration(a.Timeout * float64(time.Second))

			// Interactive mode hands the live terminal to the user only when the
			// TUI has wired a runner. Without it we run non-interactively, which
			// fails sudo-style prompts fast instead of hanging on whip's tty.
			if a.Interactive && InteractiveBash != nil {
				keys := make(chan []byte, 16)
				out := InteractiveBash.Run(ctx, a.Command, dur, keys)
				return TruncateTail(out), nil
			}

			res := bashrun.Run(ctx, bashrun.Options{
				Command: a.Command,
				Timeout: dur,
			})

			s := TruncateTail(res.Output)
			if res.TimedOut {
				return s + "\n(command timed out)", nil
			}
			if res.Exit != "" {
				return fmt.Sprintf("%s\n(%s)", s, res.Exit), nil
			}
			if s == "" {
				return "(no output)", nil
			}
			return s, nil
		},
	}
}

func readTool() Tool {
	return Tool{
		Def: llm.NewTool("read",
			"Read a file and return its contents with line numbers.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"offset":{"type":"number","description":"1-based line to start from"},"limit":{"type":"number","description":"Max lines to return (default 2000)"}},"required":["path"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			lines := strings.Split(string(data), "\n")
			start := max(a.Offset-1, 0)
			if start >= len(lines) {
				return "", fmt.Errorf("offset %d past end of file (%d lines)", a.Offset, len(lines))
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 2000
			}
			end := min(start+limit, len(lines))
			var b strings.Builder
			for i := start; i < end; i++ {
				fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
			}
			return truncate(b.String()), nil
		},
	}
}

func writeTool() Tool {
	return Tool{
		Def: llm.NewTool("write",
			"Write content to a file, creating it (and parent directories) or overwriting it.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"content":{"type":"string","description":"Full file content"}},"required":["path","content"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Wrote %d bytes to %s", len(a.Content), a.Path) + lspDiagnostics(ctx, a.Path), nil
		},
	}
}

func editTool() Tool {
	return Tool{
		Def: llm.NewTool("edit",
			"Replace an exact string in a file. old_string must appear exactly once unless replace_all is true.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Path to the file"},"old_string":{"type":"string","description":"Exact text to replace"},"new_string":{"type":"string","description":"Replacement text"},"replace_all":{"type":"boolean","description":"Replace every occurrence"}},"required":["path","old_string","new_string"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path       string `json:"path"`
				OldString  string `json:"old_string"`
				NewString  string `json:"new_string"`
				ReplaceAll bool   `json:"replace_all"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			s := string(data)
			n := strings.Count(s, a.OldString)
			switch {
			case n == 0:
				return "", fmt.Errorf("old_string not found in %s", a.Path)
			case n > 1 && !a.ReplaceAll:
				return "", fmt.Errorf("old_string appears %d times in %s; make it unique or set replace_all", n, a.Path)
			}
			s = strings.ReplaceAll(s, a.OldString, a.NewString)
			if err := os.WriteFile(a.Path, []byte(s), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Replaced %d occurrence(s) in %s", n, a.Path) + lspDiagnostics(ctx, a.Path), nil
		},
	}
}
