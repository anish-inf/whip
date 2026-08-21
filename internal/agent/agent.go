// Package agent runs the LLM tool-use loop.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/context-labs/loopy/internal/llm"
	"github.com/context-labs/loopy/internal/tools"
)

// Events receives streaming callbacks during a turn. All fields are optional.
type Events struct {
	OnText      func(delta string)               // assistant text as it streams
	OnThink     func(delta string)               // reasoning/thinking tokens as they stream
	OnToolStart func(name string, args string)   // a tool call is about to run
	OnToolEnd   func(name string, result string) // a tool call finished
	OnSteer     func(text string)                // a steered message was injected
	OnCompact   func(took, kept int)             // context was auto-compacted (messages removed/kept)
}

// Agent holds one conversation.
type Agent struct {
	Client    *llm.Client
	Model     string // model id sent to the API
	ModelName string // config model name (may differ from Model via id mapping)
	Provider  string // config provider name
	MaxTokens int
	Effort    string // reasoning effort: "" = parameter omitted from requests
	Tools     []tools.Tool
	Messages  []llm.Message

	// ContextLimit is the model's context window in tokens, as advertised by
	// the provider's GET /models (0 when unadvertised — proactive compaction
	// is then disabled and only the reactive context-limit retry applies).
	ContextLimit int
	// CompactClient and CompactModel run the compaction summary; nil/"" uses
	// the conversation's own client and model.
	CompactClient *llm.Client
	CompactModel  string

	mu        sync.Mutex
	pending   []string // steered user messages awaiting injection
	compacted bool     // a compaction already happened this turn — don't retry-loop
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
// It returns the final assistant text. When the estimated conversation size
// reaches 90% of the provider-advertised context limit, Turn compacts
// proactively before the next request; if the provider still rejects the
// request because the conversation exceeded its context window, Turn
// auto-compacts (summarizing old turns) and retries once before surfacing
// the error to the caller.
func (a *Agent) Turn(ctx context.Context, input string, ev Events) (string, error) {
	a.Messages = append(a.Messages, llm.Message{Role: "user", Content: input})
	for {
		if err := a.maybeCompact(ctx, ev); err != nil {
			return "", err
		}
		msg, err := a.Client.Stream(ctx, llm.Request{
			Model:           a.Model,
			Messages:        a.Messages,
			Tools:           tools.Defs(a.Tools),
			MaxTokens:       a.MaxTokens,
			ReasoningEffort: a.Effort,
		}, ev.OnText, ev.OnThink)
		if err != nil {
			if !a.compacted && llm.IsContextLimit(err) && ctx.Err() == nil {
				a.compacted = true
				took := len(a.Messages)
				if cerr := a.compact(ctx); cerr != nil {
					// restore the guard on hard errors so a manual /compact
					// can still attempt a compaction for the next turn
					a.compacted = false
					return "", cerr
				}
				if ev.OnCompact != nil {
					ev.OnCompact(took-len(a.Messages), len(a.Messages))
				}
				continue // retry the (now-smaller) request
			}
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
			a.compacted = false // reset for the next Turn
			return msg.Content, nil
		}
	}
}

// compactKeepBack counts assistant turns (and any tool results they pulled in)
// preserved verbatim at the tail of the history. Keeping recent context means
// any in-flight task the model is working on keeps its tool results in view,
// and we never leave an orphaned tool_call whose result the summary dropped.
const compactKeepBack = 6

// compactThreshold is the fraction of the provider-advertised context window
// at which Turn compacts proactively. 90% leaves headroom for the response
// and for the estimate's inaccuracy on non-English content.
const compactThreshold = 0.9

// maybeCompact folds old turns into a summary once the estimated token count
// crosses compactThreshold of ContextLimit. It no-ops when the provider
// didn't advertise a limit (ContextLimit == 0) — the reactive context-limit
// retry in Turn still covers that case.
func (a *Agent) maybeCompact(ctx context.Context, ev Events) error {
	if a.ContextLimit == 0 || EstimateTokens(a.Messages) < int(compactThreshold*float64(a.ContextLimit)) {
		return nil
	}
	took := len(a.Messages)
	if err := a.compact(ctx); err != nil {
		if err.Error() == "not enough history to compact" {
			return nil // too little history to fold; rely on the reactive retry
		}
		return err
	}
	if ev.OnCompact != nil {
		ev.OnCompact(took-len(a.Messages), len(a.Messages))
	}
	return nil
}

// EstimateTokens approximates the token count of a conversation. No real
// tokenizer is wired in, so this uses the common ~4 chars/token heuristic for
// message content and tool-call arguments, plus a small per-message overhead
// for roles and tool-call framing. It intentionally overestimates slightly:
// false positives just compact a little early, false negatives cost a
// rejected request.
func EstimateTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 + (len(m.Content)+3)/4
		for _, tc := range m.ToolCalls {
			total += 8 + (len(tc.Function.Name)+len(tc.Function.Arguments)+3)/4
		}
	}
	return total
}

// compact replaces old turns with an LLM-generated summary, keeping the
// system prompt and the last compactKeepBack (ish) messages so recent tool
// results and any in-flight assistant action stay intact. It runs a single
// non-streaming completion — on CompactClient/CompactModel when set, else
// on the conversation's own client and model — and stores the summary as a
// system-role message (it must carry no tool_call IDs that the kept tail
// would orphan).
func (a *Agent) compact(ctx context.Context) error {
	if len(a.Messages) <= compactKeepBack+2 { // system + ≥1 user + tail: nothing to fold
		return errors.New("not enough history to compact")
	}
	const sysIdx = 0
	sysPrompt := a.Messages[sysIdx]
	tailStart := len(a.Messages) - compactKeepBack
	if tailStart <= sysIdx+1 {
		tailStart = sysIdx + 2 // never drop the first user message entirely
	}
	tail := a.Messages[tailStart:]
	// orphan safety: a kept tail that begins with role "tool" references a
	// tool_call the summary would erase. Walk backwards to the owning
	// assistant message so both stay or both go.
	for len(tail) > 4 && tail[0].Role == "tool" {
		tail = a.Messages[tailStart-1:]
		tailStart--
	}
	history := a.Messages[sysIdx+1 : tailStart]
	summaryPrompt := buildSummaryPrompt(history)
	cli, mdl := a.CompactClient, a.CompactModel
	if cli == nil {
		cli = a.Client
	}
	if mdl == "" {
		mdl = a.Model
	}
	summary, err := cli.Complete(ctx, llm.Request{
		Model:     mdl,
		MaxTokens: 1024,
		Messages: []llm.Message{
			sysPrompt,
			{Role: "user", Content: summaryPrompt},
		},
	})
	if err != nil {
		return fmt.Errorf("compaction summary failed: %w", err)
	}
	kept := append([]llm.Message(nil), tail...)
	a.Messages = append(append([]llm.Message{}, sysPrompt,
		llm.Message{Role: "system", Content: "Summary of the conversation so far:\n\n" + strings.TrimSpace(summary)},
	), kept...)
	return nil
}

// buildSummaryPrompt renders the unsummarized turns as a transcript the model
// folds into a concise digest. Tool results are truncated so a giant file
// read doesn't push the summary request over the window we just overflowed.
func buildSummaryPrompt(msgs []llm.Message) string {
	var b strings.Builder
	b.WriteString("Summarize the following conversation between the user and the assistant. ")
	b.WriteString("Capture the user's intent, decisions made, work completed, files touched, ")
	b.WriteString("and any open task the assistant is mid-way through. ")
	b.WriteString("Be concise (a few short paragraphs at most); use bullet points for code/files. ")
	b.WriteString("Do not include verbatim tool output. End with a single line: ")
	b.WriteString("\"Open task: <what the assistant was doing last, or none>\".\n\n")
	b.WriteString("---\n\n")
	for _, m := range msgs {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "user: %s\n", truncateField(m.Content, 2000))
		case "assistant":
			if c := strings.TrimSpace(m.Content); c != "" {
				fmt.Fprintf(&b, "assistant: %s\n", truncateField(c, 2000))
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "assistant called %s(%s)\n", tc.Function.Name, truncateField(tc.Function.Arguments, 500))
			}
		case "tool":
			fmt.Fprintf(&b, "tool result: %s\n", truncateField(m.Content, 500))
		}
	}
	b.WriteString("\n---\n\nWrite the summary now.")
	return b.String()
}

func truncateField(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// ManualCompact lets the TUI's /compact command compact on demand. It calls
// OnCompact and reports whether compaction ran (false when there's too
// little history). It is safe to call while a turn is not in flight.
func (a *Agent) ManualCompact(ctx context.Context, ev Events) error {
	if err := a.compact(ctx); err != nil {
		return err
	}
	if ev.OnCompact != nil {
		ev.OnCompact(0, len(a.Messages))
	}
	return nil
}
