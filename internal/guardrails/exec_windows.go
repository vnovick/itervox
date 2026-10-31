//go:build windows

package guardrails

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/procgroup"
)

// hookArgv is the program and arguments that run the repository's hook at
// path; prog is "" when there is none. Git hooks are sh scripts with no
// executable extension, which Windows cannot start directly, so the hook
// runs under procgroup.Shell's bash — as git for Windows itself runs hooks.
func hookArgv(hook string, args []string) (prog string, argv []string, err error) {
	if info, err := os.Stat(hook); err != nil || info.IsDir() {
		return "", nil, nil
	}
	shell := procgroup.Shell("bash")
	if shell == "" {
		return "", nil, errors.New("no bash found (install Git for Windows or set ITERVOX_SHELL)")
	}
	return shell, append([]string{hook}, args...), nil
}

// execProgram runs path as a child with this process's stdio and returns
// its exit status: Windows has no exec(2), so the shim stays as the parent
// until the real program exits and then exits with its status.
func execProgram(path string, argv, env []string) (int, error) {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// executableCandidates are the files resolveReal tries for tool in dir:
// tool with each %PATHEXT% extension, in order (as CreateProcess's search
// would), so `git` finds git.exe and the guardrail's own git.cmd wrapper
// is recognised and skipped.
func executableCandidates(dir, tool string) []string {
	exts := strings.Split(strings.ToLower(os.Getenv("PATHEXT")), ";")
	if os.Getenv("PATHEXT") == "" {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	}
	var out []string
	if filepath.Ext(tool) != "" {
		out = append(out, filepath.Join(dir, tool))
	}
	for _, e := range exts {
		if e != "" {
			out = append(out, filepath.Join(dir, tool+e))
		}
	}
	return out
}

// isExecutableMode: Windows has no execute bit; the extension decides.
func isExecutableMode(os.FileInfo) bool { return true }
