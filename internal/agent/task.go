package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/tools"
)

func subagentPrompt() string {
	wd, _ := os.Getwd()
	return "You are a subagent inside whip, a coding agent harness. Complete the task you are given using your tools (bash, read, write, edit), then reply with a concise final report — that report is the only thing the caller sees, so include every finding or result that matters. Do not ask questions; make reasonable assumptions.\n\nCurrent working directory: " + wd
}

// taskTool lets the model delegate a self-contained task to a fresh subagent.
// The subagent gets the same tool set minus task itself — no recursion.
//
// background=true is the channel-native novelty: instead of blocking the turn,
// the subagent runs concurrently and its report arrives later as a steered
// message (the task registry's Done channel fans completion back into Steer).
// The parent keeps working on non-overlapping tasks meanwhile.
func taskTool(parent *Agent) tools.Tool {
	return tools.Tool{
		Def: llm.NewTool("task",
			"Launch a subagent to handle a self-contained task with its own fresh context. It has the same tools as you (bash, read, write, edit) and returns only its final report. Use it for context-heavy exploration or work that can be described completely up front. Set background=true to run it concurrently while you keep working — you'll be notified with the report automatically when it finishes; do NOT poll or sleep waiting for it.",
			`{"type":"object","properties":{"description":{"type":"string","description":"Short 3-8 word summary of the task"},"prompt":{"type":"string","description":"Complete instructions for the subagent; it cannot ask follow-up questions"},"background":{"type":"boolean","description":"Run concurrently and get notified on completion (default false = block until done)"}},"required":["prompt"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Description string `json:"description"`
				Prompt      string `json:"prompt"`
				Background  bool   `json:"background"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			desc := a.Description
			if desc == "" {
				desc = "subagent task"
			}
			if a.Background {
				t := parent.StartBackground(ctx, desc, a.Prompt)
				return fmt.Sprintf("Started background task %s: %s. Keep working on something else; the report will arrive as a message when it finishes. Do not poll for it.", t.ID, desc), nil
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
