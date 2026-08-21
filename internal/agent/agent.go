// Package agent runs the LLM tool-use loop.
package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/abe/loopy/internal/llm"
	"github.com/abe/loopy/internal/tools"
)

// Events receives streaming callbacks during a turn. All fields are optional.
type Events struct {
	OnText      func(delta string)               // assistant text as it streams
	OnToolStart func(name string, args string)   // a tool call is about to run
	OnToolEnd   func(name string, result string) // a tool call finished
	OnSteer     func(text string)                // a steered message was injected
}

// Agent holds one conversation.
type Agent struct {
	Client    *llm.Client
	Model     string // model id sent to the API
	MaxTokens int
	Effort    string // reasoning effort: "", "low", "medium", "high"
	Tools     []tools.Tool
	Messages  []llm.Message

	mu      sync.Mutex
	pending []string // steered user messages awaiting injection
}

// Steer queues a user message for injection at the next loop boundary of the
// running turn — after the in-flight response and its tool calls complete,
// never mid-generation.
func (a *Agent) Steer(text string) {
	a.mu.Lock()
	a.pending = append(a.pending, text)
	a.mu.Unlock()
}

func (a *Agent) drainPending() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pending
	a.pending = nil
	return p
}

func New(client *llm.Client, model string, maxTokens int, systemPrompt string) *Agent {
	a := &Agent{
		Client:    client,
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  []llm.Message{{Role: "system", Content: systemPrompt}},
	}
	a.Tools = append(tools.All(), taskTool(a))
	return a
}

// Turn sends user input and loops until the model stops calling tools.
// It returns the final assistant text.
func (a *Agent) Turn(ctx context.Context, input string, ev Events) (string, error) {
	a.Messages = append(a.Messages, llm.Message{Role: "user", Content: input})
	for {
		msg, err := a.Client.Stream(ctx, llm.Request{
			Model:           a.Model,
			Messages:        a.Messages,
			Tools:           tools.Defs(a.Tools),
			MaxTokens:       a.MaxTokens,
			ReasoningEffort: a.Effort,
		}, ev.OnText)
		if err != nil {
			return "", err
		}
		a.Messages = append(a.Messages, msg)
		for _, tc := range msg.ToolCalls {
			if ev.OnToolStart != nil {
				ev.OnToolStart(tc.Function.Name, tc.Function.Arguments)
			}
			result := tools.Execute(ctx, a.Tools, tc.Function.Name, json.RawMessage(tc.Function.Arguments))
			if ev.OnToolEnd != nil {
				ev.OnToolEnd(tc.Function.Name, result)
			}
			a.Messages = append(a.Messages, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
		}
		steered := a.drainPending()
		for _, s := range steered {
			if ev.OnSteer != nil {
				ev.OnSteer(s)
			}
			a.Messages = append(a.Messages, llm.Message{Role: "user", Content: s})
		}
		if len(msg.ToolCalls) == 0 && len(steered) == 0 {
			return msg.Content, nil
		}
	}
}
