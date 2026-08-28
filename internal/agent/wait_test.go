package agent

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/context-labs/whip/internal/llm"
	"github.com/context-labs/whip/internal/tools"
)

// A condition already true resolves in the first check (no interval wait)
// and delivers exactly one "met" message.
func TestWaitConditionMetImmediately(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()

	var woke atomic.Int32
	ag.Waits().OnWake = func(string) { woke.Add(1) }

	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: 50 * time.Millisecond, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("wait never delivered")
	}
	if w.Status() != WaitMet {
		t.Fatalf("status = %q, want %q", w.Status(), WaitMet)
	}
	if !strings.Contains(w.Detail, "condition met") {
		t.Fatalf("detail = %q", w.Detail)
	}
	if got := woke.Load(); got != 1 {
		t.Fatalf("idle wake fired %d times, want exactly 1", got)
	}
}

// The until regex gates success: exit-0 alone doesn't settle the wait when
// `until` is set, and a matching output settles it once the pattern appears.
func TestWaitUntilRegex(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	// The command exits 0 but prints "running" — until "ready" must not fire.
	w, err := ag.StartWait(WaitTaskSpec{Command: "echo running", Until: "ready", Interval: 50 * time.Millisecond, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitTimeout {
		t.Fatalf("status = %q, want %q (until never matched)", w.Status(), WaitTimeout)
	}

	// A bad until regex is rejected at registration, not at first poll.
	if _, err := ag.StartWait(WaitTaskSpec{Command: "true", Until: "[unclosed"}); err == nil {
		t.Fatal("bad until regex should fail StartWait")
	}
}

// A command that keeps failing strikes out after 3 consecutive errors instead
// of polling until the timeout.
func TestWaitStrikesOut(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	start := time.Now()
	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 1", Interval: 30 * time.Millisecond, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitFailed {
		t.Fatalf("status = %q, want %q", w.Status(), WaitFailed)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("strike-out should beat the timeout: took %s", d)
	}
}

// Timeout delivers a timeout message exactly once.
func TestWaitTimeout(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo still-waiting", Until: "never-matches", Interval: 30 * time.Millisecond, Timeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if w.Status() != WaitTimeout {
		t.Fatalf("status = %q, want %q", w.Status(), WaitTimeout)
	}
	if !strings.Contains(w.Detail, "timeout") {
		t.Fatalf("detail = %q", w.Detail)
	}
}

// A running turn routes delivery through Steer (loop boundary), not OnWake.
func TestWaitBusySteersInsteadOfWaking(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.running.Store(true) // simulate an in-flight turn

	var woke atomic.Int32
	ag.Waits().OnWake = func(string) { woke.Add(1) }

	w, err := ag.StartWait(WaitTaskSpec{Command: "exit 0", Interval: 30 * time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	<-w.Done
	if woke.Load() != 0 {
		t.Fatal("busy delivery must not fire OnWake")
	}
	if got := len(ag.drainPending()); got != 1 {
		t.Fatalf("busy delivery should queue exactly one steer, got %d", got)
	}
}

// Cancel stops the wait and suppresses delivery.
func TestWaitCancel(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()
	ag.Waits().OnWake = func(string) {}

	w, err := ag.StartWait(WaitTaskSpec{Command: "echo x", Until: "never", Interval: time.Second, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !ag.Waits().CancelWait(w.ID) {
		t.Fatal("cancel of a running wait should succeed")
	}
	if w.Status() != WaitKilled {
		t.Fatalf("status = %q, want %q", w.Status(), WaitKilled)
	}
	if ag.Waits().CancelWait(w.ID) {
		t.Fatal("second cancel should report not-running")
	}
}

// The wait tool parses args, registers the poller, and returns the "don't
// poll" contract message.
func TestWaitToolRegisters(t *testing.T) {
	ag := New(llm.New("http://unused", "k"), "m", 100, "sys")
	defer ag.Waits().Close()

	var wt tools.Tool
	for _, tl := range ag.AllTools() {
		if tl.Def.Function.Name == "wait" {
			wt = tl
		}
	}
	if wt.Def.Function.Name == "" {
		t.Fatal("agent should expose the wait tool")
	}
	out, err := wt.Run(t.Context(), json.RawMessage(`{"command":"exit 0","interval":0.05,"timeout":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "do NOT sleep-poll") {
		t.Fatalf("tool output should state the no-poll contract: %q", out)
	}
	// The registered wait should settle quickly (exit 0).
	deadline := time.After(2 * time.Second)
	for {
		done := true
		for _, w := range ag.Waits().ListWaits() {
			if w.Status() == WaitRunning {
				done = false
			}
		}
		if done {
			return
		}
		select {
		case <-deadline:
			t.Fatal("registered wait never settled")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
