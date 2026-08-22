package mcp

import "strings"

// ringBuffer is a fixed-capacity byte sink keeping the most recent writes —
// the stderr tail of an MCP stdio server, surfaced in connect errors so a
// server that dies on startup ("command not found", panic) is debuggable
// from /mcp without spelunking logs. Not safe for concurrent use; exec.Cmd
// writes from one goroutine and we read after the process settles.
type ringBuffer struct {
	buf  []byte
	size int
}

func newRingBuffer(size int) *ringBuffer { return &ringBuffer{size: size} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	if r.size == 0 {
		return len(p), nil
	}
	if len(p) >= r.size {
		r.buf = append(r.buf[:0], p[len(p)-r.size:]...)
		return len(p), nil
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.size {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.size:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string { return strings.TrimRight(string(r.buf), "\n") }
