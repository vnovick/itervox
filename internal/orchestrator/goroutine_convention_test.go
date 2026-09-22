package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestEventLoopGoroutinesAreWaitgroupTracked enforces that every `go func(...)`
// inside `internal/orchestrator/event_loop.go` is preceded (in the same
// function body) by an `Add(1)` call on one of the orchestrator's tracked
// WaitGroups (autoClearWg, discardWg, commentWg). The corresponding `Done`
// call typically lives at the top of the goroutine body.
//
// Without this guard, the bug class fixed by T-44 (commentWg for
// tracker-comment posters) and T-49 (StartupTerminalCleanup wait closure)
// can recur silently — a future contributor adds a goroutine that posts a
// tracker comment, forgets to Add(1), and shutdown drops the comment.
//
// Exemptions:
//   - `go o.runWorker(...)` — workers are tracked separately via the
//     `workerCancels` map and join on cancellation, not a WaitGroup.
//   - `StartupTerminalCleanup` — public function that returns a `wait()`
//     closure whose semantics are caller-controlled (T-49).
//
// G-08 (gaps_280426_2).
//
// T3 (input_required_outbox_plan final fix wave) extended this guard to also
// scan automation_rate_limited.go, after finding a second untracked `go
// o.saveAutoSwitchedToDisk(...)` goroutine there with the exact same shape
// as the one T-49-era work fixed in event_loop.go. The scan loops over a
// file list so a future file with the same class of goroutine is one line
// to add, not a second copy of this test.
func TestEventLoopGoroutinesAreWaitgroupTracked(t *testing.T) {
	files := []string{"event_loop.go", "automation_rate_limited.go"}

	knownWGs := map[string]struct{}{
		"autoClearWg": {},
		"discardWg":   {},
		"commentWg":   {},
	}

	var violations []string

	for _, filename := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		violations = append(violations, checkGoroutinesTracked(fset, f, filename, knownWGs)...)
	}

	if len(violations) > 0 {
		t.Fatalf("untracked goroutines (%d):\n  - %s",
			len(violations), strings.Join(violations, "\n  - "))
	}
}

// checkGoroutinesTracked walks filename's AST looking for `go` statements not
// preceded (lexically, within the same function body) by an Add(1) call on
// one of knownWGs. Returns one violation string per offending goroutine.
func checkGoroutinesTracked(fset *token.FileSet, f *ast.File, filename string, knownWGs map[string]struct{}) []string {
	var violations []string

	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		// Walk the function body looking for GoStmts.
		ast.Inspect(fd, func(inner ast.Node) bool {
			gostmt, ok := inner.(*ast.GoStmt)
			if !ok {
				return true
			}
			// Skip `go o.runWorker(...)` — workers have their own cleanup path
			// (workerCancels + cancelAndCleanupWorker).
			if call, ok := gostmt.Call.Fun.(*ast.SelectorExpr); ok {
				if id, ok := call.X.(*ast.Ident); ok && id.Name == "o" && call.Sel.Name == "runWorker" {
					return true
				}
			}
			// Search backwards through the function body for an Add(1) on a
			// known wait-group WITHIN the same function. The check is
			// intentionally lexical (positional) rather than data-flow: any
			// `o.<knownWG>.Add(1)` appearing earlier in the function body
			// is accepted as the matching tracker for this goroutine.
			pos := gostmt.Pos()
			found := false
			ast.Inspect(fd, func(prior ast.Node) bool {
				if found {
					return false
				}
				if prior == nil || prior.Pos() >= pos {
					return true
				}
				call, ok := prior.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "Add" {
					return true
				}
				// Match `o.<wg>.Add(...)` shape.
				wgSel, ok := sel.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := wgSel.X.(*ast.Ident); !ok || id.Name != "o" {
					return true
				}
				if _, known := knownWGs[wgSel.Sel.Name]; known {
					found = true
				}
				return true
			})
			if !found {
				// Skip `StartupTerminalCleanup` (caller-controlled wait closure).
				if fd.Name != nil && fd.Name.Name == "StartupTerminalCleanup" {
					return true
				}
				pos := fset.Position(gostmt.Pos())
				violations = append(violations,
					"go func at "+pos.String()+" in "+fd.Name.Name+
						": no preceding o.{autoClearWg|discardWg|commentWg}.Add(1) found",
				)
			}
			return true
		})
		return true
	})

	return violations
}
