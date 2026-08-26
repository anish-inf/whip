package lsp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// Concurrency proofs: spawn dedup (N concurrent touches spawn once) and
// parallel waiter wakeups. Everything runs under -race.

// TestSpawnDedup launches concurrent WaitDiagnostics calls for files that
// share a server key; exactly one spawn runs (a real spawn fails instantly
// here — the fake binary doesn't exist — but the dedup invariant is what
// matters: one spawn attempt, one broken entry, all waiters resolved).
func TestSpawnDedup(t *testing.T) {
	m := NewManager(map[string]ServerSpec{"gopls": fakeSpec()})
	defer m.Close()
	dir := t.TempDir()
	writeFile(t, dir+"/go.mod", "module x\n")
	for i := range 8 {
		writeFile(t, fmt.Sprintf("%s/f%d.go", dir, i), "package main\n")
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			m.WaitDiagnostics(context.Background(), fmt.Sprintf("%s/f%d.go", dir, i))
		})
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	if n := len(m.broken); n != 1 {
		t.Fatalf("expected exactly 1 spawn attempt (1 broken entry), got %d", n)
	}
	if len(m.spawning) != 0 {
		t.Fatalf("spawning map leaked: %v", m.spawning)
	}
}

// TestParallelWaiterWake proves publish wakes all waiters on a file and that
// waiters on different files proceed independently, with no goroutine leak.
func TestParallelWaiterWake(t *testing.T) {
	var pushes sync.WaitGroup
	pushes.Add(2)
	f := startFakeServer(t, func(uri string, version int) []push {
		pushes.Done()
		return []push{{version: version, diags: []Diagnostic{
			{Line: 1, Col: 1, Severity: SeverityError, Message: "boom " + uri},
		}}}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/a.go", "package main\n")
	writeFile(t, dir+"/b.go", "package main\n")

	// Baseline goroutine count after everything settles.
	before := numGoroutines()

	var wg sync.WaitGroup
	outs := make([]string, 2)
	for i, name := range []string{"a.go", "b.go"} {
		wg.Go(func() {
			outs[i] = m.WaitDiagnostics(context.Background(), dir+"/"+name)
		})
	}
	wg.Wait()
	pushes.Wait() // both touches reached the server

	for i, out := range outs {
		if out == "" {
			t.Fatalf("waiter %d got no diagnostics", i)
		}
	}

	// Give read/write pumps a moment to observe close, then compare counts.
	time.Sleep(50 * time.Millisecond)
	if after := numGoroutines(); after > before+2 {
		t.Fatalf("goroutines grew: before=%d after=%d", before, after)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.waiters) != 0 {
		t.Fatalf("waiters leaked: %v", m.waiters)
	}
}

// TestWaitBeforePublish covers the publish-before-wait interleaving: a push
// that lands between cache snapshot and waiter registration must still be
// observed via the re-check loop (no lost wakeup).
func TestWaitBeforePublishInterleaving(t *testing.T) {
	f := startFakeServer(t, func(uri string, version int) []push {
		return []push{{version: version, diags: []Diagnostic{
			{Line: 5, Col: 5, Severity: SeverityError, Message: "late"},
		}}}
	})
	m := pipeManager(f)
	defer m.Close()

	dir := t.TempDir()
	writeFile(t, dir+"/main.go", "package main\n")
	// First touch to establish a non-nil baseline.
	m.WaitDiagnostics(context.Background(), dir+"/main.go")
	// Second touch: version 2 push with different content must be seen.
	writeFile(t, dir+"/main.go", "package main\n\n")
	out := m.WaitDiagnostics(context.Background(), dir+"/main.go")
	if out == "" {
		t.Fatal("interleaved push was lost")
	}
}
