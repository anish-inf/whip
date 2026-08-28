package acp

// permission.go adapts whip's tools.Gate consent seam to ACP's
// session/request_permission: in "ask" mode a gated tool call blocks on the
// client's answer, exactly like the TUI's consent prompt.
//
// tools.Gate is package-global, so installs serialize bridge-wide
// (b.gateMu): a second session's ask-mode turn waits rather than interleave
// prompts the user can't attribute to the right session. "Allow always"
// rules are remembered per session (s.allowed, keyed by the arity-collapsed
// rule) — the TUI persists these to disk; ACP sessions keep them in memory
// only, which matches the editor's per-session permission model.

import (
	"context"
	"fmt"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	"github.com/context-labs/whip/internal/tools"
)

const (
	optAllowOnce   = "allow-once"
	optAllowAlways = "allow-always"
	optReject      = "reject"
)

// gateMu serializes tools.Gate installation across sessions (see above).
var gateMu sync.Mutex

// installPermissionGate points tools.Gate at the client for this session's
// turn and returns the restore func. Blocking on gateMu here means a
// concurrent ask-mode turn in another session holds the gate until its turn
// ends — ask-mode turns are inherently user-paced, so this is the honest
// serialization point.
func (b *Bridge) installPermissionGate(s *acpSession, turnCtx context.Context) (restore func()) {
	gateMu.Lock()
	prev := tools.Gate
	tools.Gate = func(req tools.GateRequest) (tools.GateDecision, string) {
		return b.requestPermission(turnCtx, s, req)
	}
	return func() {
		tools.Gate = prev
		gateMu.Unlock()
	}
}

// requestPermission round-trips one GateRequest through the client. The ctx
// is the turn's, so session/cancel unblocks a pending prompt (the client
// answers "cancelled" per spec; a dead client fails closed via ctx).
// A cancelled or errored prompt is a reject — fail-closed.
func (b *Bridge) requestPermission(ctx context.Context, s *acpSession, req tools.GateRequest) (tools.GateDecision, string) {
	if b.conn == nil {
		return tools.GateAllowOnce, ""
	}

	// "Always allow" rules cover repeat calls without re-prompting.
	rule := req.Rule
	if req.Tool != "bash" {
		rule = req.Command // path rules are exact (matches the TUI)
	}
	key := req.Tool + ":" + rule
	s.turnMu.Lock()
	covered := s.allowed[key]
	s.turnMu.Unlock()
	if covered {
		return tools.GateAllowOnce, ""
	}

	name := req.Tool
	if req.Command != "" {
		name = fmt.Sprintf("%s %q", req.Tool, req.Command)
	}
	options := []acp.PermissionOption{
		{OptionId: optAllowOnce, Name: "Allow once", Kind: acp.PermissionOptionKindAllowOnce},
		{OptionId: optReject, Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
	}
	if req.Rule != "" {
		options = append(options, acp.PermissionOption{
			OptionId: optAllowAlways,
			Name:     fmt.Sprintf("Always allow %q", req.Rule),
			Kind:     acp.PermissionOptionKindAllowAlways,
		})
	}

	resp, err := b.conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: s.id,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId("perm-" + req.Tool),
			Title:      new(name),
			Kind:       new(toolKind(req.Tool)),
		},
		Options: options,
	})
	if err != nil {
		if ctx.Err() != nil {
			return tools.GateReject, "the user cancelled the permission prompt"
		}
		return tools.GateReject, "permission request failed: " + err.Error()
	}
	switch {
	case resp.Outcome.Selected != nil:
		switch string(resp.Outcome.Selected.OptionId) {
		case optAllowOnce:
			return tools.GateAllowOnce, ""
		case optAllowAlways:
			s.turnMu.Lock()
			s.allowed[key] = true
			s.turnMu.Unlock()
			return tools.GateAllowAlways, ""
		default:
			return tools.GateReject, "the user rejected this action"
		}
	default: // cancelled
		return tools.GateReject, "the user cancelled the permission prompt"
	}
}
