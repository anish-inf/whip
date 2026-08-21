// Package llm is a minimal streaming client for OpenAI-compatible chat completions APIs.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one chat message. Content is a string; ToolCalls set on assistant
// messages, ToolCallID on role "tool" results.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	// Authored marks a user message the human actually typed and submitted, as
	// opposed to one loopy injected on their behalf (steered background-task
	// results, goal-check continuations). Internal only — never sent to the
	// provider. Used so input-history recall cycles only real submissions.
	Authored bool `json:"authored,omitempty"`
}

// stripAuthored returns a copy of msgs with the internal Authored marker
// cleared — it's loopy-local bookkeeping (input-history recall) and must never
// reach the provider. It copies because req.Messages typically aliases the
// caller's conversation slice, which must keep the flag for storage/recall.
func stripAuthored(msgs []Message) []Message {
	out := make([]Message, len(msgs))
	copy(out, msgs)
	for i := range out {
		out[i].Authored = false
	}
	return out
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is a tool definition advertised to the model.
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// NewTool builds a Tool from name, description, and a JSON Schema string.
func NewTool(name, desc, schema string) Tool {
	t := Tool{Type: "function"}
	t.Function.Name = name
	t.Function.Description = desc
	t.Function.Parameters = json.RawMessage(schema)
	return t
}

// Client talks to one provider endpoint.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
	}
}

// Request is a chat completions request.
type Request struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Tools           []Tool    `json:"tools,omitempty"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Stream          bool      `json:"stream"`
	StreamOptions   *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

// Usage is the token accounting the provider reports for one request
// (prompt = input, completion = output). CachedTokens counts the slice of
// the prompt served from the provider's prompt cache. Providers that omit
// usage leave all fields zero — the session totals just skip those calls.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// PromptTokensDetails nests the cache hit count (OpenAI-compatible).
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// Cached is the prompt-token count served from cache (0 when unreported).
func (u Usage) Cached() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// Chunk delta payload from the SSE stream.
type delta struct {
	Content string `json:"content"`
	// ReasoningContent carries thinking tokens on reasoning models (deepseek,
	// grok, kimi, claude all emit it; claude also nests it in thinking_blocks).
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type chunk struct {
	Choices []struct {
		Delta        delta  `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error"`
	Usage *Usage    `json:"usage"`
}

type apiError struct {
	Message string `json:"message"`
}

// HTTPError is returned when the API responds with a non-2xx status. Body is
// the ( capped ) response payload so callers can match against provider-
// specific reason strings; Error() keeps the "<status>: <body>" shape the
// existing tests assert ( e.g. "... 401 ..." ).
type HTTPError struct {
	Status string
	Body   string
}

func (e *HTTPError) Error() string { return e.Status + ": " + e.Body }

// contextLimitMarkers are substrings providers put in the error body when the
// conversation has grown past the model's context window. Anthropic and the
// OpenAI-compatible routers it backs onto both surface one of these.
var contextLimitMarkers = []string{
	"context_length_exceeded", // Anthropic / OpenAI error.code
	"maximum context length",  // OpenAI plain-text message
	"prompt_too_long",         // Anthropic error.type variant
}

// IsContextLimit reports whether err is a context-length-exceeded style
// error: an HTTP 4xx whose body names context length, or the older "context
// window"-free provider error code. It is the signal to auto-compact.
func IsContextLimit(err error) bool {
	if err == nil {
		return false
	}
	var he *HTTPError
	if errors.As(err, &he) {
		if strings.HasPrefix(he.Status, "400") || strings.HasPrefix(he.Status, "413") {
			b := strings.ToLower(he.Body)
			for _, m := range contextLimitMarkers {
				if strings.Contains(b, m) {
					return true
				}
			}
		}
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range contextLimitMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// ModelInfo is one entry from the provider's GET /models list. Fields beyond
// the OpenAI spec (context_length, reasoning_efforts) are omitted by APIs
// that don't supply them.
type ModelInfo struct {
	ID                  string   `json:"id"`
	ContextLength       int      `json:"context_length,omitempty"`
	MaxCompletionTokens int      `json:"max_completion_tokens,omitempty"`
	ReasoningEfforts    []string `json:"reasoning_efforts,omitempty"`
}

// Models fetches GET /models from the provider.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	hr, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &HTTPError{Status: resp.Status, Body: strings.TrimSpace(string(b))}
	}
	var list struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Data, nil
}

// Stream sends the request and invokes onText for each content delta and
// onThink for each reasoning_content delta (both may be nil). It returns the
// final assistant message (with any accumulated tool calls) plus the usage
// the provider reports on the terminal chunk (stream_options:include_usage).
func (c *Client) Stream(ctx context.Context, req Request, onText, onThink func(string)) (Message, Usage, error) {
	req.Stream = true
	req.StreamOptions = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}
	req.Messages = stripAuthored(req.Messages)
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Message{}, Usage{}, &HTTPError{Status: resp.Status, Body: strings.TrimSpace(string(b))}
	}

	msg := Message{Role: "assistant"}
	var usage Usage      // from the terminal chunk (include_usage); zero if omitted
	var calls []ToolCall // indexed by stream tool_call index
	finish := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		if ch.Error != nil {
			return Message{}, usage, fmt.Errorf("api error: %s", ch.Error.Message)
		}
		if ch.Usage != nil {
			usage = *ch.Usage // the terminal usage chunk carries empty choices
		}
		if len(ch.Choices) == 0 {
			continue
		}
		if fr := ch.Choices[0].FinishReason; fr != "" {
			finish = fr
		}
		d := ch.Choices[0].Delta
		if d.ReasoningContent != "" {
			if onThink != nil {
				onThink(d.ReasoningContent)
			}
		}
		if d.Content != "" {
			msg.Content += d.Content
			if onText != nil {
				onText(d.Content)
			}
		}
		for _, tc := range d.ToolCalls {
			for len(calls) <= tc.Index {
				calls = append(calls, ToolCall{Type: "function"})
			}
			cur := &calls[tc.Index]
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Function.Name += tc.Function.Name
			}
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, usage, err
	}
	// Never execute tool calls from a max_tokens-truncated response: the
	// streamed JSON arguments may be silently incomplete.
	if finish == "length" && len(calls) > 0 {
		calls = nil
		msg.Content += "\n[response truncated by max_tokens; tool calls discarded]"
	}
	msg.ToolCalls = calls
	return msg, usage, nil
}

// Complete sends a non-streaming chat request and returns the assistant text
// content plus the reported usage. It's used internally by compaction's
// summary call, where streaming would just add UI noise for a one-shot
// synthesis.
func (c *Client) Complete(ctx context.Context, req Request) (string, Usage, error) {
	req.Stream = false
	req.Messages = stripAuthored(req.Messages)
	body, err := json.Marshal(req)
	if err != nil {
		return "", Usage{}, err
	}
	hr, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", Usage{}, &HTTPError{Status: resp.Status, Body: strings.TrimSpace(string(b))}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, err
	}
	if len(out.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("no choices in completion response")
	}
	var usage Usage
	if out.Usage != nil {
		usage = *out.Usage
	}
	return out.Choices[0].Message.Content, usage, nil
}
