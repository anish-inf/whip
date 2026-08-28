package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/context-labs/whip/internal/llm"
)

// Context decay keeps old tool output from polluting the prompt while
// preserving the prefix cache. The invariant: a "hot window" of the newest
// ~decayHotWindow tokens (measured from the back of the message list) is
// never mutated, so the pruned prefix stays byte-identical across turns and
// the provider cache keeps hitting; only the window itself is cold each turn.
//
// The pass runs once per Turn (before round 1), not per round: within-turn
// tool output is load-bearing and per-round mutation would churn the cache
// inside the tool loop. Three mechanisms, all deterministic — no LLM call:
//
//  1. Superseded reads: a read whose file has since been re-read or written
//     collapses to a one-line pointer ("superseded by newer read at line N").
//     The model never needs two vintages of the same file; it follows the
//     newest. (Write-invalidates-read counts here too.)
//  2. Age decay: tool results that were big at ingestion (>decayMinBytes,
//     ~2k tokens) and now sit older than the hot window collapse to a
//     placeholder naming the command/path, the original byte size, and the
//     spill path when one exists. Small results (errors, short greps — the
//     semantic glue of the conversation) stay inline forever.
//  3. Exclusions: assistant messages are never rewritten (reasoning chains
//     matter), and anything inside the hot window is untouched.
//
// Placeholders are HTML-comment-style so the model reads them as metadata,
// not content to quote.

const (
	// decayHotWindow is the newest slice of context (approx tokens, len/4)
	// left byte-stable for cache reuse. Anything older may be pruned.
	decayHotWindow = 24_000
	// decayMinBytes is the size at ingestion (approx 2k tokens) above which a
	// tool result is eligible for age decay; smaller results never decay.
	decayMinBytes = 8_000
)

// decayedMarker prefixes every placeholder so later passes (and tests) can
// recognize an already-decayed message cheaply.
const decayedMarker = "⟨"

// decay applies superseded-read replacement and age decay to a.Messages
// outside the hot window, and returns the indices of messages it rewrote so
// the caller can re-persist them (a.Save with from=0 rewrites the whole
// prefix — INSERT OR REPLACE makes it cheap).
//
// Returns the number of messages rewritten.
func (a *Agent) decay() int {
	a.msgsMu.Lock()
	defer a.msgsMu.Unlock()

	boundary := hotBoundary(a.Messages)
	rewritten := 0

	// Pass 1: superseded reads — applied anywhere in history, not just past
	// the window: the replacement is idempotent (rewritten content is skipped
	// on later passes), so after the one-time rewrite the prefix stays
	// byte-stable and cache-friendly anyway. Walk backward (newest→oldest)
	// tracking, per path, the newest read/write position; an older read of
	// the same path collapses to a pointer at the newer evidence.
	latest := map[string]sighting{}
	type readRef struct {
		idx  int
		path string
	}
	var reads []readRef

	// Count tool calls per path as we go so the placeholder can say "at line N"
	// — the line count lives in the read result itself ("<n>\t..." numbered).
	for i := len(a.Messages) - 1; i > 0; i-- {
		m := a.Messages[i]
		if m.Role != "tool" {
			continue
		}
		switch m.Name {
		case "read":
			p := readPathFromCall(a.Messages, i)
			if p == "" || strings.HasPrefix(m.Content, decayedMarker) {
				continue
			}
			reads = append(reads, readRef{i, p})
			if _, ok := latest[p]; !ok {
				latest[p] = sighting{idx: i, lines: readLineCount(m.Content)}
			}
		case "write", "edit":
			p := writePathFromCall(a.Messages, i)
			if p == "" {
				continue
			}
			if _, ok := latest[p]; !ok {
				latest[p] = sighting{idx: i, write: true}
			}
		}
	}
	// Walking newest→oldest means latest[p] is always the newest evidence for
	// p; a read whose own index is not the newest sighting is superseded.
	for _, r := range reads {
		s := latest[r.path]
		if s.idx == r.idx {
			continue // this read IS the newest evidence
		}
		a.Messages[r.idx].Content = supersededNotice(r.path, s)
		rewritten++
	}

	// Pass 2: age decay of big tool outputs past the hot window. Pass-1
	// placeholders are already short, but skip them explicitly so a read that
	// was just superseded isn't counted twice.
	for i := range boundary {
		m := &a.Messages[i]
		if m.Role != "tool" || len(m.Content) <= decayMinBytes || strings.HasPrefix(m.Content, decayedMarker) {
			continue
		}
		m.Content = decayNotice(*m)
		rewritten++
	}
	return rewritten
}

// hotBoundary returns the index in msgs where the hot window begins: the
// newest position whose suffix (msgs[pos:]) sums to at most decayHotWindow
// approx tokens. Index 0 (system prompt) is never inside the window but is
// also never decayed (it is not a tool message).
func hotBoundary(msgs []llm.Message) int {
	budget := decayHotWindow
	for i := len(msgs) - 1; i > 0; i-- {
		t := msgTokens(msgs[i])
		if t > budget {
			return i + 1
		}
		budget -= t
	}
	return 1 // the whole conversation fits in the window
}

// msgTokens approximates a message's token footprint (len/4, matching the
// compaction threshold's heuristic).
func msgTokens(m llm.Message) int {
	n := len(m.Content) + len(m.ToolCallID) + len(m.Name)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n / 4
}

// readPathFromCall finds the assistant tool_call that produced the tool
// message at i and extracts its path argument. The assistant message carrying
// the call sits somewhere before i (possibly several tool results back).
func readPathFromCall(msgs []llm.Message, i int) string {
	return toolArgFromCall(msgs, i, "path")
}

func writePathFromCall(msgs []llm.Message, i int) string {
	return toolArgFromCall(msgs, i, "path")
}

// toolArgFromCall extracts arg key from the tool_call whose id matches
// msgs[i].ToolCallID. Tool messages immediately follow their assistant
// message, so scanning back to it is short.
func toolArgFromCall(msgs []llm.Message, i int, key string) string {
	id := msgs[i].ToolCallID
	if id == "" {
		return ""
	}
	for j := i - 1; j >= 0; j-- {
		if msgs[j].Role != "assistant" {
			continue
		}
		for _, tc := range msgs[j].ToolCalls {
			if tc.ID != id {
				continue
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return ""
			}
			if v, ok := args[key].(string); ok {
				return v
			}
			return ""
		}
		return "" // the first assistant message back owns the call; id not in it
	}
	return ""
}

// readLineCount counts the numbered lines a read result rendered ("<n>\t...").
func readLineCount(content string) int {
	n := 0
	for l := range strings.Lines(content) {
		if l != "" {
			n++
		}
	}
	return n
}

// sighting records the newest evidence for a path: the message index of the
// newest read or write of it, whether that evidence is a write, and (for a
// read) how many lines it reported — so a superseded placeholder can point at
// the current vintage.
type sighting struct {
	idx   int  // message index of the newest read/write of this path
	write bool // the newest evidence is a write, not a read
	lines int  // line count the newer read reported (0 for writes)
}

// supersededNotice is the Layer-1 placeholder. A write supersedes without a
// line count ("content changed by write"); a newer read carries its line
// count so the model can re-read precisely if it truly needs the old region.
func supersededNotice(path string, s sighting) string {
	if s.write {
		return fmt.Sprintf("%sread of %s superseded — file changed by a later write/edit⟩", decayedMarker, filepath.Base(path))
	}
	return fmt.Sprintf("%sread of %s superseded by newer read (%d lines)⟩", decayedMarker, filepath.Base(path), s.lines)
}

// decayNotice is the Layer-2 placeholder: what ran, how big it was, and where
// the full output lives when a spill file exists. Turns-ago is derived from
// authored user messages between the result and the tail.
func decayNotice(m llm.Message) string {
	what := m.Name
	if m.Name == "bash" {
		if cmd := bashCommandPreview(m.Content); cmd != "" {
			what = fmt.Sprintf("bash %q", cmd)
		}
	}
	spill := spillPathOf(m.Content)
	size := fmt.Sprintf("%dk bytes", len(m.Content)/1024)
	if spill != "" {
		return fmt.Sprintf("%s%s output, %s — full output: %s⟩", decayedMarker, what, size, spill)
	}
	return fmt.Sprintf("%s%s output, %s⟩", decayedMarker, what, size)
}

// bashCommandPreview recovers the command's first words from a bash result.
// whip doesn't store the command on the tool message, so this is best-effort:
// the first line of output is usually the command echo when bashrun markers
// are on; otherwise the tool name alone carries the placeholder.
func bashCommandPreview(content string) string {
	first, _, _ := strings.Cut(content, "\n")
	first = strings.TrimSpace(first)
	if len(first) > 60 || first == "" {
		return ""
	}
	return first
}

// spillPathOf extracts the "full output (N bytes): /path" pointer the
// truncation markers carry ("[full output (N bytes): /path]" from bash's
// legacy marker, "— full output (N bytes): /path] ..." from middleElide), so
// the decayed placeholder keeps the recovery path.
func spillPathOf(content string) string {
	i := strings.LastIndex(content, "full output (")
	if i < 0 {
		return ""
	}
	j := strings.Index(content[i:], "): ")
	if j < 0 {
		return ""
	}
	start := i + j + 3
	// the path runs to the next "]" or newline, whichever comes first
	end := strings.IndexAny(content[start:], "]\n")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(content[start : start+end])
}
