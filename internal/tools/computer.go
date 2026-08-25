// computer.go wires internal/computer into the tool set as `computer_exec` —
// drive the user's desktop (apps, browser via AppleScript, screen) with the
// same helper-call mini-language as browser_exec. macOS-first (v1:
// osascript-only Chrome control; AX/input/screenshot tiers follow).
//
// Design + codex/mack borrow rationale: .ai-docs/plans/computer-use/README.md
// and docs/learnings/other-harnesses/codex-computer-use-plugin.md.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/context-labs/loopy/internal/browser"
	"github.com/context-labs/loopy/internal/computer"
	"github.com/context-labs/loopy/internal/llm"
)

// ComputerPolicy gates which apps computer_exec may drive. Installed by the
// TUI at startup from config (computer.allow / computer.deny); nil = deny
// everything (the tool refuses with guidance).
var ComputerPolicy *computer.Policy

// Approver, when set, resolves an ApprovalNeeded by asking the user (the
// TUI installs a consent prompt); nil = approval can't be granted inline.
var ComputerApprover func(app string) bool

const computerDescription = `Drive the user's Mac — control apps and the already-open Chrome via AppleScript. The ` + "`code`" + ` argument is JS-like pseudocode using the helpers below; stdout you ` + "`print(...)`" + ` comes back in the result. Start code with a one-line comment describing the step for the user in plain, non-technical language, max 60 chars (e.g. ` + "`# Opening the user's calendar`" + `) — the UI displays it as the step label.

STATE: the desktop persists (apps stay open); code variables do NOT. Batch a sub-procedure into one call.

HELPERS (each line is one helper call, ` + "`;" + `"-separated): ` +
	"`tell(app, script)`" + ` runs AppleScript against an app (escape hatch — e.g. tell("Finder", "activate")); ` +
	"`chrome_state()`" + ` returns {active:{url,title}, tabs:[...]} for the user's running Chrome — their real tabs and logins; ` +
	"`chrome_tabs()`" + ` lists every tab of every window; ` +
	"`chrome_goto(url)`" + ` navigates the active tab (URL is safety-checked); ` +
	"`chrome_new_tab(url)`" + ` opens a new tab; ` +
	"`chrome_activate(window, index)`" + ` focuses a tab from chrome_tabs(); ` +
	"`chrome_close(window, index)`" + `, ` + "`chrome_back()`" + `, ` + "`chrome_reload()`" + `; ` +
	"`chrome_js(expr)`" + ` evaluates JS in the active tab (needs Chrome's View → Developer → Allow JavaScript from Apple Events toggle — the error says so if off); ` +
	"`chrome_find(substr)`" + ` finds a tab by URL substring.

Every app access is consent-gated: the first drive of an app asks the user to approve it (or it's pre-approved in computer.allow config). The user's Chrome is THEIR browser — act on their behalf, never guess credentials, stop at login walls.`

// ComputerExec builds the computer_exec tool.
func ComputerExec() Tool {
	return Tool{
		Def: llm.NewTool("computer_exec",
			computerDescription,
			`{"type":"object","properties":{"code":{"type":"string","description":"Newline/semicolon-separated helper calls; print(...) output is returned."},"timeout":{"type":"number","description":"Seconds before the call is cancelled (default 60)."}},"required":["code"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			if !computer.Available() {
				return "", fmt.Errorf("computer_exec is macOS-only for now — browser_exec drives browsers on any platform")
			}
			var a struct {
				Code    string  `json:"code"`
				Timeout float64 `json:"timeout"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Code) == "" {
				return "", fmt.Errorf("no code provided — e.g. print(chrome_state())")
			}
			if a.Timeout <= 0 {
				a.Timeout = 60
			}
			ctx, cancel := context.WithTimeout(ctx, secondsDuration(a.Timeout))
			defer cancel()
			return runComputerCode(ctx, a.Code)
		},
	}
}

func runComputerCode(ctx context.Context, code string) (string, error) {
	prog, err := parseHelperProgram(code)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, st := range prog {
		res, err := execComputerStmt(ctx, st)
		if err != nil {
			return out.String(), fmt.Errorf("%s: %w", st, err)
		}
		if res != "" {
			fmt.Fprintln(&out, res)
		}
	}
	return out.String(), nil
}

// gateApp enforces the per-app policy: config allow/deny, then the
// in-session consent prompt (Approver). Returns nil when allowed.
func gateApp(app string) error {
	if ComputerPolicy == nil {
		return fmt.Errorf("computer-use has no policy installed (computer.allow in config, or the TUI consent prompt)")
	}
	err := ComputerPolicy.Check(app)
	if err == nil {
		return nil
	}
	need, ok := err.(*computer.ApprovalNeeded)
	if !ok {
		return err
	}
	if ComputerApprover != nil && ComputerApprover(need.App) {
		ComputerPolicy.Approve(need.App)
		return nil
	}
	return err
}

func execComputerStmt(ctx context.Context, st helperStmt) (string, error) {
	argStr := func(i int) (string, error) {
		if i >= len(st.args) {
			return "", fmt.Errorf("%s: missing arg %d", st.name, i+1)
		}
		switch v := st.args[i].(type) {
		case string:
			return v, nil
		case float64:
			return fmt.Sprintf("%v", v), nil
		default:
			data, _ := json.Marshal(v)
			return string(data), nil
		}
	}
	argNum := func(i int) (int, error) {
		if i >= len(st.args) {
			return 0, fmt.Errorf("%s: missing arg %d", st.name, i+1)
		}
		f, ok := st.args[i].(float64)
		if !ok {
			return 0, fmt.Errorf("%s: arg %d must be a number", st.name, i+1)
		}
		return int(f), nil
	}

	switch st.name {
	case "print":
		switch a := st.args[0].(type) {
		case helperStmt:
			return execComputerStmt(ctx, a)
		case string:
			return a, nil
		default:
			data, _ := json.Marshal(a)
			return string(data), nil
		}
	case "tell":
		app, err := argStr(0)
		if err != nil {
			return "", err
		}
		script, err := argStr(1)
		if err != nil {
			return "", err
		}
		if err := gateApp(app); err != nil {
			return "", err
		}
		return computer.Tell(app, script)
	case "chrome_state":
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		return computer.ChromeState()
	case "chrome_tabs":
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		tabs, err := computer.ChromeTabs()
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(tabs)
		return string(data), nil
	case "chrome_goto", "chrome_new_tab":
		url, err := argStr(0)
		if err != nil {
			return "", err
		}
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		if err := browser.CheckURL(ctx, url); err != nil {
			return "", err
		}
		if st.name == "chrome_goto" {
			return "", computer.ChromeGoto(url)
		}
		return "", computer.ChromeNewTab(url)
	case "chrome_activate", "chrome_close":
		w, err := argNum(0)
		if err != nil {
			return "", err
		}
		i, err := argNum(1)
		if err != nil {
			return "", err
		}
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		if st.name == "chrome_activate" {
			return "", computer.ChromeActivateTab(w, i)
		}
		return "", computer.ChromeCloseTab(w, i)
	case "chrome_back":
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		return "", computer.ChromeBack()
	case "chrome_reload":
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		return "", computer.ChromeReload()
	case "chrome_js":
		js, err := argStr(0)
		if err != nil {
			return "", err
		}
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		return computer.ChromeJS(js)
	case "chrome_find":
		sub, err := argStr(0)
		if err != nil {
			return "", err
		}
		if err := gateApp("Google Chrome"); err != nil {
			return "", err
		}
		tab, err := computer.ChromeFindTab(sub)
		if err != nil {
			return "", err
		}
		if tab == nil {
			return "null", nil
		}
		data, _ := json.Marshal(tab)
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown helper %q — see the tool description for the list", st.name)
	}
}
