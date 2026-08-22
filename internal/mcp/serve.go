package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/context-labs/loopy/internal/tools"
)

// Serve runs loopy's built-in tools as an MCP server over stdio — the other
// direction of the integration: any MCP-capable harness (claude-code, codex,
// another loopy) can drive loopy's read/bash/edit/write with
//
//	loopy mcp serve
//
// registered as a stdio server. The `task` tool is excluded (no subagent
// recursion over MCP). Callers use the raw llm definitions verbatim.
func Serve(ctx context.Context, version string) error {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "loopy", Version: version}, nil)
	for _, t := range tools.All() {
		t := t
		srv.AddTool(&sdkmcp.Tool{
			Name:        t.Def.Function.Name,
			Description: t.Def.Function.Description,
			// The defs carry a JSON-schema string; the SDK wants any value
			// that marshals to a schema, and json.RawMessage marshals verbatim.
			InputSchema: json.RawMessage(t.Def.Function.Parameters),
		}, func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			out, err := t.Run(ctx, req.Params.Arguments)
			if err != nil {
				out = "Error: " + err.Error() // errors are tool output, not protocol failures
			}
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: out}},
			}, nil
		})
	}
	if err := srv.Run(ctx, &sdkmcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp serve: %w", err)
	}
	return nil
}
