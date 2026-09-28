package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Prompt delivery (CORE-156). The prompt — SOUL + INSTRUCTIONS + the issue
// template + every prior handoff — used to be ONE argv string to the agent
// CLI. Linux caps a single argv string at MAX_ARG_STRLEN (131072 bytes,
// NUL included) and every OS caps argv+env (ARG_MAX; 1 MiB on macOS), so a
// long pipeline could not start its agent at all: locally exec failed with
// E2BIG, and over SSH the wrapper refused with exit 98. The prompt now
// reaches the CLI on its stdin, which is at EOF right after the last prompt
// byte:
//   - claude: `-p` with no prompt argument reads stdin (verified on claude
//     2.1.283: an empty stdin fails with "Input must be provided either
//     through stdin or as a prompt argument when using --print"; piped
//     stdin is capped at 10 MiB by the CLI itself);
//   - codex: the prompt argument `-` reads stdin, for `exec` and for
//     `exec resume <id>` (verified on codex-cli 0.157.0: an empty stdin
//     fails with "No prompt provided via stdin.").
//
// Local turns: the prompt is written to an already-unlinked temp file
// (promptFile). The direct path makes it the CLI's stdin; the login-shell
// path passes it as fd 3 and the command redirects `< /dev/fd/3`
// (localPromptRedirect), so a login profile that reads stdin sees
// /dev/null as before and cannot eat the prompt. `< path` is the same in
// sh, bash, zsh, dash, ksh and fish. SSH turns: see remotePromptRedirect.

// localPromptRedirect is appended to the local login-shell command: the
// CLI's stdin is the prompt file passed as fd 3 (setPromptFD3). /dev/fd/N
// exists on Linux and macOS; on Linux opening it reopens the unlinked file
// through /proc, on macOS it dups the descriptor (offset 0 either way).
const localPromptRedirect = " < /dev/fd/3"

// localPromptRedirectFor returns localPromptRedirect when the turn has a
// prompt file, and nothing otherwise (a bare claude resume).
func localPromptRedirectFor(f *os.File) string {
	if f == nil {
		return ""
	}
	return localPromptRedirect
}

// promptFile returns a read-only-at-offset-0 handle on an unlinked temp
// file (mode 0600, under $TMPDIR) holding prompt. It is unlinked before any
// process starts, so nothing is left on disk on any exit path — a failed
// start, a cancel, or the daemon itself being killed — and no cleanup can
// be missed; the open descriptor is what the agent reads. A file (not a
// pipe) needs no writer goroutine and never blocks, whether or not the
// agent reads it.
func promptFile(prompt string) (*os.File, error) {
	f, err := os.CreateTemp("", "itervox-prompt-*")
	if err != nil {
		return nil, fmt.Errorf("agent: prompt file: %w", err)
	}
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("agent: prompt file: unlink: %w", err)
	}
	if _, err := io.WriteString(f, prompt); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("agent: prompt file: write: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("agent: prompt file: seek: %w", err)
	}
	return f, nil
}

// closePromptFile releases the parent's handle (the child has its own); a
// nil file is a no-op.
func closePromptFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

// setPromptStdin makes f the direct-exec CLI's stdin; with no prompt file
// stdin stays /dev/null.
func setPromptStdin(cmd *exec.Cmd, f *os.File) {
	if f != nil {
		cmd.Stdin = f
	}
}

// setPromptFD3 passes f to the login shell as fd 3 (ExtraFiles[0]), for
// localPromptRedirect; stdin stays /dev/null.
func setPromptFD3(cmd *exec.Cmd, f *os.File) {
	if f != nil {
		cmd.ExtraFiles = []*os.File{f}
	}
}
