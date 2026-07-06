package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestAdapterPersistThenMutateConvention enforces the persist-then-mutate
// invariant on every *orchestratorAdapter setter that mutates runtime config:
// the WORKFLOW.md persistence call (workflow.Patch* / workflow.ApplyAndWriteFrontMatter)
// MUST appear before the orch.*Cfg runtime mutation. If the persist fails, no
// in-memory state should change — otherwise the dashboard returns 200 while
// the next daemon restart silently reverts the user's action.
//
// The original audit (gaps_210426 G-05) caught two setters violating this.
// T-05 fixed them; this test pins the invariant so a future regression is
// caught at lint time, not at user-report time.
func TestAdapterPersistThenMutateConvention(t *testing.T) {
	fset := token.NewFileSet()
	// G-12 (gaps_280426_2): walk both `main.go` and `adapter_settings.go` —
	// the setter cluster was extracted to a sibling file but the convention
	// invariant still applies to every method on *orchestratorAdapter.
	files := []string{"main.go", "adapter_settings.go"}
	parsed := make([]*ast.File, 0, len(files))
	for _, name := range files {
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed = append(parsed, f)
	}

	// Setters explicitly exempt from the persist-first rule. Each entry must
	// document WHY in a comment so a future contributor can challenge the
	// exemption rather than copy-paste it.
	exempt := map[string]string{
		// Per-issue runtime overrides (user-set via dashboard, never written to
		// WORKFLOW.md by design — the override is intentionally non-persistent).
		"SetIssueProfile": "per-issue runtime override; not persisted to WORKFLOW.md",
		"SetIssueBackend": "per-issue runtime override; not persisted to WORKFLOW.md",
		// Workspace lifecycle helpers (no config change, just workspace cleanup).
		"ClearAllWorkspaces": "no config change; pure workspace fs cleanup",
		// Per-issue runtime input/control (not config).
		"DismissInput": "per-issue runtime control; not config",
	}

	var violations []string
	var checked int

	walk := func(f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Recv.NumFields() == 0 {
				return true
			}
			// Only inspect methods on *orchestratorAdapter.
			recvType, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				return true
			}
			recvIdent, ok := recvType.X.(*ast.Ident)
			if !ok || recvIdent.Name != "orchestratorAdapter" {
				return true
			}

			name := fd.Name.Name
			if !isSetter(name) {
				return true
			}
			if _, isExempt := exempt[name]; isExempt {
				return true
			}

			// Find positions of the first workflow persist call and the first orch.*Cfg
			// mutation call within the function body.
			var persistPos, mutatePos token.Pos
			ast.Inspect(fd, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				selExpr := callSelector(call)
				if selExpr == "" {
					return true
				}
				if persistPos == 0 && isPersistCall(selExpr) {
					persistPos = call.Pos()
				}
				if mutatePos == 0 && isOrchMutation(selExpr) {
					mutatePos = call.Pos()
				}
				return true
			})

			checked++

			switch {
			case mutatePos == 0:
				// Setter that doesn't mutate orch state at all (e.g. SetReviewerConfig
				// might delegate entirely to a helper). Allowed; nothing to enforce.
				return true
			case persistPos == 0:
				violations = append(violations, name+": calls orch mutation but no workflow.Patch* / ApplyAndWriteFrontMatter persist call found")
			case persistPos > mutatePos:
				violations = append(violations, name+": orch mutation at pos "+fset.Position(mutatePos).String()+" precedes persist at pos "+fset.Position(persistPos).String())
			}
			return true
		})
	}
	for _, f := range parsed {
		walk(f)
	}

	if checked == 0 {
		t.Fatal("AST walk found 0 setters — test is broken (or all methods are exempt)")
	}
	if len(violations) > 0 {
		t.Fatalf("persist-then-mutate convention violated by %d setter(s):\n  - %s",
			len(violations), strings.Join(violations, "\n  - "))
	}
}

// isSetter returns true if name matches the setter naming convention used by
// the adapter (Set*, Update*, Add*, Remove*, Bump*, Upsert*, Delete*).
func isSetter(name string) bool {
	for _, prefix := range []string{"Set", "Update", "Add", "Remove", "Bump", "Upsert", "Delete"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// callSelector returns the dotted form of a method call's selector
// (e.g. "workflow.PatchIntField", "a.orch.SetMaxWorkers") or the bare
// function name for an unqualified call (e.g. "persistTrackerStates"),
// or "" if the call is something we don't recognise.
func callSelector(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		parts := []string{fn.Sel.Name}
		x := fn.X
		for {
			switch v := x.(type) {
			case *ast.SelectorExpr:
				parts = append([]string{v.Sel.Name}, parts...)
				x = v.X
			case *ast.Ident:
				parts = append([]string{v.Name}, parts...)
				return strings.Join(parts, ".")
			default:
				return strings.Join(parts, ".")
			}
		}
	}
	return ""
}

// isPersistCall returns true if the selector dotted form names a WORKFLOW.md
// persistence helper (workflow.Patch* or workflow.ApplyAndWriteFrontMatter,
// or the local persistTrackerStates helper which wraps it).
func isPersistCall(sel string) bool {
	switch {
	case strings.HasPrefix(sel, "workflow.Patch"):
		return true
	case sel == "workflow.ApplyAndWriteFrontMatter":
		return true
	// T-30: the typed `workflow.NewDoc(path).Set*().Save()` builder is also a
	// persist site. NewDoc opens the chain that ends in an atomic Save; for
	// ordering purposes, any code following the NewDoc(...) call has already
	// committed to a persist before mutating runtime state.
	case sel == "workflow.NewDoc":
		return true
	case sel == "persistTrackerStates":
		return true
	case sel == "persistReviewerConfig":
		return true
	case sel == "persistMaxRetries":
		return true
	case sel == "persistFailedState":
		return true
	case sel == "persistMaxSwitchesPerIssuePerWindow":
		return true
	case sel == "persistSwitchWindowHours":
		return true
	}
	return false
}

// isOrchMutation returns true if the selector names a runtime mutation on the
// orchestrator (a.orch.Set*Cfg, a.orch.Add*Cfg, a.orch.Remove*Cfg, etc.).
func isOrchMutation(sel string) bool {
	if !strings.HasPrefix(sel, "a.orch.") {
		return false
	}
	method := strings.TrimPrefix(sel, "a.orch.")
	for _, prefix := range []string{"Set", "Add", "Remove", "Update", "Upsert", "Delete"} {
		if strings.HasPrefix(method, prefix) {
			return true
		}
	}
	return false
}
