package agent

import (
	"encoding/json"
	"path/filepath"
	"sync"
)

// fileLocks serializes mutations to the same canonical path across parallel
// tool calls. Each path owns a 1-capacity channel used as a semaphore: a tool
// acquires by sending (blocks until free) and releases by receiving. A channel
// per path is the idiomatic Go form of pi's per-path promise-chain queue — no
// explicit unlock bookkeeping.
//
// Only write/edit take a per-path lock; reads don't. Bash takes the global
// lock because a command can touch anything.
type fileLocks struct {
	mu     sync.Mutex
	locks  map[string]chan struct{}
	global chan struct{} // serializes bash (unknown side effects) with mutations
}

func newFileLocks() *fileLocks {
	return &fileLocks{
		locks:  map[string]chan struct{}{},
		global: make(chan struct{}, 1),
	}
}

// acquirePath blocks until the lock for path is held, returning a release func.
// The 1-capacity channel means the first acquirer succeeds immediately and
// later acquirers block on send until the holder receives.
func (f *fileLocks) acquirePath(path string) func() {
	key := canonicalPathKey(path)
	f.mu.Lock()
	ch, ok := f.locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		f.locks[key] = ch
	}
	f.mu.Unlock()
	ch <- struct{}{}       // acquire (blocks while held)
	return func() { <-ch } // release
}

// acquireGlobal serializes a tool call against every other mutation — used by
// bash, whose side effects can't be attributed to one path.
func (f *fileLocks) acquireGlobal() func() {
	f.global <- struct{}{}
	return func() { <-f.global }
}

// canonicalPathKey normalizes a path so two spellings of the same file share
// one lock (pi resolves through the FS; we settle for absolute + clean).
func canonicalPathKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

// toolMutationPath extracts the path a write/edit tool call will mutate. The
// second return is false for tools whose side effects aren't path-scoped
// (bash), which must take the global lock.
func toolMutationPath(toolName, args string) (string, bool) {
	switch toolName {
	case "write", "edit":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(args), &a); err == nil && a.Path != "" {
			return a.Path, true
		}
	}
	return "", false
}
