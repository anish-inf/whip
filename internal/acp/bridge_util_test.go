package acp

import (
	"os"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/tools"
)

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// llmTool builds a bare tool def for test-only tools.
func llmTool(name string) llm.Tool {
	return llm.NewTool(name, name, `{"type":"object","properties":{}}`)
}

// checkGateForTest invokes the installed tools.Gate exactly as the real
// gated tools do (bash/write/edit call the unexported checkGate).
func checkGateForTest(tool, command string) string {
	if tools.Gate == nil {
		return ""
	}
	decision, redirect := tools.Gate(tools.GateRequest{
		Tool:    tool,
		Command: command,
		Rule:    tools.CommandRule(command),
	})
	switch decision {
	case tools.GateReject:
		if redirect == "" {
			redirect = "the user rejected this action"
		}
		return "Permission denied: " + redirect
	default:
		return ""
	}
}

type errStringT string

func (e errStringT) Error() string { return string(e) }

func errString(s string) error { return errStringT(s) }
