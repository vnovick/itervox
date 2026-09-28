package gitexec

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestScrubRemovesRepoLocationVarsAndKeepsAuth(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/victim/.git",
		"GIT_WORK_TREE=/victim",
		"GIT_INDEX_FILE=/victim/.git/index",
		"GIT_OBJECT_DIRECTORY=/victim/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/victim/.git/objects",
		"GIT_COMMON_DIR=/victim/.git",
		"GIT_NAMESPACE=ns",
		"GIT_CEILING_DIRECTORIES=/",
		"GIT_PREFIX=sub/",
		"GIT_CONFIG_PARAMETERS='core.bare'='true'",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.bare",
		"GIT_CONFIG_VALUE_0=true",
		"GIT_QUARANTINE_PATH=/victim/.git/objects/incoming",
		"GIT_SSH_COMMAND=ssh -i key",
		"GIT_ASKPASS=/bin/askpass",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=a",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_DIRECTORY_LOOKALIKE=keep", // prefix match must not over-scrub
	}
	got := Scrub(in)
	want := []string{
		"PATH=/usr/bin",
		"GIT_SSH_COMMAND=ssh -i key",
		"GIT_ASKPASS=/bin/askpass",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=a",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_DIRECTORY_LOOKALIKE=keep",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Scrub:\n got  %q\n want %q", got, want)
	}
}

// TestRepoLocationVarsCoverGitLocalEnvVars pins the list to git's own
// definition: every name `git rev-parse --local-env-vars` prints must be
// scrubbed. A newer git that adds one fails here instead of silently leaking.
func TestRepoLocationVarsCoverGitLocalEnvVars(t *testing.T) {
	out, err := Command(context.Background(), t.TempDir(), "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	for _, name := range strings.Fields(string(out)) {
		if !IsRepoLocationVar(name) {
			t.Errorf("git reports %s as a local-repo env var but gitexec does not scrub it", name)
		}
	}
}

func TestCommandScrubsEnvAndSetsDir(t *testing.T) {
	t.Setenv("GIT_DIR", "/victim/.git")
	t.Setenv("GIT_WORK_TREE", "/victim")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i key")
	dir := t.TempDir()
	cmd := Command(context.Background(), dir, "status")
	if cmd.Dir != dir {
		t.Fatalf("Dir = %q, want %q", cmd.Dir, dir)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") {
			t.Fatalf("scrubbed env still carries %s", kv)
		}
	}
	if !slices.Contains(cmd.Env, "GIT_SSH_COMMAND=ssh -i key") {
		t.Fatal("auth variable GIT_SSH_COMMAND must be kept")
	}
}

func TestCommandRefusesEmptyDir(t *testing.T) {
	err := Command(context.Background(), "", "status").Run()
	if err == nil || !strings.Contains(err.Error(), "explicit working directory") {
		t.Fatalf("Run with empty dir: err = %v, want errNoDir", err)
	}
}

func TestUnsetInProcess(t *testing.T) {
	t.Setenv("GIT_DIR", "/victim/.git")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_SSH_COMMAND", "ssh")
	UnsetInProcess()
	for _, k := range []string{"GIT_DIR", "GIT_CONFIG_KEY_0"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set after UnsetInProcess", k)
		}
	}
	if os.Getenv("GIT_SSH_COMMAND") != "ssh" {
		t.Error("UnsetInProcess must keep GIT_SSH_COMMAND")
	}
}

// TestNoRawGitExecOutsideGitexec is the structural guard: no Go file under
// cmd/ or internal/ (production OR test) may build a git subprocess except
// through this package. It flags
//
//   - exec.Command("git", …) / exec.CommandContext(ctx, "git", …), and
//   - any []string literal whose first element is "git" — the argv-table
//     shape (exec.Command(args[0], args[1:]...)) that hides the name from
//     the first check.
//
// A raw call inherits GIT_DIR/GIT_WORK_TREE from a git hook and runs against
// the enclosing repository; that is how a pre-push `make test` once rewrote
// a developer's checkout.
func TestNoRawGitExecOutsideGitexec(t *testing.T) {
	var violations []string
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || filepath.Dir(path) == filepath.Clean("../../internal/gitexec") {
				return nil
			}
			violations = append(violations, rawGitExecs(t, path)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("git must be run via gitexec.Command (scrubs GIT_DIR & co, requires a dir); raw uses:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

func rawGitExecs(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	execName := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			execName = "exec"
			if imp.Name != nil {
				execName = imp.Name.Name
			}
		}
	}
	isGit := func(e ast.Expr) bool {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
		s, _ := strconv.Unquote(lit.Value)
		return s == "git"
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || execName == "" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != execName {
				return true
			}
			nameIdx := -1
			switch sel.Sel.Name {
			case "Command":
				nameIdx = 0
			case "CommandContext":
				nameIdx = 1
			}
			if nameIdx >= 0 && len(n.Args) > nameIdx && isGit(n.Args[nameIdx]) {
				out = append(out, fset.Position(n.Pos()).String()+": exec."+sel.Sel.Name+"(\"git\", …)")
			}
		case *ast.CompositeLit:
			if len(n.Elts) > 0 && isGit(n.Elts[0]) {
				out = append(out, fset.Position(n.Pos()).String()+": argv literal starting with \"git\"")
			}
		}
		return true
	})
	return out
}
