//go:build unix

package guardrails

import (
	"os"
	"path/filepath"
	"syscall"
)

// hookArgv is the program and arguments that run the repository's hook at
// path; prog is "" when there is no executable hook there.
func hookArgv(hook string, args []string) (prog string, argv []string, err error) {
	if info, err := os.Stat(hook); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", nil, nil
	}
	return hook, args, nil
}

// execProgram replaces the process with path. It returns only on failure.
func execProgram(path string, argv, env []string) (int, error) {
	return 0, syscall.Exec(path, argv, env)
}

// executableCandidates are the files resolveReal tries for tool in dir.
func executableCandidates(dir, tool string) []string {
	return []string{filepath.Join(dir, tool)}
}

// isExecutableMode reports whether a candidate's mode lets it run.
func isExecutableMode(info os.FileInfo) bool {
	return info.Mode()&0o111 != 0
}
