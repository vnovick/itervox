package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/procgroup"
)

// CodexRunner spawns a codex subprocess and streams its --json output.
type CodexRunner struct{}

// NewCodexRunner constructs a CodexRunner.
func NewCodexRunner() *CodexRunner {
	return &CodexRunner{}
}

// ValidateCodexCLI checks if the codex CLI is available and returns an error
// describing the problem if it cannot be found or executed.
func ValidateCodexCLI() error {
	return validateCLI("codex", "ensure 'codex' is installed and on PATH, or set OPENAI_API_KEY")
}

// ValidateCodexCLICommand is like ValidateCodexCLI but validates a specific
// command path. Falls back to ValidateCodexCLI when command is empty or "codex".
func ValidateCodexCLICommand(command string) error {
	if command == "" || command == "codex" {
		return ValidateCodexCLI()
	}
	return validateCLI(command, "ensure 'codex' is installed and on PATH, or set OPENAI_API_KEY")
}

// RunTurn runs a single codex turn as a subprocess.
//
// Fresh turn (sessionID == nil):
//
//	codex [-C <workspace>] exec --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check -
//
// Continuation (sessionID != nil):
//
//	codex [-C <workspace>] exec resume --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check <sessionID> -
//
// The prompt argument is always `-` (CORE-156): codex reads the prompt from
// its stdin until EOF (see promptFile and remotePromptRedirect).
func (c *CodexRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, logDir string,
	readTimeoutMs, turnTimeoutMs int,
	permissionMode PermissionMode,
) (TurnResult, error) {
	if err := rejectNULPrompt(prompt); err != nil {
		return TurnResult{Failed: true, FailureText: err.Error()}, err
	}
	// turnCtx is always cancellable, even when the hard turn timeout is
	// disabled (turnTimeoutMs <= 0): on a read error we must be able to kill
	// the subprocess immediately rather than relying on a no-op cancel that
	// leaves it running until the caller's ctx eventually ends (CORE-001).
	turnCtx, cancel := context.WithCancel(ctx)
	if turnTimeoutMs > 0 {
		var timeoutCancel context.CancelFunc
		turnCtx, timeoutCancel = context.WithTimeout(turnCtx, time.Duration(turnTimeoutMs)*time.Millisecond)
		baseCancel := cancel
		cancel = func() {
			timeoutCancel()
			baseCancel()
		}
	}
	defer cancel()

	// Generate a unique log filename per turn so multi-turn and multi-run logs
	// don't overwrite each other — matching Claude Code's per-session file behavior.
	logFileName := fmt.Sprintf("codex-%d.jsonl", time.Now().UnixMilli())

	var cmd *exec.Cmd
	var stdinFeed *remoteStdin // SSH only: the script, then held open (CORE-155)
	var promptIn *os.File      // local only: the prompt, for the CLI's stdin (CORE-156)
	defer func() { closePromptFile(promptIn) }()
	if workerHost != "" {
		shellCmd := remotePromptAssignment(prompt) + buildCodexShellCmd(command, sessionID, workspacePath, remotePromptRedirect, permissionMode)
		shellCmd = itervoxAgentExportPrefix() + shellCmd
		if logDir != "" {
			// Tee codex stdout to a file on the remote host so sshFetchLogs can read it later.
			shellCmd = shellCmd + " | tee " + ShellQuote(filepath.Join(logDir, logFileName))
		}
		if workspacePath != "" {
			shellCmd = "cd " + ShellQuote(workspacePath) + "; " + shellCmd
		}
		if logDir != "" {
			// pipefail (CORE-139): a bash pipeline's status is its LAST
			// command's, so without it `codex ... | tee <log>` reports tee's
			// exit 0 even when codex crashed, and ssh relays that 0 — the
			// turn came back as a successful 0-token session end. With
			// pipefail the pipeline fails with codex's status (or tee's, if
			// only tee failed, which it already did before). The script runs
			// under bash (remoteBashInvocation below), so pipefail exists.
			shellCmd = "set -o pipefail; mkdir -p " + ShellQuote(logDir) + " || true; " + shellCmd
		}
		// set -e (CORE-154): a failed cd aborts before the agent starts,
		// instead of running it in the remote HOME.
		shellCmd = "set -e; " + shellCmd
		// -T: never a PTY (round 3, m3), and the remote agent is stopped on
		// cancel by the kill-on-EOF wrapper instead (CORE-155) — see the note
		// in ClaudeRunner.RunTurn. The pipefail/tee pipeline above runs
		// inside the wrapper's agent process group; its status is the
		// wrapper's exit status.
		sshArgs := []string{"-T"}
		sshArgs = append(sshArgs, sshStrictHostOption(workerHost)...)
		remoteArg, payload := remoteBashInvocation("-lc", shellCmd)
		sshArgs = append(sshArgs, "-o", "BatchMode=yes", workerHost, remoteArg)
		cmd = exec.CommandContext(turnCtx, "ssh", sshArgs...)
		var err error
		if stdinFeed, err = attachRemoteStdin(cmd, payload); err != nil {
			return TurnResult{Failed: true}, err
		}
	} else {
		var err error
		if promptIn, err = promptFile(prompt); err != nil {
			return TurnResult{Failed: true, FailureText: err.Error()}, err
		}
		if filepath.IsAbs(command) && !strings.Contains(command, " ") {
			cmd = exec.CommandContext(turnCtx, command, buildCodexDirectArgs(sessionID, workspacePath, permissionMode)...)
			setPromptStdin(cmd, promptIn)
		} else {
			// fd 3, not stdin: a login profile that reads stdin cannot eat
			// the prompt (see ClaudeRunner.RunTurn).
			cmd = exec.CommandContext(turnCtx, loginShell(), "-lc",
				buildCodexShellCmd(command, sessionID, workspacePath, localPromptRedirect, permissionMode))
			setPromptFD3(cmd, promptIn)
		}
	}
	setProcessGroup(cmd)
	if workspacePath != "" && workerHost == "" {
		cmd.Dir = workspacePath
	}
	// Set unconditionally: Go inherits the parent environment when cmd.Env is
	// nil, so leaving it unset on any path silently drops the marker. For the
	// SSH path cmd.Env applies to the local ssh process only — the marker
	// travels in the shell command instead (see above).
	cmd.Env = itervoxAgentEnv()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{Failed: true}, fmt.Errorf("codex: stdout pipe: %w", err)
	}

	// For local workers, tee stdout to codex-session.jsonl so sshFetchLogs / parseSessionLogsMulti
	// can read the session transcript after the run.
	var logFile *os.File
	if logDir != "" && workerHost == "" {
		if mkErr := os.MkdirAll(logDir, 0o755); mkErr != nil {
			slog.Warn("codex: failed to create log dir", "dir", logDir, "error", mkErr)
		} else if f, createErr := os.Create(filepath.Join(logDir, logFileName)); createErr != nil {
			slog.Warn("codex: failed to create session log", "error", createErr)
		} else {
			logFile = f
		}
	}

	var reader io.Reader = stdout
	if logFile != nil {
		reader = io.TeeReader(stdout, logFile)
	}

	// Tail-bounded (CORE-028): only the last maxStderrCaptureBytes are
	// kept, however long the turn runs and however much the agent prints.
	stderrBuf := newTailBuffer(maxStderrCaptureBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return TurnResult{Failed: true}, fmt.Errorf("codex: start: %w", err)
	}
	stdinFeed.start()
	if workerHost == "" {
		// CORE-042: record the local group in the orphan ledger until Wait
		// has returned (the deferred call runs after cmd.Wait below).
		defer procgroup.Track(ctx, cmd)()
	}

	result, readErr := readLines(turnCtx, log, onProgress, reader, readTimeoutMs, "codex", ParseCodexLine)
	if logFile != nil {
		_ = logFile.Close()
	}

	if readErr != nil {
		// The stream ended abnormally (idle read timeout or a read error such as a terminal line over maxStreamLineBytes)
		// rather than a clean EOF/result event. Cancel the turn context now —
		// before cmd.Wait() — so cmd.Cancel (SIGKILL to the process group,
		// configured in setProcessGroup) and WaitDelay bound the wait below
		// instead of leaving the subprocess running unobserved until the
		// caller's context or the hard turn timeout eventually fires.
		cancel()
		result.Failed = true
	}

	waitErr := cmd.Wait()
	stdinFeed.join() // Wait closed ssh's stdin pipe; the writer has returned
	if waitErr != nil && readErr == nil {
		result.Failed = true
	}

	// resolveFailureText (internal/agent/failure_text.go) is the single
	// shared classification home for both Claude and Codex — see its doc
	// comment for the three-way rule (T-53 / CORE-001).
	if result.Failed {
		result.FailureText = resolveFailureText(result.FailureText, stderrBuf.String(), waitErr, readErr)
	}

	if readErr != nil {
		return result, readErr
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens
	result = FinalizeResult(result)
	return result, nil
}

// buildCodexDirectArgs returns CLI args for direct (non-shell) invocation.
// The prompt argument is `-` (read stdin; CORE-156), which also retires the
// argv hazard of a prompt starting with '-'.
func buildCodexDirectArgs(sessionID *string, workspacePath string, mode PermissionMode) []string {
	args := make([]string, 0, 8)
	if workspacePath != "" {
		args = append(args, "-C", workspacePath)
	}
	args = append(args, "exec")
	if sessionID != nil && *sessionID != "" {
		args = append(args, "resume", "--json")
		args = append(args, codexPermissionFlags(mode)...)
		args = append(args, "--skip-git-repo-check", *sessionID, "-")
		return args
	}
	args = append(args, "--json")
	args = append(args, codexPermissionFlags(mode)...)
	args = append(args, "--skip-git-repo-check", "-")
	return args
}

// buildCodexShellCmd returns the shell command for a codex turn. It ends in
// the `-` prompt argument followed by promptRedirect, which puts the prompt
// on codex's stdin (localPromptRedirect or remotePromptRedirect).
func buildCodexShellCmd(command string, sessionID *string, workspacePath, promptRedirect string, mode PermissionMode) string {
	var b strings.Builder
	b.WriteString(command)
	if workspacePath != "" {
		b.WriteString(" -C ")
		b.WriteString(ShellQuote(workspacePath))
	}
	b.WriteString(" exec")
	if sessionID != nil && *sessionID != "" {
		b.WriteString(" resume --json")
		for _, f := range codexPermissionFlags(mode) {
			b.WriteString(" " + f)
		}
		b.WriteString(" --skip-git-repo-check ")
		b.WriteString(ShellQuote(*sessionID))
		b.WriteString(" -")
		b.WriteString(promptRedirect)
		return b.String()
	}
	b.WriteString(" --json")
	for _, f := range codexPermissionFlags(mode) {
		b.WriteString(" " + f)
	}
	b.WriteString(" --skip-git-repo-check -")
	b.WriteString(promptRedirect)
	return b.String()
}
