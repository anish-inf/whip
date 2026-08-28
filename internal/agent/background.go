package agent

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	// Restored marks a task seeded from the session store by --resume: its
	// subagent died with the previous process, so it's history for /tasks —
	// never live, and the dock leaves it out.
	Restored bool

	Done   chan struct{}      // closed on settle; <-Done() wakes all waiters
	cancel context.CancelFunc // cancels the subagent's turn

	// sub is the retained subagent: while running it receives SteerTask
	// guidance, and after settle FollowupTask keeps chatting on its preserved
	// context. nil on restored tasks (their process died). Set before the
	// task is published, never reassigned — snapshots copy the pointer safely.
	sub *Agent
}

// JournaledEvent is one recorded task event. Kind mirrors the TUI's
// taskEventMsg kinds: 0 text, 1 tool start, 2 tool end, 3 steer, 4 compact.
// S/S2 carry the payload (text / tool name+args / tool result). Consecutive
// text deltas coalesce into one entry so long streams don't fill the slice.
type JournaledEvent struct {
	Kind  int
	S, S2 string
}

// taskJournal is a per-task ring of recent events, byte-bounded so a chatty
// subagent can't grow memory without limit. Over budget → drop from the front
// and mark Truncated (the detail view renders a "[earlier output dropped]"
// header line instead of pretending the transcript is complete).
type taskJournal struct {
	events    []JournaledEvent
	bytes     int
	Truncated bool
}

// journalBudget caps one task's journal at 128KB of payload text. Sized to
// hold a typical exploration's full transcript (tool outputs dominate) while
// bounding a registry full of tasks at single-digit MB.
const journalBudget = 128 * 1024

// append records one event, coalescing consecutive text deltas (kind 0) into
// the previous entry — the stream emits one event per SSE delta.
func (j *taskJournal) append(kind int, s, s2 string) {
	if kind == 0 && len(j.events) > 0 && j.events[len(j.events)-1].Kind == 0 {
		j.events[len(j.events)-1].S += s
		j.bytes += len(s)
	} else {
		j.events = append(j.events, JournaledEvent{Kind: kind, S: s, S2: s2})
		j.bytes += len(s) + len(s2)
	}
	for j.bytes > journalBudget && len(j.events) > 1 {
		j.bytes -= len(j.events[0].S) + len(j.events[0].S2)
		j.events = j.events[1:]
		j.Truncated = true
	}
}

// taskRegistry tracks background subagents for one parent agent. It is the
// Go-channels counterpart of opencode's BackgroundJob registry: a map of id →
// task whose Done channel fans completion out to the tool caller, the TUI, and
// /tasks without per-waiter state.
type taskRegistry struct {
	mu    sync.Mutex
	tasks map[string]*BackgroundTask
	// journals record each task's emitted events so a detail view opened
	// mid-run (or after settle) replays the full transcript instead of only
	// what streams in after Subscribe. Written under mu in emitter(); read
	// under mu by SubscribeWithJournal. Entries die with their task in
	// ClearSettled.
	journals map[string]*taskJournal
	// subs are live event subscribers per task id (the TUI's per-task view).
	// Events is all callbacks, so fan-out is a slice the worker walks per
	// event — no channel to close, no per-subscriber goroutine. Kept here
	// (not on the task) because List/Get snapshot tasks by value.
	subs map[string][]Events
	// OnChange fires (from the worker goroutine) when a task starts or settles;
	// the TUI installs it to redraw the task list live.
	OnChange func(*BackgroundTask)
	// OnRecord fires (from the worker goroutine) right after OnChange on start
	// and settle; the TUI installs it to persist the task to the session store.
	// Separate from OnChange so headless tests (prog == nil) still record.
	// sessionID is what the handler should record against: the TUI publishes
	// it via SetSessionID (an atomic, so the worker goroutine never races the
	// UI goroutine's session switching). "" = no session yet; handlers must
	// skip recording then.
	OnRecord  func(sessionID string, t *BackgroundTask)
	sessionID atomic.Pointer[string]
}

// SetSessionID publishes the session task records belong to ("" clears it —
// /clear and /fork do this so a task settling mid-switch doesn't record
// against the wrong session). Atomic: the registry's OnRecord runs on the
// subagent worker goroutine while the TUI sets this from the UI goroutine.
func (r *taskRegistry) SetSessionID(id string) {
	if id == "" {
		r.sessionID.Store(nil)
		return
	}
	r.sessionID.Store(&id)
}

// recordSession returns the published session id ("" when none).
func (r *taskRegistry) recordSession() string {
	if p := r.sessionID.Load(); p != nil {
		return *p
	}
	return ""
}

func newTaskRegistry() *taskRegistry {
	return &taskRegistry{tasks: map[string]*BackgroundTask{}, subs: map[string][]Events{}, journals: map[string]*taskJournal{}}
}

// List returns a snapshot of all tasks, oldest first.
func (r *taskRegistry) List() []BackgroundTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BackgroundTask, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, *t)
	}
	// insertion order isn't tracked; sort by start time. Same-burst tasks
	// share a StartedAt, so tiebreak on the id (task-N is monotonic) — a bare
	// time sort leaves ties to map iteration order and the dock reshuffles on
	// every redraw.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return taskIDNum(out[i].ID) < taskIDNum(out[j].ID)
	})
	return out
}

// taskIDNum parses the monotonic counter out of a task id for stable sorting.
// New ids are "slug-<counter>" (the counter is the trailing number); legacy
// ids were "sub-N"/"task-N" with the number right after the prefix. 0 on
// malformed ids, which sort first — fine for a tiebreak.
func taskIDNum(id string) int64 {
	if i := strings.LastIndexByte(id, '-'); i >= 0 {
		n, _ := strconv.ParseInt(id[i+1:], 10, 64)
		return n
	}
	return 0
}

// taskSlug builds a human-meaningful task id from the description plus a
// monotonic counter for uniqueness: "survey-context-in-pi-3". Falls back to
// "sub-<n>" when the description yields nothing usable. A description-derived
// id is what /subagents, the ⚙ badge tooltip, and steer messages show, so it
// should name the work, not a bare sequence number.
func taskSlug(description string, n int64) string {
	words := strings.FieldsFunc(strings.ToLower(description), func(r rune) bool {
		return r < 'a' || r > 'z' && (r < '0' || r > '9')
	})
	var kept []string
	for _, w := range words {
		if w != "" {
			kept = append(kept, w)
		}
		if len(kept) == 5 {
			break
		}
	}
	slug := strings.Join(kept, "-")
	if slug == "" {
		slug = "sub"
	}
	return fmt.Sprintf("%s-%d", slug, n)
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

// ClearSettled drops every done/error/cancelled task, keeping the running
// ones. The TUI calls this when a new turn starts: settled tasks have already
// reported into the transcript, so the dock strip makes room instead of
// accumulating stale rows forever. keep protects specific ids from the sweep
// (a task whose chat pane is open must survive). Returns the number cleared.
func (r *taskRegistry) ClearSettled(keep ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id, t := range r.tasks {
		if !slices.Contains(keep, id) && t.Status != TaskRunning {
			delete(r.tasks, id)
			delete(r.subs, id)
			delete(r.journals, id)
			n++
		}
	}
	return n
}

// Cancel signals a running task's context. Returns false if not running.
// The status check happens under the registry mutex: settle() writes Status
// under the same lock, so a Cancel racing a settle must read it there too
// (an unsynchronized read is a data race — and could cancel a task that just
// finished). The cancel func itself runs AFTER unlocking: it cancels the
// subagent's turn, and the resulting settle re-takes the lock.
func (r *taskRegistry) Cancel(id string) bool {
	r.mu.Lock()
	t, ok := r.tasks[id]
	running := ok && t.Status == TaskRunning
	r.mu.Unlock()
	if !running {
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
	// Notify and persist BEFORE closing Done: a waiter woken by the close must
	// be able to read the recorded final state (the session store row) — with
	// the old order a reader could see a settled task still persisted as
	// "running". Callbacks run on the worker goroutine and are cheap.
	if r.OnChange != nil {
		r.OnChange(t)
	}
	if r.OnRecord != nil {
		r.OnRecord(r.recordSession(), t)
	}
	close(t.Done) // broadcast to all waiters
}

var taskIDCounter atomic.Int64

// StartBackground launches a subagent that runs concurrently with the parent.
// It returns immediately with a task handle; the model is told the task id and
// that the result will arrive as a steered message when done. This is the
// tool-call half of the background-subagent novelty: instead of blocking the
// turn on a subagent, the parent keeps working and the registry's Done channel
// delivers the report back through Steer when the subagent settles.
// o overrides the subagent's model route (zero = TaskDefault → parent model).
// There is no ctx parameter on purpose: a background task outlives the turn
// that started it, so it owns its own cancellable context.
func (a *Agent) StartBackground(description, prompt string, o SubModel) *BackgroundTask {
	if a.bg == nil {
		a.bg = newTaskRegistry()
	}
	id := taskSlug(description, taskIDCounter.Add(1))
	taskCtx, cancel := context.WithCancel(context.Background())
	t := &BackgroundTask{
		ID: id, Description: description, Prompt: prompt,
		Status: TaskRunning, StartedAt: time.Now(),
		Done: make(chan struct{}), cancel: cancel,
		sub: a.newSub(o),
	}
	a.bg.mu.Lock()
	a.bg.tasks[id] = t
	a.bg.mu.Unlock()
	if a.bg.OnChange != nil {
		a.bg.OnChange(t)
	}
	if a.bg.OnRecord != nil {
		a.bg.OnRecord(a.bg.recordSession(), t)
	}

	go func() {
		report, err := t.sub.Turn(taskCtx, prompt, FanIn(a.bg.emitter(id), Events{OnUsage: a.AddUsage}))
		status := TaskDone
		text := report
		switch {
		case err != nil && taskCtx.Err() == context.Canceled:
			status, text = TaskCancelled, "cancelled"
		case err != nil:
			status, text = TaskError, err.Error()
		}
		a.bg.settle(id, status, text)
		// subscribers stop here; late events after settle go nowhere (Subscribe
		// rejects non-running tasks, and settled state is visible via List/Get)
		a.bg.mu.Lock()
		delete(a.bg.subs, id)
		a.bg.mu.Unlock()
		// Fan the result back into the parent as a steered message so the model
		// sees it on the next loop boundary — channel-close (settle) → Steer.
		// text/status are locals (not the shared task struct), so no race.
		a.Steer(fmt.Sprintf("[subagent %s %s] %s\n\n%s", id, status, description, text))
	}()
	return t
}

// SubscribeWithJournal returns the task's journaled events so far and, when
// the task is still running, registers ev as a live subscriber — atomically
// under one lock hold, so no event between the replay point and the
// subscription is missed or delivered twice. Running reports ok=true; a
// settled (or restored) task reports ok=false and the caller renders the
// journal as history. Returns false with a nil journal for unknown ids.
func (r *taskRegistry) SubscribeWithJournal(id string, ev Events) (events []JournaledEvent, truncated, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, exists := r.tasks[id]
	if !exists {
		return nil, false, false
	}
	j := r.journals[id]
	if j != nil {
		events = append([]JournaledEvent(nil), j.events...)
		truncated = j.Truncated
	}
	if t.Status != TaskRunning {
		return events, truncated, false
	}
	r.subs[id] = append(r.subs[id], ev)
	return events, truncated, true
}

// emitter returns an Events that journals every callback and forwards it to
// the task's current subscribers (the TUI's per-task view). Subscriber
// callbacks run on the worker goroutine, so they must be cheap and
// non-blocking. Tool calls stream in as args deltas (OnToolCall); only the
// start (kind 1) and end (kind 2) are journaled — the deltas would fill the
// emitLocked folds the journal append and the subscriber snapshot into a
// single registry lock hold so an event can never be both replayed from the
// journal AND delivered live to the same view: a SubscribeWithJournal that
// runs before emitLocked sees the event in the journal (and the subscriber
// isn't registered yet, so it's not in the snapshot); one that runs after
// finds the event already journaled and the subscriber registered for the
// NEXT event. The two-orderings-are-exhaustive property is what makes the
// replay→live seam neither drop nor double an event.
//
// Callbacks run AFTER the lock is released (the snapshot is taken under it):
// subscribers are allowed to block (the TUI's task view funnels events
// through prog.Send, which parks when the UI event queue backs up), and a
// blocked callback must never hold mu hostage — the UI goroutine itself takes
// mu via List/Get when rendering the dock, so running a blocking callback
// under the lock is an ABBA deadlock (worker holds mu → waits on the UI
// queue; UI waits on mu).
func (r *taskRegistry) emitLocked(id string, kind int, s, s2 string, journaled bool) []Events {
	r.mu.Lock()
	defer r.mu.Unlock()
	if journaled {
		j := r.journals[id]
		if j == nil {
			j = &taskJournal{}
			r.journals[id] = j
		}
		j.append(kind, s, s2)
	}
	return append([]Events(nil), r.subs[id]...)
}

// emitter returns an Events that journals every callback and forwards it to
// the task's current subscribers (the TUI's per-task view). Subscriber
// callbacks run on the worker goroutine, so they must be cheap and
// non-blocking. Tool calls stream in as args deltas (OnToolCall); only the
// start (kind 1) and end (kind 2) are journaled — the deltas would fill the
// budget with partial JSON. Compaction is NOT journaled: kind 4 is reserved
// for the TUI's follow-up-settled message, and replaying a compact as if it
// were a settle would render it as an error.
func (r *taskRegistry) emitter(id string) Events {
	return Events{
		OnText: func(s string) {
			subs := r.emitLocked(id, 0, s, "", true)
			for _, e := range subs {
				if e.OnText != nil {
					e.OnText(s)
				}
			}
		},
		OnThink: func(s string) {
			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnThink != nil {
					e.OnThink(s)
				}
			}
		},
		OnToolStart: func(tcID, n, a string) {
			subs := r.emitLocked(id, 1, n, a, true)
			for _, e := range subs {
				if e.OnToolStart != nil {
					e.OnToolStart(tcID, n, a)
				}
			}
		},
		OnToolCall: func(tcID, n, a string) {
			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnToolCall != nil {
					e.OnToolCall(tcID, n, a)
				}
			}
		},
		OnToolEnd: func(tcID, n, res string) {
			subs := r.emitLocked(id, 2, n, res, true)
			for _, e := range subs {
				if e.OnToolEnd != nil {
					e.OnToolEnd(tcID, n, res)
				}
			}
		},
		OnSteer: func(s string) {
			subs := r.emitLocked(id, 3, s, "", true)
			for _, e := range subs {
				if e.OnSteer != nil {
					e.OnSteer(s)
				}
			}
		},
		OnCompact: func(took, kept int) {
			// journaled=false: kind 4 is follow-up-settled in the TUI, so a
			// journaled compact would replay as an error line.
			for _, e := range r.emitLocked(id, 0, "", "", false) {
				if e.OnCompact != nil {
					e.OnCompact(took, kept)
				}
			}
		},
	}
}

// Tasks returns the registry, creating it lazily.
func (a *Agent) Tasks() *taskRegistry {
	if a.bg == nil {
		a.bg = newTaskRegistry()
	}
	return a.bg
}

// RestoreTask inserts a previously-persisted task into the registry as
// settled — no goroutine, no Steer, and Done arrives already closed so
// waiters never block on work that isn't running. Used by --resume: the
// subagent's process died with the last exit, so a persisted "running" row
// must be restored with an explicit settled status by the caller.
func (a *Agent) RestoreTask(t BackgroundTask) {
	r := a.Tasks()
	t.Done = make(chan struct{})
	close(t.Done)
	t.cancel = func() {} // Cancel() rejects non-running tasks, so it's never called
	r.mu.Lock()
	r.tasks[t.ID] = &t
	r.mu.Unlock()
}
