package mcp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServeInProcess drives Serve without a subprocess: Serve's
// StdioTransport is hardwired to os.Stdin/os.Stdout, so the test swaps both
// for pipes and speaks MCP through them with the SDK client. The
// WHIP_TEST_SELFHOST-gated tests cover the real-subprocess path; this one
// keeps Serve covered in plain CI.
func TestServeInProcess(t *testing.T) {
	inR, inW, err := os.Pipe() // server stdin
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe() // server stdout
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "test") }()

	// Restore stdio only after Serve has returned, so the swap can't race
	// the server's reads under -race.
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after stdin close + cancel")
		}
		os.Stdin, os.Stdout = oldIn, oldOut
	})

	cli := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := cli.Connect(ctx, &sdkmcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	if len(list.Tools) != 4 || !names["read"] || names["task"] {
		t.Fatalf("served tools = %v (want whip's 4, task excluded)", names)
	}

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{"path": "serve.go", "limit": 3},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	txt, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.Contains(txt.Text, "package mcp") {
		t.Fatalf("read via MCP = %#v", res.Content)
	}

	// Tool errors must come back as tool output, not protocol failures.
	res, err = cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "read",
		Arguments: map[string]any{"path": "does-not-exist.xyz"},
	})
	if err != nil {
		t.Fatalf("failing tool call should not be a protocol error: %v", err)
	}
	txt, ok = res.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.HasPrefix(txt.Text, "Error: ") {
		t.Fatalf("tool error surfaced as %#v", res.Content)
	}
}
