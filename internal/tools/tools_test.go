package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, name, args string) string {
	t.Helper()
	return Execute(context.Background(), All(), name, json.RawMessage(args))
}

func TestToolRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "sub", "a.txt")

	out := run(t, "write", fmt.Sprintf(`{"path":%q,"content":"one\ntwo\nthree\n"}`, f))
	if strings.HasPrefix(out, "Error") {
		t.Fatal(out)
	}
	out = run(t, "read", fmt.Sprintf(`{"path":%q}`, f))
	if !strings.Contains(out, "2\ttwo") {
		t.Fatalf("read missing line numbers: %q", out)
	}
	out = run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"two","new_string":"2"}`, f))
	if strings.HasPrefix(out, "Error") {
		t.Fatal(out)
	}
	out = run(t, "read", fmt.Sprintf(`{"path":%q,"offset":2,"limit":1}`, f))
	if strings.TrimSpace(out) != "2\t2" {
		t.Fatalf("edit not applied: %q", out)
	}
	// ambiguous edit must fail without replace_all
	run(t, "write", fmt.Sprintf(`{"path":%q,"content":"x x"}`, f))
	out = run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"x","new_string":"y"}`, f))
	if !strings.HasPrefix(out, "Error") {
		t.Fatalf("expected ambiguity error, got %q", out)
	}
	out = run(t, "bash", `{"command":"echo hi; echo err >&2; exit 3"}`)
	if !strings.Contains(out, "hi") || !strings.Contains(out, "err") || !strings.Contains(out, "exit") {
		t.Fatalf("bash output wrong: %q", out)
	}
	out = run(t, "nope", `{}`)
	if !strings.Contains(out, "unknown tool") {
		t.Fatalf("expected unknown tool error, got %q", out)
	}
}

func TestHelpersAndEdgeCases(t *testing.T) {
	if len(Defs(All())) != 4 {
		t.Fatal("expected 4 tool defs")
	}
	long := strings.Repeat("x", maxOutput+10)
	if out := truncate(long); !strings.Contains(out, "truncated 10 bytes") {
		t.Fatalf("truncate: %q", out[len(out)-40:])
	}
	if out := truncateTail(long); !strings.HasPrefix(out, "[... first 10 bytes truncated]") {
		t.Fatalf("truncateTail: %q", out[:40])
	}
	// short strings pass through untouched
	if truncate("ok") != "ok" || truncateTail("ok") != "ok" {
		t.Fatal("short strings must not be modified")
	}

	// bad args json hits every tool's unmarshal error branch
	for _, name := range []string{"bash", "read", "write", "edit"} {
		if out := run(t, name, `{bad`); !strings.HasPrefix(out, "Error") {
			t.Fatalf("%s: expected error, got %q", name, out)
		}
	}

	// empty output branch
	if out := run(t, "bash", `{"command":"true"}`); out != "(no output)" {
		t.Fatalf("empty output: %q", out)
	}
	// timeout branch
	if out := run(t, "bash", `{"command":"sleep 5","timeout":0.1}`); !strings.Contains(out, "timed out") {
		t.Fatalf("timeout: %q", out)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	// read: missing file, offset past EOF, default limit
	if out := run(t, "read", fmt.Sprintf(`{"path":%q}`, f)); !strings.HasPrefix(out, "Error") {
		t.Fatalf("missing file: %q", out)
	}
	run(t, "write", fmt.Sprintf(`{"path":%q,"content":"a\nb"}`, f))
	if out := run(t, "read", fmt.Sprintf(`{"path":%q,"offset":99}`, f)); !strings.Contains(out, "past end") {
		t.Fatalf("offset past EOF: %q", out)
	}
	// write: MkdirAll fails when a parent is a file
	if out := run(t, "write", fmt.Sprintf(`{"path":%q,"content":"x"}`, f+"/child.txt")); !strings.HasPrefix(out, "Error") {
		t.Fatalf("bad parent: %q", out)
	}
	// edit: missing file, not-found old_string, replace_all
	if out := run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"x","new_string":"y"}`, filepath.Join(dir, "nope"))); !strings.HasPrefix(out, "Error") {
		t.Fatalf("edit missing file: %q", out)
	}
	if out := run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"zzz","new_string":"y"}`, f)); !strings.Contains(out, "not found") {
		t.Fatalf("edit not found: %q", out)
	}
	run(t, "write", fmt.Sprintf(`{"path":%q,"content":"x x x"}`, f))
	if out := run(t, "edit", fmt.Sprintf(`{"path":%q,"old_string":"x","new_string":"y","replace_all":true}`, f)); !strings.Contains(out, "3 occurrence") {
		t.Fatalf("replace_all: %q", out)
	}

	// Regression: a command that reads from /dev/tty (as sudo does for a
	// password) must NOT hang the tool. pre-fix the tool used CombinedOutput
	// with the child sharing loopy's controlling terminal, so the read
	// blocked until the 120s bash timeout. post-fix the child runs in a new
	// session with no controlling tty and stdin tied to /dev/null, so the
	// read fails immediately. We assert it returns well under the cap and
	// surfaces the tty failure rather than silently succeeding.
	start := time.Now()
	out := run(t, "bash", `{"command":"read -r p < /dev/tty; echo got $p","timeout":5}`)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("bash tool hung %s on /dev/tty read — fast-fail regressed: %q", elapsed, out)
	}
	if strings.Contains(out, "timed out") {
		t.Fatalf("bash tool timed out on /dev/tty read — fast-fail regressed: %q", out)
	}
	// The /dev/tty open must fail (no controlling terminal under Setsid);
	// bash reports "No such device or address" or similar. The crucial bit is
	// that $p is EMPTY — no password was read — and we did not hang.
	if !strings.Contains(out, "/dev/tty") {
		t.Fatalf("expected a /dev/tty error in output: %q", out)
	}
}

// mockInteractiveRunner is a fake tools.InteractiveRunner used to verify the
// bash tool's interactive hook wiring without spinning up a PTY.
type mockInteractiveRunner struct {
	gotCommand string
	gotTimeout time.Duration
	gotKeys    <-chan []byte
	returnThis string
}

func (m *mockInteractiveRunner) Run(_ context.Context, command string, timeout time.Duration, keys <-chan []byte) string {
	m.gotCommand = command
	m.gotTimeout = timeout
	m.gotKeys = keys
	return m.returnThis
}

// TestBashToolInteractiveHook verifies that bash with interactive:true hands
// off to the installed InteractiveBash runner, passing command+timeout+keys,
// and returns whatever the runner returns. It also confirms the hook is
// consulted only when interactive is true.
func TestBashToolInteractiveHook(t *testing.T) {
	mock := &mockInteractiveRunner{returnThis: "PASSWORD_ACCEPTED\n(exit: 0)"}
	prev := InteractiveBash
	InteractiveBash = mock
	defer func() { InteractiveBash = prev }()

	out := run(t, "bash", `{"command":"sudo apt install -y sl","interactive":true,"timeout":20}`)
	if out != "PASSWORD_ACCEPTED\n(exit: 0)" {
		t.Fatalf("interactive bash should return runner output verbatim: %q", out)
	}
	if mock.gotCommand != "sudo apt install -y sl" {
		t.Fatalf("runner got wrong command: %q", mock.gotCommand)
	}
	if mock.gotTimeout != 20*time.Second {
		t.Fatalf("runner got wrong timeout: %v", mock.gotTimeout)
	}
	if mock.gotKeys == nil {
		t.Fatalf("runner must receive a keys channel")
	}

	// interactive:false must NOT call the runner even when it's installed
	mock.gotCommand = ""
	out = run(t, "bash", `{"command":"echo nohook"}`)
	if mock.gotCommand != "" {
		t.Fatalf("non-interactive call should not reach the runner: %q", mock.gotCommand)
	}
	if !strings.Contains(out, "nohook") {
		t.Fatalf("non-interactive output wrong: %q", out)
	}
}
