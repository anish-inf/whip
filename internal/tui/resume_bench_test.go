package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/llm"
)

// benchTranscript builds a realistic resumed conversation: n exchanges, each
// with a user message, an assistant answer (markdown), and a tool call.
func benchTranscript(n int) []llm.Message {
	msgs := make([]llm.Message, 0, n*3)
	for i := range n {
		msgs = append(msgs,
			llm.Message{Role: "user", Content: fmt.Sprintf("question %d: how do I do the thing?", i)},
			llm.Message{Role: "assistant", Content: strings.Repeat("Here is **some** `answer` with text. ", 20)},
			func() llm.Message {
				var tc llm.ToolCall
				tc.Function.Name = "bash"
				tc.Function.Arguments = fmt.Sprintf(`{"command":"ls %d"}`, i)
				return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{tc}}
			}(),
		)
	}
	return msgs
}

// BenchmarkSeedTranscript measures resume: seeding a stored conversation into
// the transcript. With per-block render caching this is one O(n) render pass.
func BenchmarkSeedTranscript(b *testing.B) {
	msgs := benchTranscript(200)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		m := compactCmdModel()
		m.Update(mkWinSize(120, 40))
		m.seedTranscript(msgs, 1)
	}
}

// BenchmarkAppendStream measures the streaming hot path: appending assistant
// segments to an already-long transcript. Cached renders keep this O(1) per
// append instead of O(transcript).
func BenchmarkAppendStream(b *testing.B) {
	m := compactCmdModel()
	m.Update(mkWinSize(120, 40))
	m.seedTranscript(benchTranscript(200), 1)
	b.ResetTimer()
	b.ReportAllocs()
	for i := range b.N {
		m.append(fmt.Sprintf("streamed line %d", i))
	}
}
