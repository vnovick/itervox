package extsessions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestExternalSessionInspectionIsReadOnly is the structural guard of #178
// step 3: discovery, transcripts and the liveness check only read. No file
// is created, written, renamed or removed; no process is signalled, killed
// or attached to; and the only commands run are `ps` and lsof (by path).
func TestExternalSessionInspectionIsReadOnly(t *testing.T) {
	forbidden := map[string]bool{
		"os.Create": true, "os.OpenFile": true, "os.WriteFile": true, "os.Remove": true, "os.RemoveAll": true,
		"os.Rename": true, "os.Chmod": true, "os.Chtimes": true, "os.Truncate": true, "os.Symlink": true,
		"os.Mkdir": true, "os.MkdirAll": true, "os.FindProcess": true, "os.Pipe": true,
		"syscall.Kill": true, "syscall.PtraceAttach": true, "syscall.Open": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	commands := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			at := name + ":" + strconv.Itoa(fset.Position(call.Pos()).Line)
			if pkg, ok := sel.X.(*ast.Ident); ok {
				if forbidden[pkg.Name+"."+sel.Sel.Name] {
					violations = append(violations, at+": "+pkg.Name+"."+sel.Sel.Name)
				}
				if pkg.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
					commands++
					arg := call.Args[0]
					if sel.Sel.Name == "CommandContext" {
						arg = call.Args[1]
					}
					lit, isLit := arg.(*ast.BasicLit)
					ident, isIdent := arg.(*ast.Ident)
					isPS := isLit && lit.Value == `"ps"`
					isLsof := isIdent && ident.Name == "lsof"
					if !isPS && !isLsof {
						violations = append(violations, at+": runs a command other than ps or lsof")
					}
				}
			}
			switch sel.Sel.Name {
			case "Signal", "Kill", "StdinPipe", "Write", "WriteString", "WriteAt":
				violations = append(violations, at+": ."+sel.Sel.Name)
			}
			return true
		})
	}
	if commands != 2 {
		violations = append(violations, "expected exactly the ps and lsof commands, found "+strconv.Itoa(commands))
	}
	if len(violations) > 0 {
		t.Fatalf("extsessions must only read (#178):\n  %s", strings.Join(violations, "\n  "))
	}
}
