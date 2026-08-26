// `whip run` — non-interactive (headless) mode: one turn of the agent with
// no TUI and no trust prompt, for trusted automation and scripting. Piped
// stdin is appended to the prompt. --format json emits the raw event stream
// as newline-delimited JSON; the final event is {"type":"done",...} or
// {"type":"error",...}. Exit code 0 on success, 1 on error.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/context-labs/whip/internal/agent"
	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/session"
)

func runCLI(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	format := fs.String("format", "text", "output format: text (stream the reply) or json (newline-delimited event stream)")
	modelFlag := fs.String("m", "", "model name from ~/.whip/config.json (default: defaultModel)")
	providerFlag := fs.String("p", "", "provider to route the model through (default: model's first provider)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: whip run [--format text|json] [-m model] [-p provider] \"prompt\"")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *format {
	case "text", "json":
	default:
		return fmt.Errorf("unknown --format %q (want text|json)", *format)
	}

	prompt := strings.Join(fs.Args(), " ")
	// Piped stdin is appended to the prompt (both matter: e.g.
	// `git diff | whip run "review this"`). Read only when stdin is not a
	// TTY, so interactive `whip run "…"` never blocks on it.
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			if piped := strings.TrimSpace(string(data)); piped != "" {
				if prompt != "" {
					prompt += "\n\n"
				}
				prompt += piped
			}
		}
	}
	if prompt == "" {
		fs.Usage()
		return fmt.Errorf("no prompt given (pass one as an argument or pipe it on stdin)")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	prov, mdl, apiID, err := cfg.Resolve(*modelFlag, *providerFlag)
	if err != nil {
		return err
	}
	modelName, provName := *modelFlag, *providerFlag
	if modelName == "" {
		modelName = cfg.DefaultModel
	}
	if provName == "" {
		provName = cfg.DefaultProvider
		if provName == "" && len(mdl.Providers) > 0 {
			provName = mdl.Providers[0]
		}
	}
	key := prov.Key()
	if key == "" {
		return fmt.Errorf("no API key for provider %q (set apiKey/apiKeyEnv in ~/.whip/config.json)", provName)
	}

	client := llm.New(prov.BaseURL, key)
	client.MaxRetries = cfg.MaxRetries
	ag := agent.New(client, apiID, mdl.MaxTokens, systemPrompt())
	ag.ModelName, ag.Provider = modelName, provName
	// Headless runs have no one to answer a consent prompt: computer_exec
	// stays disabled (no interactive approver is ever installed).
	ag.ComputerDisabled = true
	ag.ContextLimit = mdl.ContextWindow()
	ag.Effort = cfg.DefaultEffort
	if ag.Effort == "" {
		ag.Effort = "medium"
	}

	// Land the turn in the session store like a TUI turn (resumable with
	// `whip --resume <id>`), without requiring a TTY or the trust prompt.
	// The store is best-effort: a run never fails over session persistence.
	var store *session.Store
	var sessionID string
	if dir, derr := config.Dir(); derr == nil {
		if st, serr := session.Open(dir + "/sessions.db"); serr == nil {
			store = st
			defer func() { _ = st.Close() }()
			if cwd, cerr := os.Getwd(); cerr == nil {
				if id, ierr := st.Create(cwd, modelName, provName); ierr == nil {
					sessionID = id
				}
			}
		}
	}

	// ctrl+c cancels the turn rather than orphaning an in-flight request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ev := agent.Events{}
	var emit func(any) // set only for --format json
	if *format == "json" {
		enc := json.NewEncoder(os.Stdout)
		emit = func(v any) {
			if err := enc.Encode(v); err != nil {
				fmt.Fprintln(os.Stderr, "whip: json encode:", err)
			}
		}
		ev.OnText = func(d string) { emit(map[string]string{"type": "text", "delta": d}) }
		ev.OnToolStart = func(_, name, args string) {
			emit(map[string]string{"type": "tool_start", "name": name, "args": args})
		}
		ev.OnToolEnd = func(_, name, result string) {
			emit(map[string]string{"type": "tool_end", "name": name, "result": result})
		}
	} else {
		ev.OnText = func(d string) { fmt.Fprint(os.Stdout, d) }
		ev.OnToolStart = func(_, name, args string) { fmt.Fprintf(os.Stderr, "⚒ %s\n", name) }
	}

	final, err := ag.Turn(ctx, prompt, ev)
	if emit != nil {
		if err != nil {
			emit(map[string]string{"type": "error", "error": err.Error()})
		} else {
			emit(map[string]string{"type": "done", "text": final})
		}
	} else {
		fmt.Fprintln(os.Stdout) // end the streamed reply's line
	}

	// Best-effort persistence (the TUI's persist does the same each turn).
	if store != nil && sessionID != "" {
		if serr := store.Save(sessionID, 1, ag.MessagesSnapshot(), modelName, provName); serr != nil {
			config.LogEvent("session.save", "run FAILED id="+sessionID+": "+serr.Error())
		}
		fmt.Fprintf(os.Stderr, "session %s — resume with: whip --resume %s\n", sessionID, sessionID)
	}
	return err
}
