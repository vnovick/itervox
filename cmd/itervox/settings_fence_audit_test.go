package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// settingsFenceRoots are the cmd/itervox entry points allowed to reach a
// WORKFLOW.md patcher without beginSettingsSave, each with its reason. They
// run in a separate `itervox <subcommand>` process, not inside a daemon run
// generation, so there is no settings generation to fence against; the
// patchers still take the per-path edit lock themselves.
var settingsFenceRoots = map[string]string{
	"runModelsRefresh": "`itervox models refresh` CLI process, no daemon generation",
}

// isWorkflowPatcher reports whether a call is one of the registered
// internal/workflow writers: every workflow.Patch* function and
// workflow.ApplyAndWriteFrontMatter (the Mutate* functions are pure and
// write nothing).
func isWorkflowPatcher(sel *ast.SelectorExpr) bool {
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "workflow" {
		return false
	}
	return strings.HasPrefix(sel.Sel.Name, "Patch") || sel.Sel.Name == "ApplyAndWriteFrontMatter"
}

type fenceUnit struct {
	name    string // FuncDecl name ("" for a function literal)
	pos     token.Position
	body    *ast.BlockStmt
	fenced  bool
	callees []string // local funcs and workflow patchers this unit calls directly
	writes  []token.Position
	parent  *fenceUnit // enclosing unit of a function literal
	goStmt  bool       // the literal is launched with `go` (runs after the caller returns)
}

// TestWorkflowPatchersAreFencedBySettingsSave (CORE-171): every production
// path in cmd/itervox that reaches a registered workflow patcher goes through
// beginSettingsSave (the generation fence plus the per-path lock), directly
// in the calling function (or function literal) or in every caller of a
// wrapper that performs the write. It fails naming each unfenced path.
func TestWorkflowPatchersAreFencedBySettingsSave(t *testing.T) {
	violations := auditSettingsFence(t, ".")
	if len(violations) > 0 {
		t.Fatalf("workflow patchers reached without beginSettingsSave:\n  %s", strings.Join(violations, "\n  "))
	}
}

func auditSettingsFence(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var units []*fenceUnit
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				if fn.Body != nil {
					units = append(units, &fenceUnit{name: fn.Name.Name, pos: fset.Position(fn.Pos()), body: fn.Body})
				}
			case *ast.FuncLit:
				units = append(units, &fenceUnit{pos: fset.Position(fn.Pos()), body: fn.Body})
			}
			return true
		})
	}
	byBody := map[*ast.BlockStmt]*fenceUnit{}
	for _, u := range units {
		byBody[u.body] = u
	}
	// Each unit sees only its own statements: nested function literals are
	// their own units, linked to their parent.
	for _, u := range units {
		ast.Inspect(u.body, func(n ast.Node) bool {
			if g, ok := n.(*ast.GoStmt); ok {
				if lit, ok := g.Call.Fun.(*ast.FuncLit); ok {
					byBody[lit.Body].goStmt = true
				}
			}
			if lit, ok := n.(*ast.FuncLit); ok && lit.Body != u.body {
				byBody[lit.Body].parent = u
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				if f.Name == "beginSettingsSave" {
					u.fenced = true
				}
				u.callees = append(u.callees, f.Name)
			case *ast.SelectorExpr:
				if f.Sel.Name == "beginSettingsSave" {
					u.fenced = true
				}
				if isWorkflowPatcher(f) {
					u.writes = append(u.writes, fset.Position(call.Pos()))
				} else {
					u.callees = append(u.callees, f.Sel.Name)
				}
			}
			return true
		})
	}
	// A function literal runs inside its parent's fence — a rollback closure,
	// a TUI callback that calls beginSettingsSave itself — unless it is
	// launched with `go`, which outlives the caller's unlock.
	for changed := true; changed; {
		changed = false
		for _, u := range units {
			if !u.fenced && u.parent != nil && u.parent.fenced && !u.goStmt {
				u.fenced = true
				changed = true
			}
		}
	}
	// A named unit that writes (directly or through another such unit)
	// without fencing is a wrapper: its callers must fence instead.
	wrappers := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, u := range units {
			if u.name == "" || u.fenced || wrappers[u.name] {
				continue
			}
			if _, root := settingsFenceRoots[u.name]; root {
				continue // an allowed root does not make its callers writers
			}
			writes := len(u.writes) > 0
			for _, c := range u.callees {
				writes = writes || wrappers[c]
			}
			if writes {
				wrappers[u.name] = true
				changed = true
			}
		}
	}
	var violations []string
	for _, u := range units {
		if u.fenced {
			continue
		}
		reaches := len(u.writes) > 0
		var via []string
		for _, c := range u.callees {
			if wrappers[c] {
				reaches = true
				via = append(via, c)
			}
		}
		if !reaches {
			continue
		}
		if u.name != "" {
			if _, root := settingsFenceRoots[u.name]; root {
				continue
			}
			if !isCalledSomewhere(units, u.name) {
				violations = append(violations, fmt.Sprintf("%s: %s writes WORKFLOW.md unfenced and nothing fences it", u.pos, u.name))
			}
			continue // a wrapper: its callers are audited
		}
		violations = append(violations, fmt.Sprintf("%s: function literal writes WORKFLOW.md without beginSettingsSave (via %v)", u.pos, via))
	}
	for root := range settingsFenceRoots {
		found := false
		for _, u := range units {
			found = found || u.name == root
		}
		if !found {
			violations = append(violations, "settingsFenceRoots entry "+root+" matches no function (stale)")
		}
	}
	sort.Strings(violations)
	return violations
}

func isCalledSomewhere(units []*fenceUnit, name string) bool {
	for _, u := range units {
		for _, c := range u.callees {
			if c == name {
				return true
			}
		}
	}
	return false
}
