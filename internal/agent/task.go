package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/context-labs/loopy/internal/llm"
	"github.com/context-labs/loopy/internal/tools"
)

func subagentPrompt() string {
	wd, _ := os.Getwd()
	return fmt.Sprintf(`You are a subagent inside loopy, a coding agent harness. Complete the task you are given using your tools (bash, read, write, edit), then reply with a concise final report — that report is the only thing the caller sees, so include every finding or result that matters. Do not ask questions; make reasonable assumptions.

Current working directory: %s`, wd)
}

// taskTool lets the model delegate a self-contained task to a fresh subagent.
// The subagent gets the same tool set minus task itself — no recursion.
func taskTool(parent *Agent) tools.Tool {
	return tools.Tool{
		Def: llm.NewTool("task",
			"Launch a subagent to handle a self-contained task with its own fresh context. It has the same tools as you (bash, read, write, edit) and returns only its final report. Use it for context-heavy exploration or work that can be described completely up front.",
			`{"type":"object","properties":{"description":{"type":"string","description":"Short 3-8 word summary of the task"},"prompt":{"type":"string","description":"Complete instructions for the subagent; it cannot ask follow-up questions"}},"required":["prompt"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Description string `json:"description"`
				Prompt      string `json:"prompt"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			sub := New(parent.Client, parent.Model, parent.MaxTokens, subagentPrompt())
			sub.Effort = parent.Effort
			sub.Tools = tools.All()
			// roll the subagent's spend into the parent's session totals
			report, err := sub.Turn(ctx, a.Prompt, Events{OnUsage: parent.AddUsage})
			return report, err
		},
	}
}
