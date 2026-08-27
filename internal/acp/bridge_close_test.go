package acp

// Coverage for the session teardown + capability-stub surface: the
// method-not-found stubs, session/close, CloseAll, the MCP merge (all four
// branches), and the small util helpers. These are the protocol edges the
// streaming tests don't reach.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"github.com/context-labs/whip/internal/mcp"
)

// session/close removes the session and releases it; an unknown id errors.
func TestCloseSession(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	id := f.newSession(t, t.TempDir())

	if _, err := f.conn.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: id}); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	f.bridge.mu.Lock()
	_, still := f.bridge.sessions[id]
	f.bridge.mu.Unlock()
	if still {
		t.Error("session still registered after close")
	}

	// Closing again is an unknown-session error.
	if _, err := f.conn.CloseSession(context.Background(), acp.CloseSessionRequest{SessionId: id}); err == nil {
		t.Error("expected error closing a removed session")
	}
}

// CloseAll tears down every live session and is idempotent once empty.
func TestCloseAll(t *testing.T) {
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, nil, factoryFor(srv, nil))
	f.initialize(t)
	f.newSession(t, t.TempDir())
	f.newSession(t, t.TempDir())

	f.bridge.CloseAll()
	f.bridge.mu.Lock()
	n := len(f.bridge.sessions)
	f.bridge.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions survived CloseAll", n)
	}
	f.bridge.CloseAll() // no-op on an empty registry
}

// The capability stubs whip doesn't implement answer method-not-found rather
// than panicking. Called directly: the SDK's client side doesn't expose these
// as outbound requests, so the wire path can't reach them.
func TestUnimplementedMethodsReturnMethodNotFound(t *testing.T) {
	b := NewBridge("test", nil, nil, false, nil)
	ctx := context.Background()

	if _, err := b.Authenticate(ctx, acp.AuthenticateRequest{}); err == nil {
		t.Error("authenticate: want method-not-found")
	}
	if _, err := b.Logout(ctx, acp.LogoutRequest{}); err == nil {
		t.Error("logout: want method-not-found")
	}
	if _, err := b.ResumeSession(ctx, acp.ResumeSessionRequest{}); err == nil {
		t.Error("session/resume: want method-not-found")
	}
	if _, err := b.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{}); err == nil {
		t.Error("session/set_config_option: want method-not-found")
	}
}

// mergeMCPServers: stdio + http translate, whip's base wins name clashes,
// empty names and deprecated variants drop.
func TestMergeMCPServers(t *testing.T) {
	b := NewBridge("test", nil, nil, false, map[string]mcp.ServerConfig{
		"base": {Command: []string{"whip-mcp"}},
	})

	out := b.mergeMCPServers([]acp.McpServer{
		{Stdio: &acp.McpServerStdio{
			Name: "docs", Command: "docs-mcp", Args: []string{"--serve"},
			Env: []acp.EnvVariable{{Name: "KEY", Value: "v"}},
		}},
		{Http: &acp.McpServerHttpInline{
			Name: "remote", Url: "https://mcp.example",
			Headers: []acp.HttpHeader{{Name: "Authorization", Value: "Bearer x"}},
		}},
		{Stdio: &acp.McpServerStdio{Name: "base", Command: "shadow"}},   // whip wins
		{Stdio: &acp.McpServerStdio{Name: "", Command: "noname"}},       // dropped
		{Sse: &acp.McpServerSseInline{Name: "old", Url: "https://sse"}}, // deprecated
	})

	if got := out["base"].Command; len(got) != 1 || got[0] != "whip-mcp" {
		t.Errorf("base = %v; whip config must win the name clash", got)
	}
	stdio := out["docs"]
	if len(stdio.Command) != 2 || stdio.Command[0] != "docs-mcp" || stdio.Command[1] != "--serve" {
		t.Errorf("stdio command = %v", stdio.Command)
	}
	if stdio.Env["KEY"] != "v" {
		t.Errorf("stdio env = %v", stdio.Env)
	}
	http := out["remote"]
	if http.URL != "https://mcp.example" || http.Headers["Authorization"] != "Bearer x" {
		t.Errorf("http = %+v", http)
	}
	if _, ok := out["old"]; ok {
		t.Error("deprecated sse server should be dropped")
	}
	if len(out) != 3 {
		t.Errorf("merged %d servers, want 3 (base + docs + remote)", len(out))
	}
}

// updateThoughtText wraps a delta in an agent_thought_chunk update (the SDK
// has no helper for this one).
func TestUpdateThoughtText(t *testing.T) {
	u := updateThoughtText("thinking…")
	if u.AgentThoughtChunk == nil {
		t.Fatal("no thought chunk")
	}
	if u.AgentThoughtChunk.SessionUpdate != "agent_thought_chunk" {
		t.Errorf("sessionUpdate = %q", u.AgentThoughtChunk.SessionUpdate)
	}
}

// todoStatusToACP maps whip's todo states onto ACP plan-entry statuses;
// anything unrecognized is pending.
func TestTodoStatusToACP(t *testing.T) {
	cases := map[string]acp.PlanEntryStatus{
		"in_progress": acp.PlanEntryStatusInProgress,
		"completed":   acp.PlanEntryStatusCompleted,
		"pending":     acp.PlanEntryStatusPending,
		"bogus":       acp.PlanEntryStatusPending,
		"":            acp.PlanEntryStatusPending,
	}
	for in, want := range cases {
		if got := todoStatusToACP(in); got != want {
			t.Errorf("todoStatusToACP(%q) = %v, want %v", in, got, want)
		}
	}
}

// update, sendTitle and close are all safe on a zero-value bridge/session:
// no connection, no store, no manager.
func TestZeroValueBridgeHelpersAreSafe(t *testing.T) {
	b := NewBridge("test", nil, nil, false, nil)
	ctx := context.Background()

	// update with no connection is a no-op (nil error).
	if err := b.update(ctx, "s1", acp.SessionUpdate{}); err != nil {
		t.Errorf("update with nil conn: %v", err)
	}

	// sendTitle with no store returns without sending.
	b.sendTitle(ctx, &acpSession{id: "s1"})

	// close with no manager and no in-flight turn is a no-op.
	(&acpSession{}).close()
}

// session/new validates its params: an empty cwd is rejected before any agent
// is built.
func TestNewSessionRequiresCwd(t *testing.T) {
	b := NewBridge("test", nil, nil, false, nil)
	_, err := b.NewSession(context.Background(), acp.NewSessionRequest{Cwd: ""})
	if err == nil {
		t.Error("empty cwd: want an invalid-params error")
	}
}

// session/load's guards: no store → method-not-found; a cwd that disagrees
// with the recorded session → invalid-params.
func TestLoadSessionGuards(t *testing.T) {
	ctx := context.Background()

	// No store: load is unsupported.
	noStore := NewBridge("test", nil, nil, false, nil)
	if _, err := noStore.LoadSession(ctx, acp.LoadSessionRequest{SessionId: "x"}); err == nil {
		t.Error("no store: want method-not-found")
	}

	// cwd mismatch against a real stored session.
	st := testStore(t)
	srv := scriptServer(t, []step{{text: "ok"}})
	f := newFixture(t, nil, st, factoryFor(srv, nil))
	f.initialize(t)
	dir := t.TempDir()
	id := f.newSession(t, dir)
	if _, err := f.prompt(t, id, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.LoadSession(ctx, acp.LoadSessionRequest{
		SessionId: id, Cwd: t.TempDir(), McpServers: []acp.McpServer{},
	}); err == nil {
		t.Error("cwd mismatch: want an invalid-params error")
	}
}

// config_logf fans out to the event log once SetEventLog wires it (stderr is
// untestable here; the hook is the observable half).
func TestEventLogHook(t *testing.T) {
	t.Cleanup(func() { SetEventLog(nil) })
	var got strings.Builder
	SetEventLog(func(format string, args ...any) { fmt.Fprintf(&got, format, args...) })
	config_logf("loaded %d servers", 3)
	if !strings.Contains(got.String(), "loaded 3 servers") {
		t.Errorf("event log = %q", got.String())
	}
}
