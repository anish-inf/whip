// loopy is a minimal coding agent harness.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/abe/loopy/internal/agent"
	"github.com/abe/loopy/internal/config"
	"github.com/abe/loopy/internal/llm"
	"github.com/abe/loopy/internal/tui"
)

var version = "dev" // set via -ldflags "-X main.version=..."

func systemPrompt() string {
	wd, _ := os.Getwd()
	prompt := fmt.Sprintf(`You are an expert coding assistant operating inside loopy, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Available tools:
- read: Read file contents
- bash: Execute bash commands (ls, grep, find, etc.)
- edit: Make precise file edits with exact text replacement
- write: Create or overwrite files
- task: Delegate a self-contained task to a subagent with fresh context

Guidelines:
- Use bash for file operations like ls, rg, find
- Use read to examine files instead of cat or sed
- Use edit for precise changes (old_string must match exactly and be unique, or set replace_all)
- Use write only for new files or complete rewrites
- When the user tags a file with @, a note lists the tagged paths — inspect them with your tools as needed
- Be concise in your responses
- Show file paths clearly when working with files

Current working directory: %s`, wd)
	// the skills block is appended fresh each turn by the TUI, so newly added
	// skills are picked up without restarting
	return prompt
}

func main() {
	modelFlag := flag.String("m", "", "model name from ~/.loopy/config.json (default: defaultModel)")
	providerFlag := flag.String("p", "", "provider to route the model through (default: model's first provider)")
	versionFlag := flag.Bool("version", false, "print version")
	resumeFlag := flag.String("resume", "", "resume a previous session by id (or unique prefix)")
	benchFlag := flag.Bool("bench", false, "do full startup init (config, routing, key, agent) then exit; for `task benchmark`")
	flag.Parse()

	if *versionFlag {
		fmt.Println("loopy", version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "loopy:", err)
		os.Exit(1)
	}

	if *benchFlag {
		prov, mdl, id, err := cfg.Resolve(*modelFlag, *providerFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "loopy:", err)
			os.Exit(1)
		}
		_ = prov.Key()
		_ = agent.New(llm.New(prov.BaseURL, "bench"), id, mdl.MaxTokens, systemPrompt())
		return
	}
	sessionID, err := tui.Run(cfg, *modelFlag, *providerFlag, systemPrompt(), *resumeFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loopy:", err)
		os.Exit(1)
	}
	if sessionID != "" {
		fmt.Printf("session %s — resume with: loopy --resume %s\n", sessionID, sessionID)
	}
}
