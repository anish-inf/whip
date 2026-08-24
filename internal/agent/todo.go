// todowrite: a conversation-scoped plan the model rewrites in full on every
// call. Open items are injected back into the request each round so the plan
// survives long tool loops and compactions. Design: docs/learnings/
// other-harnesses/exo.md §7 (exo's todo-tools.ts), trimmed to loopy's scale.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/context-labs/loopy/internal/llm"
	"github.com/context-labs/loopy/internal/tools"
)

const (
	maxTodos          = 50
	maxTodoContent    = 300
	toolNameTodowrite = "todowrite"
)

// Todo is one plan item. Status is one of pending|in_progress|completed|cancelled.
type Todo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

var todoStatuses = map[string]bool{
	"pending": true, "in_progress": true, "completed": true, "cancelled": true,
}

// todos is the agent's plan store. Full-list rewrite on every call; the turn
// loop reads it per round for injection.
type todos struct {
	mu    sync.Mutex
	items []Todo
}

// set validates and replaces the whole list. It reports what changed so the
// tool result tells the model how much open work remains.
func (t *todos) set(items []Todo) (open int, err error) {
	if len(items) > maxTodos {
		return 0, fmt.Errorf("list exceeds %d items; consolidate steps", maxTodos)
	}
	seen := map[string]bool{}
	inProgress := 0
	for i, it := range items {
		it.Content = strings.TrimSpace(it.Content)
		if it.Content == "" || len(it.Content) > maxTodoContent {
			return 0, fmt.Errorf("todo %d needs non-empty content of at most %d chars", i+1, maxTodoContent)
		}
		if !todoStatuses[it.Status] {
			return 0, fmt.Errorf("todo %d has invalid status %q (pending|in_progress|completed|cancelled)", i+1, it.Status)
		}
		if it.ID == "" {
			it.ID = fmt.Sprintf("t%d", i+1)
		}
		if seen[it.ID] {
			return 0, fmt.Errorf("duplicate todo id %q", it.ID)
		}
		seen[it.ID] = true
		if it.Status == "in_progress" {
			inProgress++
		}
		items[i] = it
	}
	if inProgress > 1 {
		return 0, fmt.Errorf("keep exactly one item in_progress (%d given)", inProgress)
	}
	t.mu.Lock()
	t.items = items
	t.mu.Unlock()
	for _, it := range items {
		if it.Status == "pending" || it.Status == "in_progress" {
			open++
		}
	}
	return open, nil
}

// block renders open items as the per-round injection; "" when there is
// nothing open (a finished or empty plan spends no prompt space).
func (t *todos) block() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	open := 0
	for _, it := range t.items {
		if it.Status == "pending" || it.Status == "in_progress" {
			open++
			fmt.Fprintf(&b, "- [%s] %s: %s\n", it.Status, it.ID, it.Content)
		}
	}
	if open == 0 {
		return ""
	}
	return "Your current plan (from todowrite). Keep it updated: rewrite the full list each call, keep one item in_progress, mark items completed only once verified.\n\n" + strings.TrimRight(b.String(), "\n")
}

// todosFor returns the plan store, lazily allocating it so Agent literals
// built without New (tests, resumed sessions) are safe too.
func (a *Agent) todosFor() *todos {
	if a.todos == nil {
		a.todos = &todos{}
	}
	return a.todos
}

// TodosJSON serializes the current plan for session persistence ("" when empty).
func (a *Agent) TodosJSON() string {
	t := a.todosFor()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.items) == 0 {
		return ""
	}
	b, err := json.Marshal(t.items)
	if err != nil {
		return ""
	}
	return string(b)
}

// LoadTodosJSON restores a persisted plan (best-effort: a corrupt blob loads
// as an empty plan, which the model can simply rewrite).
func (a *Agent) LoadTodosJSON(s string) {
	if s == "" {
		return
	}
	var items []Todo
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return
	}
	a.todosFor().mu.Lock()
	a.todos.items = items
	a.todos.mu.Unlock()
}

// todoTool registers the model-facing todowrite tool on the agent.
func todoTool(a *Agent) tools.Tool {
	return tools.Tool{
		Def: llm.NewTool(toolNameTodowrite,
			"Record and update your plan for this conversation. Rewrite the FULL list on every call — the list you send replaces the previous one and open items are shown back to you each round. Use it for any task needing 3 or more steps; skip it for trivial one-step work. Keep exactly one item in_progress and mark items completed only after verifying they are actually done. Send an empty list to clear it.",
			`{"type":"object","properties":{"todos":{"type":"array","description":"The full, updated plan.","items":{"type":"object","properties":{"id":{"type":"string","description":"Stable id, e.g. t1 (assigned if omitted)"},"content":{"type":"string","description":"The step, phrased as an imperative"},"status":{"type":"string","enum":["pending","in_progress","completed","cancelled"]}},"required":["content","status"]}}},"required":["todos"]}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Todos []Todo `json:"todos"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", err
			}
			open, err := a.todosFor().set(in.Todos)
			if err != nil {
				return "", err
			}
			if len(in.Todos) == 0 {
				return "Plan cleared.", nil
			}
			return fmt.Sprintf("Plan updated: %d item(s), %d open.", len(in.Todos), open), nil
		},
	}
}
