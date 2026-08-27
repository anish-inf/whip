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
	return "You are a subagent inside whip, a coding agent harness. Complete the task you are given using your tools (bash, read, write, edit), then reply with a concise final report — that report is the only thing the caller sees, so include every finding or result that matters. Do not ask questions; make reasonable assumptions. The caller (or the user) may send you additional guidance mid-task as user messages — fold it into the work.\n\nCurrent working directory: " + wd
}

// SubModel is a resolved subagent route: which client/model a subagent runs
// on. The zero value means "unset" (fall through to the next default).
type SubModel struct {
	Client       *llm.Client
	Model        string // model id sent to the API
	ContextLimit int    // provider-advertised context window (0 = unknown)
	MaxTokens    int    // output cap (0 = inherit the parent's)
}

// newSub builds a fresh subagent. Route precedence: the explicit override o
// (a per-task model pick) → the agent's TaskDefault (config taskModel) → the
// parent's own client and model.
func (a *Agent) newSub(o SubModel) *Agent {
	if o.Client == nil {
		o = a.TaskDefault
	}
	if o.Client == nil {
		o = SubModel{Client: a.Client, Model: a.Model, ContextLimit: a.ContextLimit}
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = a.MaxTokens
	}
	// Own client copy, never a shared pointer (parent's or TaskDefault's):
	// Turn writes Client.OnRetry per call, so a shared struct races when two
	// agents stream concurrently. Shallow copy is safe — the embedded
	// *http.Client is concurrency-safe and stays shared.
	c := *o.Client
	o.Client = &c
	sub := New(o.Client, o.Model, o.MaxTokens, subagentPrompt())
	sub.Effort = a.Effort
	sub.ContextLimit = o.ContextLimit
	sub.Tools = tools.All()
	return sub
}

// resolveSub turns the task tool's optional model/provider args into a
// SubModel. Empty model means "use the defaults" (zero SubModel).
func (a *Agent) resolveSub(model, provider string) (SubModel, error) {
	if model == "" {
		return SubModel{}, nil
	}
	if a.ResolveModel == nil {
		return SubModel{}, fmt.Errorf("per-task model overrides are not available in this session")
	}
	return a.ResolveModel(model, provider)
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
		Def: llm.NewTool("subagent",
			"Launch a subagent to handle a self-contained task with its own fresh context. It has the same tools as you (bash, read, write, edit) and returns only its final report. Use it for context-heavy exploration or work that can be described completely up front. Set background=true to run it concurrently while you keep working — you'll be notified with the report automatically when it finishes; do NOT poll or sleep waiting for it (subagent_steer can send it mid-course corrections). Subagents run on a cheap fast model by default; set model only when the task needs a specific/stronger one.",
			`{"type":"object","properties":{"description":{"type":"string","description":"Short 3-8 word summary of the task"},"prompt":{"type":"string","description":"Complete instructions for the subagent; it cannot ask follow-up questions"},"background":{"type":"boolean","description":"Run concurrently and get notified on completion (default false = block until done)"},"model":{"type":"string","description":"Optional model to run the subagent on (a configured model name or catalog id); omit for the default"},"provider":{"type":"string","description":"Optional provider for the model override; omit for its default routing"}},"required":["prompt"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Description string `json:"description"`
				Prompt      string `json:"prompt"`
				Background  bool   `json:"background"`
				Model       string `json:"model"`
				Provider    string `json:"provider"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			desc := a.Description
			if desc == "" {
				desc = "subagent task"
			}
			o, err := parent.resolveSub(a.Model, a.Provider)
			if err != nil {
				return "Error: model override: " + err.Error(), nil
			}
			if a.Background {
				t := parent.StartBackground(desc, a.Prompt, o)
				return fmt.Sprintf("Started background subagent %s: %s. Keep working on something else; the report will arrive as a message when it finishes. Do not poll for it.", t.ID, desc), nil
			}
			sub := parent.newSub(o)
			// roll the subagent's spend into the parent's session totals
			report, err := sub.Turn(ctx, a.Prompt, Events{OnUsage: parent.AddUsage})
			return report, err
		},
	}
}

// taskSteerTool lets the model send mid-course guidance into a RUNNING
// background subagent — the same Steer primitive the parent's own queue uses,
// pointed at the child: the message lands at the child's next loop boundary.
func taskSteerTool(parent *Agent) tools.Tool {
	return tools.Tool{
		Def: llm.NewTool("subagent_steer",
			"Send additional guidance to a running background subagent (started with the subagent tool). The message is injected into the subagent's conversation at its next loop boundary. Use it to correct course or add information; it cannot make a finished subagent resume.",
			`{"type":"object","properties":{"id":{"type":"string","description":"The subagent id, e.g. sub-3"},"message":{"type":"string","description":"The guidance to inject"}},"required":["id","message"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				ID      string `json:"id"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			if err := parent.SteerTask(a.ID, a.Message); err != nil {
				return "Error: " + err.Error(), nil
			}
			return fmt.Sprintf("Steered %s; the guidance lands at the subagent's next loop boundary.", a.ID), nil
		},
	}
}

// SteerTask injects guidance into a running background subagent's
// conversation (the child's own pendingSteer queue — no new synchronization).
func (a *Agent) SteerTask(id, text string) error {
	r := a.Tasks()
	t, ok := r.Get(id)
	if !ok {
		return fmt.Errorf("unknown subagent %q", id)
	}
	if t.Status != TaskRunning {
		return fmt.Errorf("subagent %s already %s — steering only reaches a running subagent", id, t.Status)
	}
	if t.sub == nil {
		return fmt.Errorf("subagent %s is not live", id)
	}
	t.sub.Steer(text)
	return nil
}

// FollowupTask runs one more turn on a SETTLED background task's retained
// subagent — its full conversation context is preserved, so the task pane
// works like a chat session after the report lands. The task's registry
// lifecycle is untouched (status, report, and the already-closed Done channel
// stay as they settled); follow-up turns are live-only and die with the
// process. Usage rolls into the parent's session totals.
func (a *Agent) FollowupTask(ctx context.Context, id, text string, ev Events) (string, error) {
	r := a.Tasks()
	t, ok := r.Get(id)
	if !ok {
		return "", fmt.Errorf("unknown subagent %q", id)
	}
	if t.Status == TaskRunning {
		return "", fmt.Errorf("subagent %s is still running — steer it instead", id)
	}
	if t.sub == nil {
		return "", fmt.Errorf("subagent %s is not live (restored from a previous session)", id)
	}
	return t.sub.Turn(ctx, text, FanIn(ev, Events{OnUsage: a.AddUsage}))
}
