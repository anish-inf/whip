// Package uilock is a go/analysis analyzer that catches whip's TUI
// self-deadlock: a synchronous (*tea.Program).Send call that can run on
// bubbletea's UI event-loop goroutine.
//
// Background: bubbletea's Program.Send blocks writing to the event loop's
// unbuffered msgs channel until the loop reads it. The loop is also the
// goroutine that runs Update. So if any code reachable from Update — a key
// handler, a palette action, or a callback Update invokes synchronously (like
// the MCP manager's OnChange, which mcpSetImport fires from inside Update) —
// calls prog.Send directly, the loop waits on itself and the TUI freezes
// permanently (the frozen ctrl+p → MCPs incident).
//
// The rule: a (*tea.Program).Send call in internal/tui must be wrapped in a
// go statement (detached onto its own goroutine). Because "reachable from the
// event loop" isn't cheaply decidable, the analyzer is conservative: it flags
// EVERY synchronous Send. Sites that provably run on a background goroutine
// (the permission gate, catalog fetch, the interactive PTY runner, shell,
// compaction, title fetch) opt out with a `//nolint:uilock` comment on the
// Send line, which this analyzer honors textually.
//
// ponytail: intentionally a local, syntactic check (Send call not under a
// GoStmt), not a call-graph reachability proof — reachability needs
// whole-program pointer analysis; the syntactic rule + a short whitelist
// catches the recurring mistake at review time with zero false negatives on
// the UI path.
package uilock

import (
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer flags synchronous (*tea.Program).Send calls in the TUI package.
var Analyzer = &analysis.Analyzer{
	Name:     "uilock",
	Doc:      "flags synchronous tea.Program.Send calls that can deadlock the TUI event loop",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

const sendMethod = "Send"

func run(pass *analysis.Pass) (any, error) {
	// Only the TUI drives a *tea.Program's event loop directly; other packages
	// never touch bubbletea, so skip them entirely.
	if !strings.Contains(pass.Pkg.Path(), "internal/tui") {
		return nil, nil //nolint:nilnil // go/analysis convention: (nil, nil) means "no findings"
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{(*ast.CallExpr)(nil)}
	insp.WithStack(nodeFilter, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push {
			return true
		}
		call := n.(*ast.CallExpr)
		// Test files never run the production event loop — their Sends drive a
		// program from the test goroutine, so the deadlock can't occur.
		if strings.HasSuffix(pass.Fset.Position(call.Pos()).Filename, "_test.go") {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != sendMethod {
			return true
		}
		// Confirm the receiver is a *tea.Program (or whip's alias for it).
		t := pass.TypesInfo.TypeOf(sel.X)
		if t == nil || !strings.HasSuffix(t.String(), "tea.Program") {
			return true
		}
		// `go x.Send(...)` is detached and safe: the call's parent is a GoStmt.
		if detached(stack) {
			return true
		}
		// A background-goroutine sender can opt out with //nolint:uilock on the
		// Send line.
		if hasNolint(pass, stack) {
			return true
		}
		pass.Reportf(call.Pos(),
			"synchronous (*tea.Program).Send can deadlock the TUI event loop: detach it (go x.Send(...)) or, if this runs on a background goroutine, mark it //nolint:uilock")
		return true
	})
	return nil, nil //nolint:nilnil // go/analysis convention: (nil, nil) means "no findings"
}

// detached reports whether the Send runs on a spawned goroutine. Two shapes:
// the direct `go x.Send(...)` (the CallExpr's parent is a GoStmt), and
// `go func() { … x.Send(…) … }()` (the CallExpr sits inside a FuncLit that a
// GoStmt launches). We check the direct case first, then walk outward from
// the innermost FuncLit; we stop at a FuncDecl, because a named function's
// goroutine-ness isn't decidable locally (those senders use //nolint:uilock).
func detached(stack []ast.Node) bool {
	// Direct `go x.Send(...)`: parent of the CallExpr is a GoStmt.
	if len(stack) >= 2 {
		if _, isGo := stack[len(stack)-2].(*ast.GoStmt); isGo {
			return true
		}
	}
	// Closure case: an enclosing FuncLit spawned by a GoStmt. In
	// `go func() { … }()` the FuncLit's parent is the invocation CallExpr
	// (func(){}()), and the GoStmt is one level further out — so we look for
	// the pattern [… GoStmt, CallExpr, FuncLit …] and stop at a FuncDecl.
	for i := len(stack) - 1; i >= 2; i-- {
		if _, isLit := stack[i].(*ast.FuncLit); isLit {
			_, inv := stack[i-1].(*ast.CallExpr)
			_, isGo := stack[i-2].(*ast.GoStmt)
			if inv && isGo {
				return true
			}
		}
		if _, isDecl := stack[i].(*ast.FuncDecl); isDecl {
			return false
		}
	}
	return false
}

// hasNolint reports whether the line containing the call (or the line directly
// above it) carries a //nolint:uilock comment. Comments aren't attached to
// statements in the AST, so we match on source lines via the file's comments.
func hasNolint(pass *analysis.Pass, stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	call := stack[len(stack)-1]
	fset := pass.Fset
	line := fset.Position(call.Pos()).Line
	file := fset.File(call.Pos())
	if file == nil {
		return false
	}
	// Find the file's comments and check the call's line and the one above.
	for _, f := range pass.Files {
		if fset.File(f.Pos()) != file {
			continue
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				cl := fset.Position(c.Pos()).Line
				if (cl == line || cl == line-1) && strings.Contains(c.Text, "nolint:uilock") {
					return true
				}
			}
		}
	}
	_ = token.NoPos // keep token import if unused in future edits
	return false
}
