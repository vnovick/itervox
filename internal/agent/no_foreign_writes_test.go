package agent_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestNoWritesToProcessesItervoxDidNotStart is the structural guard of the
// Sessions panel invariant (#178): itervox writes input only to sessions it
// owns — the stdin of a process it started itself — with ONE narrow
// exception: internal/tmuxctl, which types into an agent CLI running in a
// tmux pane through tmux's own paste buffer and key events, opt-in
// (sessions.tmux_control) or for tmux sessions itervox started. Nothing
// ever injects into a terminal device (TIOCSTI, /dev/pts), drives another
// app through UI automation (AppleScript System Events keystrokes, the
// Accessibility API, CGEvent, xdotool) or opens another process's stdin
// through /proc. Checked over every non-test Go file under cmd/ and
// internal/ and every Rust file of the Mac app:
//
//   - no forbidden token in a string literal, identifier or Rust source;
//     the tmux write commands (send-keys, paste-buffer, load-buffer) only in
//     internal/tmuxctl;
//   - `tmux` is executed only by internal/tmuxctl, and internal/tmuxctl
//     executes nothing but `tmux` (a literal program name, explicit
//     arguments);
//   - every stdin wiring (x.Stdin = …, x.ExtraFiles = …, x.StdinPipe())
//     is on an *exec.Cmd the same function created with exec.Command or
//     exec.CommandContext, or inside an allowlisted helper that is handed
//     such a command by its caller (which is checked);
//   - os.Pipe — the live-steering stdin — only where the steering writer
//     lives.
func TestNoWritesToProcessesItervoxDidNotStart(t *testing.T) {
	var violations []string
	for _, root := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			violations = append(violations, foreignWriteViolations(t, path)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	violations = append(violations, rustForeignWriteViolations(t, "../../desktop/src-tauri/src")...)
	if tmuxExecs == 0 {
		violations = append(violations, "internal/tmuxctl: expected its exec of tmux to be found")
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("itervox may write only to processes it started (#178); found:\n  %s", strings.Join(violations, "\n  "))
	}
}

// tmuxctlDir is the one package allowed to write into a tmux pane.
const tmuxctlDir = "internal/tmuxctl/"

// tmuxWriteTokens are tmux's write commands: allowed in tmuxctlDir only.
var tmuxWriteTokens = []string{"send-keys", "paste-buffer", "load-buffer"}

// tmuxExecs counts tmuxctl's exec sites of tmux (at least one must exist,
// so the check cannot pass by not finding the package).
var tmuxExecs int

// forbiddenWriteTokens name the mechanisms that write into a process
// itervox did not start. Matched case-insensitively in Go string literals
// and identifiers, and in Rust sources.
var forbiddenWriteTokens = []string{
	"tiocsti",       // push bytes into another terminal's input queue
	"/dev/pts",      // another terminal
	"send-keys",     // tmux / screen keystroke injection (tmuxctl only)
	"paste-buffer",  // tmux paste (tmuxctl only)
	"load-buffer",   // tmux paste buffer (tmuxctl only)
	"xdotool",       // X11 keystroke injection
	"system events", // AppleScript UI scripting (keystroke, key code)
	"keystroke",
	"axuielement", // macOS Accessibility API
	"cgeventpost", // synthesized keyboard events
	"cgevent",
	"/fd/0", // another process's stdin through /proc
	"ptraceattach",
	"ptracepokedata",
}

// devTTYAllowed: the TUI resets its own controlling terminal (`stty sane`
// on /dev/tty) after Bubbletea exits; nothing else opens /dev/tty.
var devTTYAllowed = map[string]string{
	"internal/statusui/statusui.go":       "stty sane on the daemon's own controlling terminal",
	"internal/statusui/tty_guard_unix.go": "restores the daemon's own terminal mode",
}

// stdinHelpers are functions handed an *exec.Cmd by their caller; the
// caller's own creation of that command is what the check verifies.
var stdinHelpers = map[string]string{
	"internal/agent/prompt_stdin.go:setPromptStdin": "the prompt file of a command RunTurn created",
	"internal/agent/prompt_stdin.go:setPromptFD3":   "the prompt file of a command RunTurn created",
	"internal/agent/ssh.go:attachRemoteStdin":       "the script pipe of the ssh command RunTurn created",
}

// osPipeAllowed: the live-steering stdin of a claude turn RunTurn starts.
var osPipeAllowed = map[string]string{
	"internal/agent/steer.go": "newLiveInput: stdin of the claude turn RunTurn starts",
}

func relPath(path string) string {
	p := filepath.ToSlash(filepath.Clean(path))
	return strings.TrimPrefix(p, "../../")
}

func foreignWriteViolations(t *testing.T, path string) []string {
	t.Helper()
	rel := relPath(path)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	execName, osName := "", ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch p {
		case "os/exec":
			execName = "exec"
			if name != "" {
				execName = name
			}
		case "os":
			osName = "os"
			if name != "" {
				osName = name
			}
		}
	}
	var out []string
	pos := func(n ast.Node) string { return rel + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) }

	// Tokens in string literals and identifiers.
	ast.Inspect(f, func(n ast.Node) bool {
		var text string
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				text, _ = strconv.Unquote(n.Value)
			}
		case *ast.Ident:
			text = n.Name
		default:
			return true
		}
		lower := strings.ToLower(text)
		for _, tok := range forbiddenWriteTokens {
			if strings.HasPrefix(rel, tmuxctlDir) && slices.Contains(tmuxWriteTokens, tok) {
				continue // the one permitted writer
			}
			if strings.Contains(lower, tok) {
				out = append(out, pos(n)+": forbidden "+strconv.Quote(tok))
			}
		}
		if strings.Contains(lower, "/dev/tty") {
			if _, ok := devTTYAllowed[rel]; !ok {
				out = append(out, pos(n)+": /dev/tty outside the TUI's own terminal reset")
			}
		}
		return true
	})

	// Stdin wiring per function.
	isExecCall := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok || execName == "" {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == execName && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
	}
	// Who runs tmux: tmuxctl only, and tmuxctl nothing else.
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isExecCall(call) {
			return true
		}
		prog := call.Args[0]
		if sel := call.Fun.(*ast.SelectorExpr); sel.Sel.Name == "CommandContext" {
			prog = call.Args[1]
		}
		lit, isLit := prog.(*ast.BasicLit)
		isTmux := isLit && lit.Value == `"tmux"`
		switch {
		case strings.HasPrefix(rel, tmuxctlDir) && !isTmux:
			out = append(out, pos(call)+": internal/tmuxctl executes something other than a literal \"tmux\"")
		case strings.HasPrefix(rel, tmuxctlDir):
			tmuxExecs++
		case isTmux:
			out = append(out, pos(call)+": tmux executed outside internal/tmuxctl")
		}
		return true
	})

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		_, helper := stdinHelpers[rel+":"+fn.Name.Name]
		created := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range n.Rhs {
					if i < len(n.Lhs) && isExecCall(rhs) {
						if id, ok := n.Lhs[i].(*ast.Ident); ok {
							created[id.Name] = true
						}
					}
				}
			case *ast.ValueSpec:
				for i, v := range n.Values {
					if i < len(n.Names) && isExecCall(v) {
						created[n.Names[i].Name] = true
					}
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || (sel.Sel.Name != "Stdin" && sel.Sel.Name != "ExtraFiles") {
						continue
					}
					if i < len(n.Rhs) {
						if id, ok := n.Rhs[i].(*ast.Ident); ok && id.Name == "nil" {
							continue // writes nothing
						}
					}
					if id, ok := sel.X.(*ast.Ident); ok && (created[id.Name] || helper) {
						continue
					}
					out = append(out, pos(n)+": "+sel.Sel.Name+" set on a command "+fn.Name.Name+" did not create")
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name == "StdinPipe" {
					if id, ok := sel.X.(*ast.Ident); !ok || (!created[id.Name] && !helper) {
						out = append(out, pos(n)+": StdinPipe on a command "+fn.Name.Name+" did not create")
					}
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && osName != "" && pkg.Name == osName && sel.Sel.Name == "Pipe" {
					if _, ok := osPipeAllowed[rel]; !ok {
						out = append(out, pos(n)+": os.Pipe outside the live-steering writer")
					}
				}
			}
			return true
		})
	}
	return out
}

// rustForeignWriteViolations scans the Mac app. osascript runs only the
// app's own dialogs (prompt.rs: display dialog / choose from list).
func rustForeignWriteViolations(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".rs") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		rel := relPath(path)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // prose, not code
			}
			lower := strings.ToLower(line)
			at := rel + ":" + strconv.Itoa(i+1)
			for _, tok := range append(forbiddenWriteTokens, "/dev/tty", "tell application", "key code") {
				if strings.Contains(lower, tok) {
					out = append(out, at+": forbidden "+strconv.Quote(tok))
				}
			}
			if strings.Contains(lower, "osascript") && e.Name() != "prompt.rs" {
				out = append(out, at+": osascript outside the app's own dialogs (prompt.rs)")
			}
		}
	}
	return out
}
