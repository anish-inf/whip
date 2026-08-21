package bashrun

import (
	"context"
	"syscall"
	"testing"
	"time"
)

func trackedCount() int {
	trackMu.Lock()
	defer trackMu.Unlock()
	return len(tracked)
}

// KillAll must reap a long-running child started via the non-interactive path.
func TestKillAllReapsChildren(t *testing.T) {
	done := make(chan Result, 1)
	go func() { done <- Run(context.Background(), Options{Command: "sleep 60"}) }()

	deadline := time.Now().Add(2 * time.Second)
	for trackedCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("child never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	trackMu.Lock()
	var pid int
	for p := range tracked {
		pid = p
	}
	trackMu.Unlock()

	KillAll()

	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("process %d still alive after KillAll", pid)
	}
	select {
	case res := <-done:
		if !res.Killed {
			t.Fatalf("expected killed result, got %+v", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after KillAll")
	}
}

// A command that backgrounds a grandchild (`sleep 60 &`) must not hang the
// agent waiting on pipe EOF, and KillAll must still find the shell. (The
// grandchild is in the shell's process group; the group kill covers it.)
func TestBackgroundGrandchildDoesNotHang(t *testing.T) {
	done := make(chan Result, 1)
	go func() {
		done <- Run(context.Background(), Options{Command: "sleep 30 & echo started", Timeout: 10 * time.Second})
	}()
	select {
	case res := <-done:
		if res.TimedOut {
			t.Fatalf("background grandchild caused a timeout: %+v", res)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("run hung on a backgrounded grandchild")
	}
}
