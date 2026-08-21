package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/context-labs/loopy/internal/tools"
)

// TaskStatus is the lifecycle of a background subagent.
type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskDone      TaskStatus = "done"
	TaskError     TaskStatus = "error"
	TaskCancelled TaskStatus = "cancelled"
)

// BackgroundTask is one backgrounded subagent. Done is closed exactly once when
// the task settles — closing a channel broadcasts to every waiter at once,
// which is what makes the "any number of watchers get woken together" shape
// free in Go (opencode needs a per-job Deferred for the same thing).
type BackgroundTask struct {
	ID          string
	Description string
	Prompt      string
	Status      TaskStatus
	Report      string // final report (done) or error text (error)
	StartedAt   time.Time
	EndedAt     time.Time

	Done   chan struct{}      // closed on settle; <-Done() wakes all waiters
	cancel context.CancelFunc // cancels the subagent's turn
}

// taskRegistry tracks background subagents for one parent agent. It is the
// Go-channels counterpart of opencode's BackgroundJob registry: a map of id →
// task whose Done channel fans completion out to the tool caller, the TUI, and
// /tasks without per-waiter state.
type taskRegistry struct {
	mu    sync.Mutex
	tasks map[string]*BackgroundTask
	seq   atomic.Int64
	// OnChange fires (from the worker goroutine) when a task starts or settles;
	// the TUI installs it to redraw the task list live.
	OnChange func(*BackgroundTask)
}

func newTaskRegistry() *taskRegistry {
	return &taskRegistry{tasks: map[string]*BackgroundTask{}}
}

// List returns a snapshot of all tasks, oldest first.
func (r *taskRegistry) List() []BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BackgroundTask, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, *t)
	}
	// insertion order isn't tracked; sort by start time
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.Before(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Get returns a snapshot of one task, or false if unknown.
func (r *taskRegistry) Get(id string) (BackgroundTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return BackgroundTask{}, false
	}
	return *t, true
}

// Cancel signals a running task's context. Returns false if not running.
func (r *taskRegistry) Cancel(id string) bool {
	r.mu.Lock()
	t, ok := r.tasks[id]
	r.mu.Unlock()
	if !ok || t.Status != TaskRunning {
		return false
	}
	t.cancel()
	return true
}

// settle records the final state and closes Done to wake every waiter.
func (r *taskRegistry) settle(id string, status TaskStatus, report string) {
	r.mu.Lock()
	t, ok := r.tasks[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	t.Status, t.Report, t.EndedAt = status, report, time.Now()
	r.mu.Unlock()
	close(t.Done) // broadcast to all waiters
	if r.OnChange != nil {
		r.OnChange(t)
	}
}

var taskIDCounter atomic.Int64

// StartBackground launches a subagent that runs concurrently with the parent.
// It returns immediately with a task handle; the model is told the task id and
// that the result will arrive as a steered message when done. This is the
// tool-call half of the background-subagent novelty: instead of blocking the
// turn on a subagent, the parent keeps working and the registry's Done channel
// delivers the report back through Steer when the subagent settles.
func (a *Agent) StartBackground(ctx context.Context, description, prompt string) *BackgroundTask {
	if a.bg == nil {
		a.bg = newTaskRegistry()
	}
	id := fmt.Sprintf("task-%d", taskIDCounter.Add(1))
	taskCtx, cancel := context.WithCancel(context.Background()) // NOT tied to the turn's ctx: a background task outlives the current turn
	t := &BackgroundTask{
		ID: id, Description: description, Prompt: prompt,
		Status: TaskRunning, StartedAt: time.Now(),
		Done: make(chan struct{}), cancel: cancel,
	}
	a.bg.mu.Lock()
	a.bg.tasks[id] = t
	a.bg.mu.Unlock()
	if a.bg.OnChange != nil {
		a.bg.OnChange(t)
	}

	go func() {
		sub := New(a.Client, a.Model, a.MaxTokens, subagentPrompt())
		sub.Effort = a.Effort
		sub.ContextLimit = a.ContextLimit
		sub.Tools = tools.All()
		report, err := sub.Turn(taskCtx, prompt, Events{OnUsage: a.AddUsage})
		status := TaskDone
		text := report
		switch {
		case err != nil && taskCtx.Err() == context.Canceled:
			status, text = TaskCancelled, "cancelled"
		case err != nil:
			status, text = TaskError, err.Error()
		}
		a.bg.settle(id, status, text)
		// Fan the result back into the parent as a steered message so the model
		// sees it on the next loop boundary — channel-close (settle) → Steer.
		// text/status are locals (not the shared task struct), so no race.
		a.Steer(fmt.Sprintf("[background task %s %s] %s\n\n%s", id, status, description, text))
	}()
	return t
}

// Tasks returns the registry, creating it lazily.
func (a *Agent) Tasks() *taskRegistry {
	if a.bg == nil {
		a.bg = newTaskRegistry()
	}
	return a.bg
}
