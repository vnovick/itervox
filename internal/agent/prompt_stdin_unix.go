//go:build unix

package agent

import (
	"fmt"
	"os"
	"os/exec"
)

// unlinkedTempFile creates promptFile's temp file and unlinks it at once:
// the open descriptor is all that is left of it.
func unlinkedTempFile() (*os.File, error) {
	f, err := os.CreateTemp("", "itervox-prompt-*")
	if err != nil {
		return nil, fmt.Errorf("agent: prompt file: %w", err)
	}
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("agent: prompt file: unlink: %w", err)
	}
	return f, nil
}

// localPromptRedirect is appended to the local login-shell command: the
// CLI's stdin is the prompt file passed as fd 3 (setPromptFD3). /dev/fd/N
// exists on Linux and macOS; on Linux opening it reopens the unlinked file
// through /proc, on macOS it dups the descriptor (offset 0 either way).
const localPromptRedirect = " < /dev/fd/3"

// setPromptFD3 passes f to the login shell as fd 3 (ExtraFiles[0]), for
// localPromptRedirect; stdin stays /dev/null.
func setPromptFD3(cmd *exec.Cmd, f *os.File) {
	if f != nil {
		cmd.ExtraFiles = []*os.File{f}
	}
}
